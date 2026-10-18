package dbmgr

// 主从同步 v1:主库 = 本机面板 MySQL/MariaDB 实例;从库 = 任意可达的
// MySQL/MariaDB(另一台服务器的库、本机另一个容器等,需 root 密码)。
// 经典 binlog position 复制:坐标取自 mysqldump --master-data 写在导出文件
// 头部的 CHANGE MASTER 行,兼容 MySQL 5.7/8.x 与 MariaDB。
// 主库 binlog 配置写入容器 /etc/mysql/conf.d/kunrepl.cnf 并重启生效;
// 容器重建会丢该文件,状态查询时自动检测并补写。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
)

// ReplState 主从同步状态(含从库/复制账号密码,0600 落盘;API 回显时另行脱敏,
// 不把这两个字段放进响应 map)
type ReplState struct {
	Key        string   `json:"key"` // 主库实例键
	SlaveHost  string   `json:"slave_host"`
	SlavePort  string   `json:"slave_port"`
	MasterHost string   `json:"master_host"` // 从库回连主库的地址(同 Docker 网络填容器名,跨机填可达 IP)
	MasterPort string   `json:"master_port"` // 从库回连主库的端口(通常容器内 3306)
	ReplUser   string   `json:"repl_user"`
	ReplPass   string   `json:"repl_pass"`  // 复制账号密码(状态/启停用)
	SlavePass  string   `json:"slave_pass"` // 从库 root 密码
	DbList     []string `json:"db_list"`    // 初始化导入的业务库
	MasterLog  string   `json:"master_log"` // 初始化起点
	MasterPos  string   `json:"master_pos"`
	Created    string   `json:"created"`
}

func replStatePath() string { return filepath.Join(config.DataDir(), "dbrepl.json") }

// GetReplState 当前主从配置(未配置返回 nil)
func GetReplState() *ReplState {
	b, err := os.ReadFile(replStatePath())
	if err != nil {
		return nil
	}
	var st ReplState
	if err := json.Unmarshal(b, &st); err != nil || st.Key == "" {
		return nil
	}
	return &st
}

func saveReplState(st *ReplState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(replStatePath(), b, 0o600)
}

// ClearReplState 解除主从后清除本地记录
func ClearReplState() { _ = os.Remove(replStatePath()) }

// ---------- 通用小件 ----------

func masterClientBin(key string) string {
	if key == "mariadb" {
		return "mariadb"
	}
	return "mysql"
}

func masterDumpBin(key string) string {
	if key == "mariadb" {
		return "mariadb-dump"
	}
	return "mysqldump"
}

// replServerID 由实例键生成稳定且主从不同的 server-id
func replServerID(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return 100 + int(h.Sum32()%100000)
}

// ---------- 主库侧 ----------

// ensureMasterBinlog 确保 binlog 开启(8.0+ 默认已开则零操作);
// 返回是否执行了重启(应用会短暂断连)
func ensureMasterBinlog(key string, w io.Writer) (bool, error) {
	out, err := Query(key, "", "SHOW VARIABLES LIKE 'log_bin'")
	if err != nil {
		return false, fmt.Errorf("主库连接失败: %s", tailStr(out))
	}
	if strings.Contains(out, "\tON") || strings.HasSuffix(strings.TrimSpace(out), "\tON") || strings.Contains(out, "ON") {
		fmt.Fprintln(w, ">> 主库 binlog 已开启,无需重启")
		return false, nil
	}
	fmt.Fprintln(w, ">> 主库未开启 binlog,写入配置并重启数据库容器(应用会短暂断连)...")
	conf := "[mysqld]\nserver-id=" + strconv.Itoa(replServerID(key)) + "\nlog-bin=mysql-bin\nbinlog-format=ROW\n"
	full := []string{"docker", "exec", "kun-" + key, "sh", "-c",
		"mkdir -p /etc/mysql/conf.d && printf %s " + shellQuote(conf) + " > /etc/mysql/conf.d/kunrepl.cnf"}
	if out, err := util.RunWithTimeout(15*time.Second, full[0], full[1:]...); err != nil {
		return false, fmt.Errorf("写入主库配置失败: %s", tailStr(out))
	}
	return true, restartAndWait(key, w)
}

// restartAndWait 重启数据库容器并等待恢复可连接(最长 60s)
func restartAndWait(key string, w io.Writer) error {
	fmt.Fprintln(w, ">> 重启数据库容器 ...")
	if _, err := util.RunWithTimeout(60*time.Second, "docker", "restart", "kun-"+key); err != nil {
		return fmt.Errorf("重启容器失败: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		if _, err := Query(key, "", "SELECT 1"); err == nil {
			fmt.Fprintln(w, ">> 数据库已恢复运行")
			return nil
		}
	}
	return fmt.Errorf("数据库重启后 60 秒仍未恢复连接,请到应用商店检查容器状态")
}

// ensureReplUser 确保复制账号存在且密码正确
func ensureReplUser(key, user, pass string) error {
	if !dbUserNameRe.MatchString(user) {
		return fmt.Errorf("复制账号名格式不正确")
	}
	if err := validateUserPassword(pass); err != nil {
		return fmt.Errorf("复制账号密码: %v", err)
	}
	// MySQL 8 的 caching_sha2 在 5.7 从库上有兼容坑,统一用 mysql_native_password
	if _, err := Query(key, "", "CREATE USER IF NOT EXISTS '"+user+"'@'%' IDENTIFIED WITH mysql_native_password BY '"+pass+"'"); err != nil {
		// 账号已存在但插件不同/旧版本不支持 IF NOT EXISTS 组合时退回 ALTER
		if _, e2 := Query(key, "", "ALTER USER '"+user+"'@'%' IDENTIFIED WITH mysql_native_password BY '"+pass+"'"); e2 != nil {
			return fmt.Errorf("创建复制账号失败: %s", tailStr(err.Error())+" / "+tailStr(e2.Error()))
		}
	}
	_, err := Query(key, "", "GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO '"+user+"'@'%'")
	return err
}

// masterStatus 当前 binlog 坐标
func masterStatus(key string) (file, pos string, err error) {
	out, err := Query(key, "", "SHOW MASTER STATUS")
	if err != nil {
		// MySQL 8.4 起移除 SHOW MASTER STATUS,退回新语句
		if out2, e2 := Query(key, "", "SHOW BINARY LOG STATUS"); e2 == nil {
			out, err = out2, nil
		} else {
			return "", "", fmt.Errorf("读取主库 binlog 坐标失败: %s", tailStr(out))
		}
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimRight(l, "\r"))
		if len(f) >= 2 && (strings.HasPrefix(f[0], "mysql-bin") || strings.HasPrefix(f[0], "binlog") || strings.Contains(f[0], "-bin")) {
			return f[0], f[1], nil
		}
	}
	return "", "", fmt.Errorf("主库 binlog 坐标解析失败: %s", tailStr(out))
}

// ---------- 从库侧(经主库容器的 mysql 客户端连出) ----------

func slaveExec(st *ReplState, sql string) (string, error) {
	full := []string{"docker", "exec", "-e", "MYSQL_PWD=" + st.SlavePass, "kun-" + st.Key,
		masterClientBin(st.Key), "-uroot", "-h", st.SlaveHost, "-P", st.SlavePort,
		"--connect-timeout=10", "--batch", "--raw", "-e", sql}
	out, err := util.RunWithTimeout(60*time.Second, full[0], full[1:]...)
	if err != nil {
		return out, fmt.Errorf("%v | %s", err, tailStr(out))
	}
	return out, nil
}

var replDumpFileRe = regexp.MustCompile(`(?:MASTER_LOG_FILE|SOURCE_LOG_FILE)='([^']+)'`)
var replDumpPosRe = regexp.MustCompile(`(?:MASTER_LOG_POS|SOURCE_LOG_POS)=([0-9]+)`)

// parseReplCoords 从 mysqldump 头部解析 CHANGE MASTER 坐标
func parseReplCoords(head string) (file, pos string, err error) {
	for _, l := range strings.Split(head, "\n") {
		if !strings.Contains(l, "CHANGE MASTER TO") && !strings.Contains(l, "CHANGE REPLICATION SOURCE TO") {
			continue
		}
		f := replDumpFileRe.FindStringSubmatch(l)
		p := replDumpPosRe.FindStringSubmatch(l)
		if f != nil && p != nil {
			return f[1], p[1], nil
		}
	}
	return "", "", fmt.Errorf("导出文件中未找到 CHANGE MASTER 坐标")
}

// slaveVersionFlavor 从库系列校验(MySQL↔MySQL、MariaDB↔MariaDB)
func checkSlaveFlavor(key string, st *ReplState) error {
	out, err := slaveExec(st, "SELECT VERSION()")
	if err != nil {
		return fmt.Errorf("从库连接失败(请检查地址/端口/防火墙与 root 密码): %s", tailStr(out))
	}
	slaveMaria := strings.Contains(out, "MariaDB")
	masterMaria := key == "mariadb"
	if slaveMaria != masterMaria {
		return fmt.Errorf("主从需为同系列数据库:主库是 %s,从库是 %s(MySQL 与 MariaDB 不能互为主从)", mariaOrMySQL(masterMaria), mariaOrMySQL(slaveMaria))
	}
	return nil
}

func mariaOrMySQL(m bool) string {
	if m {
		return "MariaDB"
	}
	return "MySQL"
}

// ---------- 主流程(任务体内执行) ----------

// ReplSetupOpts 主从配置参数
type ReplSetupOpts struct {
	Key        string // 主库实例键
	SlaveHost  string
	SlavePort  string
	MasterHost string // 从库回连主库的地址(同 Docker 网络填容器名,跨机填可达 IP)
	MasterPort string // 从库回连主库的端口(默认 3306)
	ReplUser   string
	ReplPass   string
	SlavePass  string   // 从库 root 密码
	InitDump   bool     // 全量初始化(从库业务库必须为空)
	DbList     []string // 初始化的库(空=全部业务库)
}

// ReplSetup 主从配置任务体
func ReplSetup(opts ReplSetupOpts, w io.Writer) error {
	if !dbUserSupported(opts.Key) {
		return fmt.Errorf("仅 MySQL/MariaDB 支持主从同步")
	}
	if !dockerx.ContainerRunning("kun-" + opts.Key) {
		return fmt.Errorf("主库容器未运行")
	}
	opts.SlaveHost = strings.TrimSpace(opts.SlaveHost)
	opts.SlavePort = strings.TrimSpace(opts.SlavePort)
	if opts.SlaveHost == "" || !dbUserHostRe.MatchString(opts.SlaveHost) {
		return fmt.Errorf("从库地址格式不正确")
	}
	if opts.SlavePort == "" {
		opts.SlavePort = "3306"
	}
	if _, err := strconv.Atoi(opts.SlavePort); err != nil || len(opts.SlavePort) > 5 {
		return fmt.Errorf("从库端口格式不正确")
	}
	opts.MasterHost = strings.TrimSpace(opts.MasterHost)
	if !dbUserHostRe.MatchString(opts.MasterHost) {
		return fmt.Errorf("主库地址格式不正确(从库回连主库用)")
	}
	if opts.MasterPort == "" {
		opts.MasterPort = "3306"
	}
	if _, err := strconv.Atoi(opts.MasterPort); err != nil || len(opts.MasterPort) > 5 {
		return fmt.Errorf("主库复制端口格式不正确")
	}
	if opts.ReplUser == "" {
		opts.ReplUser = "repl"
	}

	// 1) 主库 binlog(可能重启容器)
	restarted, err := ensureMasterBinlog(opts.Key, w)
	if err != nil {
		return err
	}
	_ = restarted
	// 2) 复制账号
	fmt.Fprintf(w, ">> 创建复制账号 %s ...", opts.ReplUser)
	if err := ensureReplUser(opts.Key, opts.ReplUser, opts.ReplPass); err != nil {
		return err
	}
	fmt.Fprintln(w, " 完成")

	st := &ReplState{Key: opts.Key, SlaveHost: opts.SlaveHost, SlavePort: opts.SlavePort, MasterHost: opts.MasterHost, MasterPort: opts.MasterPort,
		ReplUser: opts.ReplUser, ReplPass: opts.ReplPass, SlavePass: opts.SlavePass}
	// 3) 从库连通性 + 系列校验 + server-id 配置探测
	fmt.Fprintln(w, ">> 连接从库并校验版本系列 ...")
	if err := checkSlaveFlavor(opts.Key, st); err != nil {
		return err
	}
	if err := probeSlaveConfig(st); err != nil {
		return err
	}
	// 4) 全量初始化
	if opts.InitDump {
		if err := replInitDump(opts, st, w); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(w, ">> 跳过全量初始化(从库需自行保证数据与主库一致)")
		st.MasterLog, st.MasterPos, err = masterStatus(opts.Key)
		if err != nil {
			return err
		}
		// 未初始化时从当前坐标起步(数据可能不一致,仅高级用法)
		if err := replStartSlave(st, w); err != nil {
			return err
		}
	}
	st.Created = time.Now().Format("2006-01-02 15:04:05")
	if err := saveReplState(st); err != nil {
		return err
	}
	fmt.Fprintln(w, ">> 主从同步已建立,可用「同步状态」持续观察 IO/SQL 线程与延迟")
	return nil
}

// replInitDump 全量初始化:导出(含坐标)→ 导入从库 → CHANGE MASTER → START
func replInitDump(opts ReplSetupOpts, st *ReplState, w io.Writer) error {
	dbs := opts.DbList
	if len(dbs) == 0 {
		all, err := Databases(opts.Key)
		if err != nil {
			return err
		}
		for _, d := range all {
			if !systemDBs[d] {
				dbs = append(dbs, d)
			}
		}
	}
	if len(dbs) == 0 {
		return fmt.Errorf("主库没有业务库可初始化")
	}
	st.DbList = dbs
	// 从库同名业务库必须为空(防止覆盖生产数据)
	existing, err := slaveExec(st, "SHOW DATABASES")
	if err != nil {
		return fmt.Errorf("读取从库库列表失败: %s", tailStr(existing))
	}
	var clash []string
	for _, d := range dbs {
		for _, l := range strings.Split(existing, "\n") {
			if strings.TrimSpace(l) == d {
				clash = append(clash, d)
			}
		}
	}
	if len(clash) > 0 {
		return fmt.Errorf("从库已存在同名库: %s;请先清理从库或去掉这些库再试", strings.Join(clash, "、"))
	}

	tmp := filepath.Join(config.DataDir(), "kunrepl_dump.sql")
	defer os.Remove(tmp)
	dumpArgs := " --single-transaction --master-data=2 --routines --triggers --events --databases " + strings.Join(dbs, " ")
	inner := "export MYSQL_PWD=" + shellQuote(passwordFor0(opts.Key)) + "; " + masterDumpBin(opts.Key) + " -uroot" + dumpArgs
	fmt.Fprintf(w, ">> 导出主库数据(%d 个库,大库可能需要数分钟)...", len(dbs))
	if err := runToWriter(30*time.Minute, []string{"docker", "exec", "kun-" + opts.Key, "sh", "-c", inner}, tmp, w); err != nil {
		return fmt.Errorf("主库导出失败: %v", err)
	}
	head := readHead(tmp, 64*1024)
	file, pos, err := parseReplCoords(head)
	if err != nil {
		return err
	}
	st.MasterLog, st.MasterPos = file, pos
	fmt.Fprintf(w, " 完成(起点 %s:%s)\n", file, pos)

	fmt.Fprintln(w, ">> 导入从库 ...")
	impInner := "export MYSQL_PWD=" + shellQuote(st.SlavePass) + "; " + masterClientBin(opts.Key) + " -uroot -h " + st.SlaveHost + " -P " + st.SlavePort + " --default-character-set=utf8mb4"
	if err := runFromFile(30*time.Minute, []string{"sh", "-c",
		"docker exec -i kun-" + opts.Key + " sh -c " + shellQuote(impInner) + " < " + shellQuote(tmp)}, tmp, w); err != nil {
		return fmt.Errorf("从库导入失败: %v", err)
	}
	return replStartSlave(st, w)
}

// probeSlaveConfig 外部从库的 server-id 必须写在配置文件里随启动生效——
// MySQL 5.7 的复制校验认启动参数,运行时 SET GLOBAL server_id 不算数。
// 用 STOP SLAVE 探测:1794 = 从库缺 server-id 配置,提前失败并给出明确指引
// (在全量导出之前失败,不浪费导出;已在复制的从库会短暂 STOP,稍后自动恢复)
func probeSlaveConfig(st *ReplState) error {
	out, err := slaveExec(st, "STOP SLAVE")
	if err != nil {
		low := strings.ToLower(out)
		if strings.Contains(out, "1794") || strings.Contains(low, "server-id") || strings.Contains(low, "server_id") {
			return fmt.Errorf("从库未在配置文件中设置 server-id(MySQL 5.7 复制校验只认启动参数,运行时 SET GLOBAL 不算数)。请在从库 my.cnf 的 [mysqld] 段加入 server-id=<数字>(与主库不同)并重启从库数据库后重试")
		}
		return fmt.Errorf("从库预检失败: %s", tailStr(out))
	}
	return nil
}

// replStartSlave 在从库配置并启动复制
func replStartSlave(st *ReplState, w io.Writer) error {
	fmt.Fprintln(w, ">> 配置并启动复制 ...")
	sql := "STOP SLAVE; RESET SLAVE ALL; " +
		"CHANGE MASTER TO MASTER_HOST='" + st.MasterHost + "', MASTER_PORT=" + st.MasterPort +
		", MASTER_USER='" + st.ReplUser + "', MASTER_PASSWORD='" + st.ReplPass +
		"', MASTER_LOG_FILE='" + st.MasterLog + "', MASTER_LOG_POS=" + st.MasterPos + ";" +
		" START SLAVE;"
	if out, err := slaveExec(st, sql); err != nil {
		return fmt.Errorf("从库配置失败: %s", tailStr(out))
	}
	return nil
}

// ReplStopSlave / ReplStartSlave 暂停 / 恢复复制
func ReplStopSlave() error {
	st := GetReplState()
	if st == nil {
		return fmt.Errorf("尚未配置主从同步")
	}
	_, err := slaveExec(st, "STOP SLAVE")
	return err
}

func ReplStartSlave() error {
	st := GetReplState()
	if st == nil {
		return fmt.Errorf("尚未配置主从同步")
	}
	_, err := slaveExec(st, "START SLAVE")
	return err
}

// ReplReset 解除主从(从库停止并清除复制配置),清掉本地状态
func ReplReset() error {
	st := GetReplState()
	if st == nil {
		return nil
	}
	_, _ = slaveExec(st, "STOP SLAVE")
	_, err := slaveExec(st, "RESET SLAVE ALL")
	ClearReplState()
	return err
}

// ReplStatus 实时状态(从库 SHOW SLAVE STATUS + 主库自动补写 binlog 配置)
func ReplStatus() (map[string]any, error) {
	st := GetReplState()
	out := map[string]any{"configured": st != nil}
	if st == nil {
		return out, nil
	}
	out["key"] = st.Key
	out["slave_host"] = st.SlaveHost
	out["slave_port"] = st.SlavePort
	out["repl_user"] = st.ReplUser
	out["db_list"] = st.DbList
	out["created"] = st.Created
	out["master_log"] = st.MasterLog
	out["master_pos"] = st.MasterPos

	// 主库 binlog 配置自愈:容器重建丢 conf 时补写并重启
	if _, err := Query(st.Key, "", "SELECT 1"); err == nil {
		if chk, e := util.RunWithTimeout(10*time.Second, "docker", "exec", "kun-"+st.Key,
			"sh", "-c", "test -f /etc/mysql/conf.d/kunrepl.cnf"); e != nil || strings.TrimSpace(chk) != "" {
			var b strings.Builder
			if _, err2 := ensureMasterBinlog(st.Key, &b); err2 == nil {
				if strings.Contains(b.String(), "重启") {
					out["healed"] = true
				}
			}
		}
	}

	raw, err := slaveExec(st, "SHOW SLAVE STATUS")
	if err != nil {
		out["error"] = tailStr(raw)
		return out, nil
	}
	sts := parseSlaveStatus(raw)
	out["io_running"] = sts["Slave_IO_Running"]
	out["sql_running"] = sts["Slave_SQL_Running"]
	out["seconds_behind"] = sts["Seconds_Behind_Master"]
	out["last_io_error"] = sts["Last_IO_Error"]
	out["last_sql_error"] = sts["Last_SQL_Error"]
	out["slave_master_log"] = sts["Master_Log_File"]
	out["slave_read_pos"] = sts["Read_Master_Log_Pos"]
	if f, p, e := masterStatus(st.Key); e == nil {
		out["master_log_now"] = f
		out["master_pos_now"] = p
	}
	return out, nil
}

// parseSlaveStatus 解析 SHOW SLAVE STATUS 的 TSV 输出(列名→值)
func parseSlaveStatus(raw string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) < 2 {
		return out
	}
	cols := strings.Split(strings.TrimRight(lines[0], "\r"), "\t")
	vals := strings.Split(strings.TrimRight(lines[1], "\r"), "\t")
	for i, c := range cols {
		if i < len(vals) {
			out[strings.TrimSpace(c)] = strings.TrimSpace(vals[i])
		}
	}
	return out
}

// ---------- 文件流辅助 ----------

func passwordFor0(key string) string {
	p, _ := passwordFor(key)
	return p
}

// runToWriter 执行命令并把 stdout 写入文件(任务日志报进度)
func runToWriter(timeout time.Duration, cmd []string, dst string, w io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Env = util.ChildEnv()
	out, err := os.Create(dst + ".tmp")
	if err != nil {
		return err
	}
	c.Stdout = out
	c.Stderr = io.Discard
	if err := c.Start(); err != nil {
		out.Close()
		return err
	}
	// 每秒报一次落盘大小,便于观察大库导出进度
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if fi, e := os.Stat(dst + ".tmp"); e == nil {
					fmt.Fprintf(w, "\r>> 已导出 %s", humanSize(fi.Size()))
				}
			}
		}
	}()
	err = c.Wait()
	close(done)
	out.Close()
	if err != nil {
		os.Remove(dst + ".tmp")
		return err
	}
	return os.Rename(dst+".tmp", dst)
}

// runFromFile 执行命令并以文件内容作为 stdin(任务日志报进度)
func runFromFile(timeout time.Duration, cmd []string, src string, w io.Writer) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Env = util.ChildEnv()
	c.Stdin = f
	c.Stderr = io.Discard
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if fi, e := os.Stat(src); e == nil {
					pos := 0
					if s, e := f.Seek(0, io.SeekCurrent); e == nil {
						pos = int(s)
					}
					fmt.Fprintf(w, "\r>> 已导入 %s / %s", humanSize(int64(pos)), humanSize(fi.Size()))
				}
			}
		}
	}()
	err = c.Wait()
	close(done)
	fmt.Fprintln(w)
	return err
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func readHead(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, n)
	read, _ := io.ReadFull(bufio.NewReader(f), b)
	if read < 0 {
		read = 0
	}
	return string(b[:read])
}
