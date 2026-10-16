package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

// handleNginxLogs 查看站点访问/错误日志(容器或宿主机)
func (s *Server) handleNginxLogs(w http.ResponseWriter, r *http.Request) {
	logType := r.URL.Query().Get("type")
	if logType != "access" && logType != "error" {
		Fail(w, http.StatusBadRequest, "type 仅支持 access / error")
		return
	}
	n := atoiDefault(r.URL.Query().Get("tail"), 200)
	if n > 2000 {
		n = 2000
	}
	mode := websites.DetectNginx()
	switch mode.Mode {
	case "container":
		out, err := runContainerTail(mode.Target, containerLogPath(mode.Target, logType), n)
		if err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		OK(w, map[string]any{"source": mode.Target, "content": strings.TrimRight(out, "\n")})
	case "host":
		b, err := os.ReadFile("/var/log/nginx/" + logType + ".log")
		if err != nil {
			Fail(w, http.StatusInternalServerError, "读取日志失败: "+err.Error())
			return
		}
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		OK(w, map[string]any{"source": "宿主机 nginx", "content": strings.Join(lines, "\n")})
	default:
		Fail(w, http.StatusBadRequest, "未检测到 Nginx / OpenResty")
	}
}

func containerLogPath(container, logType string) string {
	if container == "kun-openresty" {
		return "/usr/local/openresty/nginx/logs/" + logType + ".log"
	}
	return "/var/log/nginx/" + logType + ".log"
}

func runContainerTail(container, file string, n int) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("容器日志仅在 Linux 上可用")
	}
	out, err := exec.Command("docker", "exec", container, "tail", "-n", strconv.Itoa(n), file).CombinedOutput()
	if err != nil && strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("读取失败: %v", err)
	}
	return string(out), nil
}

// ---------- 日志清理(一键清空 + 定时自动清理) ----------

// siteLogsDir 宿主机站点日志目录(nginx 容器挂载同源,truncate 对容器内同样生效)
func siteLogsDir() string {
	return filepath.Join(config.DataDir(), "websites", "logs")
}

// truncateSiteLogs 截断目录下全部 *.log(保留文件句柄有效,nginx 无需 reload)。
// 返回处理的文件数。
func truncateSiteLogs() int {
	count := 0
	entries, err := os.ReadDir(siteLogsDir())
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if err := os.Truncate(filepath.Join(siteLogsDir(), e.Name()), 0); err == nil {
			count++
		}
	}
	return count
}

// handleNginxLogsClear 一键清空站点日志(按 type 清对应全局日志 + 全部站点日志文件)
func (s *Server) handleNginxLogsClear(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type string `json:"type"`
	}
	_ = readJSON(r, &req)
	if req.Type != "access" && req.Type != "error" {
		Fail(w, http.StatusBadRequest, "type 仅支持 access / error")
		return
	}
	mode := websites.DetectNginx()
	if mode.Mode == "container" {
		// 容器内全局日志文件(与宿主机挂载同源时幂等)
		path := containerLogPath(mode.Target, req.Type)
		_, _ = exec.Command("docker", "exec", mode.Target, "sh", "-c", ": > "+path).CombinedOutput()
	} else if mode.Mode == "host" {
		_ = os.Truncate("/var/log/nginx/"+req.Type+".log", 0)
	}
	n := truncateSiteLogs()
	OK(w, map[string]any{"cleared": n})
}

// logCleanCfg 自动清理配置
type logCleanCfg struct {
	Enabled  bool   `json:"enabled"`
	KeepDays int    `json:"keep_days"`
	LastRun  string `json:"last_run,omitempty"`
}

func logCleanPath() string {
	return filepath.Join(config.DataDir(), "websites", "log_clean.json")
}

func getLogCleanCfg() logCleanCfg {
	cfg := logCleanCfg{Enabled: false, KeepDays: 7}
	if b, err := os.ReadFile(logCleanPath()); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg.KeepDays < 1 || cfg.KeepDays > 365 {
		cfg.KeepDays = 7
	}
	return cfg
}

func saveLogCleanCfg(c logCleanCfg) error {
	if c.KeepDays < 1 || c.KeepDays > 365 {
		c.KeepDays = 7
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(logCleanPath(), out, 0o600)
}

// RunSiteLogClean 按保留天数截断过期站点日志(mtime 超期即清),返回清理文件数
func RunSiteLogClean() (int, error) {
	cfg := getLogCleanCfg()
	if !cfg.Enabled {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -cfg.KeepDays)
	count := 0
	entries, err := os.ReadDir(siteLogsDir())
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Truncate(filepath.Join(siteLogsDir(), e.Name()), 0); err == nil {
			count++
		}
	}
	cfg.LastRun = time.Now().Format("2006-01-02 15:04:05")
	_ = saveLogCleanCfg(cfg)
	return count, nil
}

// handleNginxLogsAutoClean 自动清理配置读取/保存(GET 无 body 读,POST 写并立即执行一次)
func (s *Server) handleNginxLogsAutoClean(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		OK(w, getLogCleanCfg())
		return
	}
	var req struct {
		Enabled  bool `json:"enabled"`
		KeepDays int  `json:"keep_days"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if req.KeepDays < 1 || req.KeepDays > 365 {
		Fail(w, http.StatusBadRequest, "保留天数应在 1-365 之间")
		return
	}
	cfg := getLogCleanCfg()
	cfg.Enabled = req.Enabled
	cfg.KeepDays = req.KeepDays
	if err := saveLogCleanCfg(cfg); err != nil {
		Fail(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	cleaned := 0
	if cfg.Enabled {
		cleaned, _ = RunSiteLogClean()
	}
	OK(w, map[string]any{"cleaned": cleaned})
}
