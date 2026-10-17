// Package dbmgr 数据库管理:通过 docker exec 直连面板安装的数据库容器,
// 提供 SQL 控制台、库列表与一键备份(无需用户输入数据库密码)。
package dbmgr

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os/exec"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/appstore"
	"kunpanel/internal/config"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
)

// Instance 一个可管理的数据库容器
type Instance struct {
	Key     string `json:"key"` // mysql / mariadb / postgresql
	Name    string `json:"name"`
	Type    string `json:"type"` // mysql / postgres
	Running bool   `json:"running"`
	// Databases 该实例的业务库数量(排除系统库;未运行时为 0)。
	// 概览 KPI 与数据库页 hero 用同一口径,30s 缓存,建/删库时主动失效
	Databases int `json:"databases,omitempty"`
}

var supported = []Instance{
	{Key: "mysql", Name: "MySQL", Type: "mysql"},
	{Key: "mariadb", Name: "MariaDB", Type: "mysql"},
	{Key: "postgresql", Name: "PostgreSQL", Type: "postgres"},
}

// Instances 检测已安装的数据库容器(含运行实例的业务库数量)
func Instances() []Instance {
	out := make([]Instance, len(supported))
	copy(out, supported)
	// 一次 docker ps 批量取状态(此前逐实例起子进程)
	states := dockerx.ContainerStates()
	for i := range out {
		out[i].Running = states["kun-"+out[i].Key] == "running"
		if out[i].Running {
			out[i].Databases = businessDBCount(out[i].Key)
		}
	}
	return out
}

// ---------- 业务库数量缓存(概览 KPI 用,避免每次切页都对每个实例 exec) ----------

const dbCountTTL = 30 * time.Second

type dbCountEntry struct {
	n  int
	at time.Time
}

var (
	dbCountMu    sync.Mutex
	dbCountCache = map[string]dbCountEntry{}
)

// businessDBCount 运行实例的业务库数量(排除系统库),30s 缓存;查询失败按 0 处理
func businessDBCount(key string) int {
	dbCountMu.Lock()
	if e, ok := dbCountCache[key]; ok && time.Since(e.at) < dbCountTTL {
		dbCountMu.Unlock()
		return e.n
	}
	dbCountMu.Unlock()

	n := 0
	if dbs, err := Databases(key); err == nil {
		for _, d := range dbs {
			if !systemDBs[d] {
				n++
			}
		}
	}
	dbCountMu.Lock()
	dbCountCache[key] = dbCountEntry{n: n, at: time.Now()}
	dbCountMu.Unlock()
	return n
}

// invalidateDBCount 建/删库后主动失效,概览与数据库页口径立即可见
func invalidateDBCount(key string) {
	dbCountMu.Lock()
	delete(dbCountCache, key)
	dbCountMu.Unlock()
}

// passwordFor 从应用实例参数里读取 root 密码(面板自己保存的,不返回给前端)
func passwordFor(key string) (string, error) {
	list, _ := appstore.List()
	for _, inst := range list {
		if inst.Key == key {
			if p := strings.TrimSpace(inst.Params["RootPassword"]); p != "" {
				return p, nil
			}
			if p := strings.TrimSpace(inst.Params["Password"]); p != "" {
				return p, nil
			}
			return "", fmt.Errorf("该实例未记录 root 密码,请卸载后重新安装")
		}
	}
	return "", fmt.Errorf("数据库 %s 未安装", key)
}

func backupsDir() string { return filepath.Join(config.DataDir(), "backups", "db") }

// clientArgs 组装容器内执行的命令
func clientArgs(key, password, database, sql string) (env []string, cmd []string, err error) {
	switch key {
	case "mysql":
		env = []string{"MYSQL_PWD=" + password}
		// -h127.0.0.1 强制走 TCP(root@%),避免依赖状态不一致的 root@localhost 账户
		cmd = []string{"mysql", "-uroot", "-h127.0.0.1", "--default-character-set=utf8mb4"}
	case "mariadb":
		env = []string{"MYSQL_PWD=" + password}
		cmd = []string{"mariadb", "-uroot", "-h127.0.0.1", "--default-character-set=utf8mb4"}
	case "postgresql":
		env = []string{"PGPASSWORD=" + password}
		cmd = []string{"psql", "-U", "postgres"}
		if strings.TrimSpace(database) != "" && database != "postgres" {
			cmd = append(cmd, "-d", database)
		}
		cmd = append(cmd, "-c", sql)
		return env, cmd, nil
	default:
		return nil, nil, fmt.Errorf("不支持的数据库类型")
	}
	if strings.TrimSpace(database) != "" {
		cmd = append(cmd, "--database", database)
	}
	cmd = append(cmd, "-e", sql)
	return env, cmd, nil
}

// Query 执行 SQL,返回文本输出
func Query(key, database, sqlText string) (string, error) {
	if !isSupported(key) {
		return "", fmt.Errorf("不支持的数据库")
	}
	if strings.TrimSpace(sqlText) == "" {
		return "", fmt.Errorf("SQL 不能为空")
	}
	if !dockerx.ContainerRunning("kun-" + key) {
		return "", fmt.Errorf("数据库容器未运行")
	}
	password, err := passwordFor(key)
	if err != nil {
		return "", err
	}
	env, cmd, err := clientArgs(key, password, database, sqlText)
	if err != nil {
		return "", err
	}
	full := append([]string{"docker", "exec"}, envArg(env)...)
	full = append(full, "kun-"+key)
	full = append(full, cmd...)
	out, err := util.RunWithTimeout(60*time.Second, full[0], full[1:]...)
	if err != nil {
		return out, fmt.Errorf("%v | %s", err, strings.TrimSpace(out))
	}
	if len(out) > 200*1024 {
		out = out[:200*1024] + "\n... (结果过长已截断)"
	}
	return out, nil
}

// TableResult 结构化查询结果(列名+行数据)
type TableResult struct {
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
}

// TableQuery 执行 SELECT 并返回结构化结果(表格展示用)
func TableQuery(key, database, sqlText string) (*TableResult, error) {
	if !isSupported(key) {
		return nil, fmt.Errorf("不支持的数据库")
	}
	password, err := passwordFor(key)
	if err != nil {
		return nil, err
	}
	env, cmd, err := clientArgs(key, password, database, sqlText)
	if err != nil {
		return nil, err
	}
	// 加 --batch 和 --raw 让输出为 TSV
	full := append([]string{"docker", "exec"}, envArg(env)...)
	full = append(full, "kun-"+key)
	full = append(full, cmd...)
	// mysql/mariadb: 在 -e 前插入 --batch --raw
	// docker exec 的环境变量参数也是 -e,必须从后往前找最后一个 -e(SQL 那个)
	if key == "mysql" || key == "mariadb" {
		for i := len(full) - 1; i >= 0; i-- {
			if full[i] == "-e" {
				full = append(full[:i], append([]string{"--batch", "--raw"}, full[i:]...)...)
				break
			}
		}
	}
	out, err := util.RunWithTimeout(60*time.Second, full[0], full[1:]...)
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(out))
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		return &TableResult{Columns: []string{}, Rows: [][]interface{}{}}, nil
	}
	cols := strings.Split(lines[0], "\t")
	var rows [][]interface{}
	for _, l := range lines[1:] {
		if l == "" {
			continue
		}
		vals := strings.Split(l, "\t")
		row := make([]interface{}, len(cols))
		for i, v := range vals {
			if v == "NULL" {
				row[i] = nil
			} else {
				row[i] = v
			}
		}
		// 补齐缺失列
		for len(row) < len(cols) {
			row = append(row, nil)
		}
		rows = append(rows, row)
	}
	return &TableResult{Columns: cols, Rows: rows}, nil
}

// Databases 列出数据库
func Databases(key string) ([]string, error) {
	var sql string
	switch key {
	case "mysql", "mariadb":
		sql = "SHOW DATABASES"
	case "postgresql":
		sql = "SELECT datname FROM pg_database WHERE NOT datistemplate ORDER BY datname"
	default:
		return nil, fmt.Errorf("不支持的数据库")
	}
	out, err := Query(key, "", sql)
	if err != nil {
		return nil, err
	}
	var dbs []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.EqualFold(l, "Database") || strings.HasPrefix(l, "-") || strings.HasPrefix(l, "(") {
			continue
		}
		dbs = append(dbs, l)
	}
	sort.Strings(dbs)
	return dbs, nil
}

var dbNameRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// 系统库黑名单:禁止删除
var systemDBs = map[string]bool{
	"mysql": true, "sys": true, "information_schema": true, "performance_schema": true, "postgres": true, "template0": true, "template1": true,
}

// CreateDatabase 创建数据库(mysql/mariadb 加 utf8mb4;postgres 走 psql)
func CreateDatabase(key, name string) error {
	if !isSupported(key) {
		return fmt.Errorf("不支持的数据库")
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if !dbNameRe.MatchString(name) {
		return fmt.Errorf("库名仅支持小写字母、数字和下划线(1-64 位)")
	}
	if systemDBs[name] {
		return fmt.Errorf("不能使用系统库名")
	}
	var sql string
	if key == "postgresql" {
		sql = `CREATE DATABASE ` + name
	} else {
		sql = "CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
	}
	_, err := Query(key, "", sql)
	if err == nil {
		invalidateDBCount(key)
	}
	return err
}

// DropDatabase 删除数据库(系统库保护)
func DropDatabase(key, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if !dbNameRe.MatchString(name) {
		return fmt.Errorf("库名仅支持小写字母、数字和下划线")
	}
	if systemDBs[name] {
		return fmt.Errorf("系统库禁止删除")
	}
	var sql string
	if key == "postgresql" {
		sql = `DROP DATABASE IF EXISTS ` + name
	} else {
		sql = "DROP DATABASE IF EXISTS `" + name + "`"
	}
	_, err := Query(key, "", sql)
	if err == nil {
		invalidateDBCount(key)
	}
	return err
}

// BackupInfo 备份文件
type BackupInfo struct {
	File string `json:"file"`
	Size int64  `json:"size"`
	Time string `json:"time"`
}

// Backup 一键备份数据库(gzip)到面板备份目录
func Backup(key, database string) (string, error) {
	if !isSupported(key) || strings.TrimSpace(database) == "" {
		return "", fmt.Errorf("参数错误")
	}
	if !dockerx.ContainerRunning("kun-" + key) {
		return "", fmt.Errorf("数据库容器未运行")
	}
	password, err := passwordFor(key)
	if err != nil {
		return "", err
	}
	_ = os.MkdirAll(backupsDir(), 0o755)
	file := filepath.Join(backupsDir(), fmt.Sprintf("%s_%s_%s.sql.gz", key, sanitize(database), time.Now().Format("20060102_150405")))

	var dumpCmd []string
	switch key {
	case "mysql":
		dumpCmd = []string{"mysqldump", "-uroot", "--default-character-set=utf8mb4", "--single-transaction", database}
	case "mariadb":
		dumpCmd = []string{"mariadb-dump", "-uroot", "--single-transaction", database}
	case "postgresql":
		dumpCmd = []string{"pg_dump", "-U", "postgres", database}
	}
	full := append([]string{"docker", "exec"}, envArg([]string{envKey(key) + "=" + password})...)
	full = append(full, "kun-"+key)
	full = append(full, dumpCmd...)

	dump, err := util.RunWithTimeout(30*time.Minute, full[0], full[1:]...)
	if err != nil {
		return "", fmt.Errorf("导出失败: %s", tailStr(dump))
	}
	// 写入 gzip
	gz, err := os.Create(file + ".tmp")
	if err != nil {
		return "", err
	}
	gw := gzip.NewWriter(gz)
	if _, err := gw.Write([]byte(dump)); err != nil {
		gz.Close()
		return "", err
	}
	gw.Close()
	gz.Close()
	if err := os.Rename(file+".tmp", file); err != nil {
		return "", err
	}
	return file, nil
}

// ListBackups 某数据库实例的备份列表
func ListBackups(key string) []BackupInfo {
	dir := filepath.Join(backupsDir())
	prefix := key + "_"
	var out []BackupInfo
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || !strings.HasSuffix(e.Name(), ".sql.gz") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{File: e.Name(), Size: fi.Size(), Time: fi.ModTime().Format("2006-01-02 15:04")})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].File > out[b].File })
	return out
}

// BackupPath 校验并返回备份文件完整路径(防穿越)
func BackupPath(key, file string) (string, error) {
	if !isSupported(key) || strings.Contains(file, "/") || strings.Contains(file, "\\") || strings.Contains(file, "..") {
		return "", fmt.Errorf("非法备份文件名")
	}
	if !strings.HasPrefix(file, key+"_") || !strings.HasSuffix(file, ".sql.gz") {
		return "", fmt.Errorf("非法备份文件名")
	}
	return filepath.Join(backupsDir(), file), nil
}

// DeleteBackup 删除备份
func DeleteBackup(key, file string) error {
	p, err := BackupPath(key, file)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("备份不存在")
	}
	return os.Remove(p)
}

func isSupported(key string) bool {
	for _, s := range supported {
		if s.Key == key {
			return true
		}
	}
	return false
}

func envKey(key string) string {
	if key == "postgresql" {
		return "PGPASSWORD"
	}
	return "MYSQL_PWD"
}

// envArg 把 KEY=VAL 列表展开为 docker exec -e 参数
func envArg(env []string) []string {
	var out []string
	for _, e := range env {
		out = append(out, "-e", e)
	}
	return out
}

func tailStr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[len(s)-300:]
	}
	return s
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// ValidIdentifier 校验库/表名(仅小写字母数字下划线)
func ValidIdentifier(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// ChangeRootPassword 修改 MySQL/MariaDB root 密码:
// 1. 用旧密码执行 ALTER USER(root@% 与 root@localhost)
// 2. 同步更新面板实例记录,否则后续管理操作会因旧密码失败
func ChangeRootPassword(key, newPassword string) error {
	if !isSupported(key) || key == "postgresql" {
		return fmt.Errorf("该数据库类型暂不支持修改 root 密码")
	}
	if len(newPassword) < 6 {
		return fmt.Errorf("新密码至少 6 位")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9!@#$%^&*()_+=-]{6,64}$`).MatchString(newPassword) {
		return fmt.Errorf("密码包含不支持的字符(仅允许字母数字与 !@#$%%^&*()_+=-)")
	}
	oldPass, err := passwordFor(key)
	if err != nil {
		return err
	}
	if !dockerx.ContainerRunning("kun-" + key) {
		return fmt.Errorf("数据库容器未运行")
	}
	// 先用记录中的密码校验连接有效
	env, cmd, err := clientArgs(key, oldPass, "", "SELECT 1")
	if err != nil {
		return err
	}
	full := append([]string{"docker", "exec"}, envArg(env)...)
	full = append(full, "kun-"+key)
	full = append(full, cmd...)
	if out, err := util.RunWithTimeout(15*time.Second, full[0], full[1:]...); err != nil {
		return fmt.Errorf("旧密码校验失败(当前密码可能已被修改): %s", strings.TrimSpace(out))
	}
	// 逐条执行:先重建 root@localhost(账户状态可能不一致甚至不存在),
	// 最后才 ALTER root@%。全程走 TCP root@%,重建 localhost 不会导致掉线;
	// ALTER 自身账户必须放最后,之后旧密码的新连接会被拒绝。
	execSQL := func(sqlText string) error {
		full2 := append([]string{"docker", "exec"}, envArg(env)...)
		full2 = append(full2, "kun-"+key)
		// cmd 形如 [mysql, -uroot, ..., -e, 旧SQL]:去掉旧的 -e 与 SQL 两项
		full2 = append(full2, cmd[:len(cmd)-2]...)
		full2 = append(full2, "-e", sqlText)
		out, e := util.RunWithTimeout(15*time.Second, full2[0], full2[1:]...)
		if e != nil {
			return fmt.Errorf("%v: %s", e, strings.TrimSpace(out))
		}
		return nil
	}
	for _, sqlText := range []string{
		"DROP USER IF EXISTS 'root'@'localhost'",
		"CREATE USER 'root'@'localhost' IDENTIFIED BY '" + newPassword + "'",
		"GRANT ALL PRIVILEGES ON *.* TO 'root'@'localhost' WITH GRANT OPTION",
		"FLUSH PRIVILEGES",
		"ALTER USER 'root'@'%' IDENTIFIED BY '" + newPassword + "'",
	} {
		if err := execSQL(sqlText); err != nil {
			return fmt.Errorf("修改失败(%s): %s", strings.SplitN(sqlText, " ", 3)[0], strings.TrimSpace(err.Error()))
		}
	}
	// 同步面板记录的密码,否则后续管理操作会因旧密码失败
	if !appstore.UpdateInstanceParam(key, "RootPassword", newPassword) {
		return fmt.Errorf("密码已修改,但面板记录同步失败")
	}
	return nil
}

// Import 从 SQL 文件(.sql 或 .sql.gz)导入到指定数据库
func Import(key, database, file string) (string, error) {
	if !isSupported(key) || strings.TrimSpace(database) == "" {
		return "", fmt.Errorf("参数错误")
	}
	if !dockerx.ContainerRunning("kun-" + key) {
		return "", fmt.Errorf("数据库容器未运行")
	}
	fi, err := os.Stat(file)
	if err != nil || fi.IsDir() {
		return "", fmt.Errorf("SQL 文件不存在")
	}
	if fi.Size() == 0 {
		return "", fmt.Errorf("SQL 文件为空")
	}
	password, err := passwordFor(key)
	if err != nil {
		return "", err
	}
	_ = password
	pipe := file
	if strings.HasSuffix(strings.ToLower(file), ".gz") {
		pipe = file + ".plain"
		if err := decompressGz(file, pipe); err != nil {
			return "", err
		}
		defer os.Remove(pipe)
	}
	var inner string
	switch key {
	case "mysql":
		inner = fmt.Sprintf(`mysql -uroot -p"$MYSQL_ROOT_PASSWORD" --default-character-set=utf8mb4 %s`, shellQuote(database))
	case "mariadb":
		inner = fmt.Sprintf(`mariadb -uroot -p"$MYSQL_ROOT_PASSWORD" %s`, shellQuote(database))
	case "postgresql":
		// psql 不带 -f 时从 stdin 读;宿主机路径在容器内不可见,必须走 docker exec -i stdin
		inner = fmt.Sprintf(`psql -U postgres -d %s -v ON_ERROR_STOP=0`, shellQuote(database))
	default:
		return "", fmt.Errorf("不支持的数据库类型")
	}
	// stdin 重定向必须在宿主机层完成(docker exec -i 从宿主机 stdin 读)
	full := fmt.Sprintf("docker exec -i kun-%s sh -c %s < %s", key, shellQuote(inner), shellQuote(pipe))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", full)
	cmd.Env = util.ChildEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return string(out), fmt.Errorf("导入失败: %s", strings.TrimSpace(tail))
	}
	return "导入完成", nil
}

func decompressGz(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("不是有效的 gzip 文件: %v", err)
	}
	defer gz.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, gz); err != nil {
		return err
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
