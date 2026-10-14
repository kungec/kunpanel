package cronx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/alertx"
	"kunpanel/internal/config"
	"kunpanel/internal/dbmgr"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/filemgr"
	"kunpanel/internal/notifyx"
	"kunpanel/internal/panelbackup"
	"kunpanel/internal/remotex"
	"kunpanel/internal/util"
)

// Job 计划任务
type Job struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"` // shell / backup / logrotate / dbbackup / trashclean / panelbackup / containerlog
	Spec      string `json:"spec"` // 标准 5 位 cron 表达式
	Command   string `json:"command"`
	BackupSrc string `json:"backup_src,omitempty"`
	BackupDir string `json:"backup_dir,omitempty"`
	DBKey     string `json:"db_key,omitempty"`  // dbbackup:数据库类型 mysql/mariadb/postgresql
	DBName    string `json:"db_name,omitempty"` // dbbackup:库名
	Remote    bool   `json:"remote,omitempty"`  // 备份完成后同时上传到 SFTP 远程
	Keep      int    `json:"keep,omitempty"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	LastRun   string `json:"last_run"`
	LastStat  string `json:"last_status"` // success / failed
	NextRun   string `json:"next_run"`
}

// ---------- 5 位 cron 表达式解析(min hour dom mon dow) ----------

type fieldSpec struct {
	min, max int
	names    map[string]int
}

var fieldSpecs = []fieldSpec{
	{0, 59, nil},
	{0, 23, nil},
	{1, 31, nil},
	{1, 12, map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}},
	{0, 6, map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}},
}

type parsedExpr struct {
	sets       [][]int
	restricted [5]bool // 字段是否为受限(非 *)
}

// ParseCron 校验表达式合法性
func ParseCron(expr string) error {
	_, err := parseExpr(expr)
	return err
}

func parseExpr(expr string) (*parsedExpr, error) {
	parts := strings.Fields(strings.TrimSpace(expr))
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron 表达式需要 5 个字段(分 时 日 月 周),当前 %d 个", len(parts))
	}
	out := &parsedExpr{sets: make([][]int, 5)}
	for i, p := range parts {
		out.restricted[i] = p != "*"
		set, err := parseField(p, fieldSpecs[i])
		if err != nil {
			return nil, fmt.Errorf("字段 %d (%s): %v", i+1, p, err)
		}
		out.sets[i] = set
	}
	return out, nil
}

func parseField(p string, f fieldSpec) ([]int, error) {
	var set []int
	for _, seg := range strings.Split(p, ",") {
		step := 1
		if i := strings.Index(seg, "/"); i >= 0 {
			s, err := strconv.Atoi(seg[i+1:])
			if err != nil || s <= 0 {
				return nil, fmt.Errorf("非法步长 %s", seg[i+1:])
			}
			step = s
			seg = seg[:i]
		}
		lo, hi := f.min, f.max
		switch {
		case seg == "" || seg == "*":
			// 全范围
		case strings.Contains(seg, "-"):
			bits := strings.SplitN(seg, "-", 2)
			var err error
			if lo, err = parseVal(bits[0], f); err != nil {
				return nil, err
			}
			if hi, err = parseVal(bits[1], f); err != nil {
				return nil, err
			}
		default:
			v, err := parseVal(seg, f)
			if err != nil {
				return nil, err
			}
			lo, hi = v, v
		}
		if lo < f.min || hi > f.max || lo > hi {
			return nil, fmt.Errorf("取值范围应在 %d-%d", f.min, f.max)
		}
		for v := lo; v <= hi; v += step {
			set = append(set, v)
		}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("字段为空")
	}
	return set, nil
}

func parseVal(s string, f fieldSpec) (int, error) {
	if f.names != nil {
		if v, ok := f.names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("非法取值 %q", s)
	}
	return v, nil
}

// Match 判断时间 t(精确到分钟)是否命中表达式
func Match(expr string, t time.Time) bool {
	pe, err := parseExpr(expr)
	if err != nil {
		return false
	}
	minHit := in(pe.sets[0], t.Minute())
	hourHit := in(pe.sets[1], t.Hour())
	domHit := in(pe.sets[2], t.Day())
	monHit := in(pe.sets[3], int(t.Month()))
	dowHit := in(pe.sets[4], int(t.Weekday()))
	if !minHit || !hourHit || !monHit {
		return false
	}
	// 标准 vixie cron 语义:日与周都受限时,命中任一即可
	if pe.restricted[2] && pe.restricted[4] {
		return domHit || dowHit
	}
	return domHit && dowHit
}

func in(set []int, v int) bool {
	for _, x := range set {
		if x == v {
			return true
		}
	}
	return false
}

// Next 计算下一次执行时间
func Next(expr string, from time.Time) (time.Time, error) {
	if _, err := parseExpr(expr); err != nil {
		return time.Time{}, err
	}
	t := from.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(1, 0, 0)
	for t.Before(limit) {
		if Match(expr, t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("一年内未找到匹配时间")
}

// ---------- 任务存储与调度 ----------

var (
	mu   sync.Mutex
	list []Job
)

func dataDir() string { return filepath.Join(config.DataDir(), "cron") }
func storeFile() string {
	return filepath.Join(dataDir(), "jobs.json")
}
func logFile(id string) string { return filepath.Join(dataDir(), id+".log") }

func load() {
	if b, err := os.ReadFile(storeFile()); err == nil {
		_ = json.Unmarshal(b, &list)
	}
}

func save() error {
	_ = os.MkdirAll(dataDir(), 0o755)
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(storeFile(), b, 0o600)
}

// List 全部任务
func List() []Job {
	mu.Lock()
	defer mu.Unlock()
	load()
	out := make([]Job, len(list))
	copy(out, list)
	return out
}

// Get 单个任务
func Get(id string) (Job, bool) {
	mu.Lock()
	defer mu.Unlock()
	load()
	for _, j := range list {
		if j.ID == id {
			return j, true
		}
	}
	return Job{}, false
}

// Add 新建任务
func validateJob(j *Job) error {
	switch j.Type {
	case "backup":
		if j.BackupSrc == "" || j.BackupDir == "" {
			return fmt.Errorf("备份任务需要填写源目录与备份目录")
		}
		if j.Keep <= 0 {
			j.Keep = 5
		}
	case "logrotate":
		if j.Keep <= 0 {
			j.Keep = 10
		}
	case "dbbackup":
		if j.DBKey != "mysql" && j.DBKey != "mariadb" && j.DBKey != "postgresql" {
			return fmt.Errorf("数据库备份需要选择 mysql / mariadb / postgresql")
		}
		if strings.TrimSpace(j.DBName) == "" {
			return fmt.Errorf("数据库备份需要填写库名")
		}
		if j.Keep <= 0 {
			j.Keep = 7
		}
	case "trashclean":
		if j.Keep <= 0 {
			j.Keep = 30
		}
	case "panelbackup":
		if j.Keep <= 0 {
			j.Keep = 10
		}
	case "containerlog":
		// 无额外参数
	default:
		j.Type = "shell"
		if strings.TrimSpace(j.Command) == "" {
			return fmt.Errorf("命令不能为空")
		}
	}
	return nil
}

func Add(j Job) (Job, error) {
	if strings.TrimSpace(j.Name) == "" {
		return j, fmt.Errorf("任务名称不能为空")
	}
	if err := ParseCron(j.Spec); err != nil {
		return j, err
	}
	if err := validateJob(&j); err != nil {
		return j, err
	}

	mu.Lock()
	defer mu.Unlock()
	load()
	j.ID = fmt.Sprintf("cron_%d", time.Now().UnixNano()/1000000)
	j.Enabled = true
	j.CreatedAt = time.Now().Format("2006-01-02 15:04:05")
	if next, err := Next(j.Spec, time.Now()); err == nil {
		j.NextRun = next.Format("2006-01-02 15:04")
	}
	list = append(list, j)
	return j, save()
}

// Update 修改任务
func Update(j Job) error {
	if err := ParseCron(j.Spec); err != nil {
		return err
	}
	if err := validateJob(&j); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	load()
	for i := range list {
		if list[i].ID == j.ID {
			lastRun, lastStat := list[i].LastRun, list[i].LastStat
			j.ID = list[i].ID
			j.CreatedAt = list[i].CreatedAt
			j.LastRun, j.LastStat = lastRun, lastStat
			if next, err := Next(j.Spec, time.Now()); err == nil {
				j.NextRun = next.Format("2006-01-02 15:04")
			}
			list[i] = j
			return save()
		}
	}
	return fmt.Errorf("任务不存在")
}

// Delete 删除任务
func Delete(id string) error {
	mu.Lock()
	defer mu.Unlock()
	load()
	var kept []Job
	found := false
	for _, j := range list {
		if j.ID == id {
			found = true
			continue
		}
		kept = append(kept, j)
	}
	if !found {
		return fmt.Errorf("任务不存在")
	}
	list = kept
	_ = os.Remove(logFile(id))
	return save()
}

// Toggle 启用/禁用
func Toggle(id string, enabled bool) error {
	mu.Lock()
	defer mu.Unlock()
	load()
	for i := range list {
		if list[i].ID == id {
			list[i].Enabled = enabled
			return save()
		}
	}
	return fmt.Errorf("任务不存在")
}

// ClearLog 清空指定任务的执行日志
func ClearLog(id string) error {
	mu.Lock()
	defer mu.Unlock()
	load()
	found := false
	for _, j := range list {
		if j.ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("任务不存在")
	}
	_ = os.MkdirAll(dataDir(), 0o755)
	return util.AtomicWrite(logFile(id), []byte{}, 0o644)
}

// ReadLog 读取任务最近日志
func ReadLog(id string) string {
	b, err := os.ReadFile(logFile(id))
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > 128*1024 {
		s = s[len(s)-128*1024:]
	}
	return s
}

// RunNow 手动触发
func RunNow(id string) error {
	j, ok := Get(id)
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	go func() { _ = RunJob(j) }()
	return nil
}

// RunJob 同步执行一次任务并记录结果
func RunJob(j Job) error {
	out, err := execute(j)
	appendLog(j.ID, out, err)
	if err != nil {
		text := "【告警】计划任务失败: " + j.Name + "  " + strings.TrimSpace(out)
		notifyx.Add("cron", "计划任务失败: "+j.Name, strings.TrimSpace(out))
		alertx.Broadcast(alertx.Get().Webhook, text, map[string]any{"type": "cron_fail", "job": j.Name})
	}
	mu.Lock()
	defer mu.Unlock()
	load()
	for i := range list {
		if list[i].ID == j.ID {
			list[i].LastRun = time.Now().Format("2006-01-02 15:04:05")
			list[i].LastStat = map[bool]string{true: "success", false: "failed"}[err == nil]
			_ = save()
		}
	}
	return err
}

func execute(j Job) (string, error) {
	if j.Type == "backup" {
		return doBackup(j)
	}
	if j.Type == "logrotate" {
		return doLogrotate(j)
	}
	if j.Type == "dbbackup" {
		return doDBBackup(j)
	}
	if j.Type == "panelbackup" {
		return doPanelBackup(j)
	}
	if j.Type == "containerlog" {
		return doContainerLogClean()
	}
	if j.Type == "trashclean" {
		n, err := filemgr.TrashCleanOld(time.Now().AddDate(0, 0, -j.Keep))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("回收站清理完成: 删除 %d 个超过 %d 天的条目", n, j.Keep), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", j.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", j.Command)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// doLogrotate 网站日志切割:轮转网关容器内 /var/log/nginx 下的日志并通知重开,
// 旧日志压缩归档到宿主机 websites/logs/archive,保留最近 Keep 份
func doDBBackup(j Job) (string, error) {
	file, err := dbmgr.Backup(j.DBKey, j.DBName)
	if err != nil {
		return "", fmt.Errorf("数据库备份失败: %v", err)
	}
	// 保留最近 Keep 份(按同名前缀)
	bd := filepath.Join(config.DataDir(), "backups", "db")
	prefix := j.DBKey + "_" + j.DBName + "_"
	if entries, e := os.ReadDir(bd); e == nil {
		var mine []string
		for _, e2 := range entries {
			n := e2.Name()
			if !e2.IsDir() && strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".sql.gz") {
				mine = append(mine, n)
			}
		}
		sort.Strings(mine)
		for len(mine) > j.Keep {
			_ = os.Remove(filepath.Join(bd, mine[0]))
			mine = mine[1:]
		}
	}
	if j.Remote {
		cfg := remotex.LoadPlain()
		if cfg.Host != "" {
			remote, uerr := remotex.Upload(cfg, file)
			if uerr != nil {
				return fmt.Sprintf("本地备份完成: %s;远程上传失败: %v", file, uerr), fmt.Errorf("远程上传失败: %v", uerr)
			}
			_ = os.Remove(file)
			return fmt.Sprintf("数据库备份并上传远程完成: %s", remote), nil
		}
	}
	return fmt.Sprintf("数据库备份完成: %s", file), nil
}

func doPanelBackup(j Job) (string, error) {
	tmp, err := panelbackup.Build()
	if err != nil {
		return "", fmt.Errorf("打包失败: %v", err)
	}
	defer os.Remove(tmp)
	dir := filepath.Join(config.DataDir(), "backups", "panel")
	_ = os.MkdirAll(dir, 0o755)
	dst := filepath.Join(dir, filepath.Base(tmp))
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	// 保留最近 Keep 份
	if entries, e := os.ReadDir(dir); e == nil {
		var mine []string
		for _, e2 := range entries {
			n := e2.Name()
			if !e2.IsDir() && strings.HasPrefix(n, "kunpanel_backup_") && strings.HasSuffix(n, ".tar.gz") {
				mine = append(mine, n)
			}
		}
		sort.Strings(mine)
		for len(mine) > j.Keep {
			_ = os.Remove(filepath.Join(dir, mine[0]))
			mine = mine[1:]
		}
	}
	if j.Remote {
		cfg := remotex.LoadPlain()
		if cfg.Host != "" {
			remote, uerr := remotex.Upload(cfg, dst)
			if uerr != nil {
				return fmt.Sprintf("本地备份完成: %s;远程上传失败: %v", dst, uerr), fmt.Errorf("远程上传失败: %v", uerr)
			}
			_ = os.Remove(dst)
			return fmt.Sprintf("面板备份并上传远程完成: %s", remote), nil
		}
	}
	return fmt.Sprintf("面板备份完成: %s", dst), nil
}

func doContainerLogClean() (string, error) {
	// 列出所有运行中容器的 json-file 日志路径并清空
	out, err := util.Run("sh", "-c",
		`for id in $(docker ps -q); do lp=$(docker inspect --format '{{.LogPath}}' $id 2>/dev/null); [ -n "$lp" ] && [ -f "$lp" ] && truncate -s 0 "$lp" && echo "cleaned: $id"; done`)
	if err != nil {
		return out, err
	}
	n := strings.Count(out, "cleaned:")
	// 顺带清空网关容器的站点错误日志(/var/log/nginx/error.log)
	gwExtra := ""
	for _, c := range []string{"kun-nginx", "kun-openresty"} {
		if o, e := util.Run("docker", "ps", "--filter", "name=^/"+c+"$", "--format", "{{.ID}}"); e == nil && strings.TrimSpace(o) != "" {
			if _, e2 := dockerx.ExecIn(c, "sh", "-c", ": > /var/log/nginx/error.log 2>/dev/null"); e2 == nil {
				gwExtra = "; 已清空 " + c + " 错误日志"
			}
			break
		}
	}
	return fmt.Sprintf("已清理 %d 个容器的日志文件%s", n, gwExtra), nil
}

func doLogrotate(j Job) (string, error) {
	gw := ""
	for _, c := range []string{"kun-nginx", "kun-openresty"} {
		if out, err := util.Run("docker", "ps", "--filter", "name=^/"+c+"$", "--format", "{{.ID}}"); err == nil && strings.TrimSpace(out) != "" {
			gw = c
			break
		}
	}
	if gw == "" {
		return "", fmt.Errorf("未检测到运行中的 Nginx/OpenResty 容器")
	}
	stamp := time.Now().Format("20060102_150405")
	script := "cd /var/log/nginx && for f in *.log; do [ -f \"$f\" ] && [ ! -L \"$f\" ] && [ -s \"$f\" ] && mv \"$f\" \"$f.$STAMP\"; done; kill -USR1 1; echo rotated"
	script = strings.ReplaceAll(script, "$STAMP", stamp)
	out, err := util.Run("docker", "exec", gw, "sh", "-c", script)
	if err != nil {
		return out, fmt.Errorf("轮转失败: %v", err)
	}
	// 把切出来的日志搬到宿主机归档目录压缩保存
	archiveDir := filepath.Join(config.DataDir(), "websites", "logs", "archive")
	_ = os.MkdirAll(archiveDir, 0o755)
	list, _ := dockerx.ExecIn(gw, "sh", "-c", "cd /var/log/nginx && ls *."+stamp+" 2>/dev/null")
	var moved []string
	for _, f := range strings.Fields(list) {
		tmp := filepath.Join(archiveDir, gw+"_"+f)
		if b, _ := dockerx.ExecIn(gw, "base64", "-w0", "/var/log/nginx/"+f); b != "" {
			if dec, de := base64.StdEncoding.DecodeString(strings.TrimSpace(b)); de == nil {
				_ = util.AtomicWrite(tmp, dec, 0o644)
				moved = append(moved, f)
			}
			_, _ = dockerx.ExecIn(gw, "rm", "-f", "/var/log/nginx/"+f)
		}
	}
	if len(moved) == 0 {
		return "本轮无需轮转(站点日志均为空或仅输出到 docker 控制台)", nil
	}
	gz := filepath.Join(archiveDir, "nginx_"+stamp+".tar.gz")
	staging := filepath.Join(archiveDir, "staging_"+stamp)
	_ = os.MkdirAll(staging, 0o755)
	for _, f := range moved {
		_ = os.Rename(filepath.Join(archiveDir, gw+"_"+f), filepath.Join(staging, f))
	}
	_ = filemgr.Compress(gz, []string{staging})
	_ = os.RemoveAll(staging)
	// 归档保留 Keep 份
	if entries, e := os.ReadDir(archiveDir); e == nil {
		var mine []string
		for _, e2 := range entries {
			if !e2.IsDir() && strings.HasPrefix(e2.Name(), "nginx_") && strings.HasSuffix(e2.Name(), ".tar.gz") {
				mine = append(mine, e2.Name())
			}
		}
		sort.Strings(mine)
		for len(mine) > j.Keep {
			_ = os.Remove(filepath.Join(archiveDir, mine[0]))
			mine = mine[1:]
		}
	}
	return fmt.Sprintf("已轮转 %d 个日志文件,归档: %s", len(moved), gz), nil
}

func doBackup(j Job) (string, error) {
	if _, err := os.Stat(j.BackupSrc); err != nil {
		return "", fmt.Errorf("备份源不存在: %v", err)
	}
	if err := os.MkdirAll(j.BackupDir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s_%s.tar.gz", filepath.Base(j.BackupSrc), time.Now().Format("20060102_150405"))
	dst := filepath.Join(j.BackupDir, name)
	if err := filemgr.Compress(dst, []string{j.BackupSrc}); err != nil {
		return "", fmt.Errorf("打包失败: %v", err)
	}
	// 清理旧备份,保留最近 Keep 份
	if entries, err := os.ReadDir(j.BackupDir); err == nil {
		var mine []os.DirEntry
		prefix := filepath.Base(j.BackupSrc) + "_"
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".tar.gz") {
				mine = append(mine, e)
			}
		}
		if len(mine) > j.Keep {
			sort.Slice(mine, func(a, b int) bool { return mine[a].Name() < mine[b].Name() })
			for _, e := range mine[:len(mine)-j.Keep] {
				_ = os.Remove(filepath.Join(j.BackupDir, e.Name()))
			}
		}
	}
	if j.Remote {
		cfg := remotex.LoadPlain()
		if cfg.Host != "" {
			remote, uerr := remotex.Upload(cfg, dst)
			if uerr != nil {
				return fmt.Sprintf("本地备份完成: %s;远程上传失败: %v", dst, uerr), fmt.Errorf("远程上传失败: %v", uerr)
			}
			_ = os.Remove(dst) // 已上云,本地不留副本
			return fmt.Sprintf("备份并上传远程完成: %s", remote), nil
		}
		return fmt.Sprintf("本地备份完成: %s(未配置远程备份目标,未上传)", dst), nil
	}
	return fmt.Sprintf("备份完成: %s", dst), nil
}

func appendLog(id, out string, err error) {
	_ = os.MkdirAll(dataDir(), 0o755)
	ts := time.Now().Format("[2006-01-02 15:04:05]")
	status := "✅ 成功"
	if err != nil {
		status = "❌ 失败: " + err.Error()
	}
	line := fmt.Sprintf("%s %s\n%s\n\n", ts, status, strings.TrimSpace(out))
	b, _ := os.ReadFile(logFile(id))
	b = append(b, []byte(line)...)
	if len(b) > 2<<20 {
		b = b[len(b)-512*1024:]
	}
	_ = util.AtomicWrite(logFile(id), b, 0o644)
}

// StartScheduler 启动调度循环(每 30s 检查一次到期任务)
func StartScheduler(stop <-chan struct{}) {
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-tick.C:
				checkDue(now)
			}
		}
	}()
}

// runningJobs 运行中任务集合(防长任务超时间隔后叠加并发实例)
var runningMu sync.Mutex
var runningJobs = map[string]bool{}

func checkDue(now time.Time) {
	prev := now.Add(-30 * time.Second).Truncate(time.Minute)
	for _, j := range List() {
		if !j.Enabled {
			continue
		}
		if Match(j.Spec, prev) {
			job := j
			runningMu.Lock()
			if runningJobs[job.Name] {
				runningMu.Unlock()
				continue
			}
			runningJobs[job.Name] = true
			runningMu.Unlock()
			go func() {
				defer func() {
					runningMu.Lock()
					delete(runningJobs, job.Name)
					runningMu.Unlock()
				}()
				_ = RunJob(job)
			}()
		}
	}
}
