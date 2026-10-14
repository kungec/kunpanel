package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"path/filepath"

	"kunpanel/internal/config"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/logsx"
	"kunpanel/internal/panelupdate"
	"kunpanel/internal/system"
	"kunpanel/internal/taskx"
)

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil && v > 0 {
		return v
	}
	return def
}

// handleSystemQuick 轻量系统快照(顶栏监控芯片专用:不触发 docker 子进程)
func (s *Server) handleSystemQuick(w http.ResponseWriter, r *http.Request) {
	snap := system.Get()
	cfg := config.M.Get()
	OK(w, map[string]any{
		"system":       snap,
		"panel_name":   cfg.PanelName,
	})
}

// handleDashboard 概览数据
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	snap := system.Get()
	docker := dockerx.Info()
	cfg := config.M.Get()
	OK(w, map[string]any{
		"system": snap,
		"docker": docker,
		"panel": map[string]any{
			"version":   Version,
			"started":   startedAt.Format("2006-01-02 15:04:05"),
			"port":      cfg.Port,
			"entrance":  cfg.Entrance != "",
			"panelName": cfg.PanelName,
		},
	})
}

var startedAt = time.Now()

// ---------- 设置 ----------

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg := config.M.Get()
	resp := map[string]any{
		"panel_name":   cfg.PanelName,
		"port":         cfg.Port,
		"username":     cfg.Username,
		"entrance":     cfg.Entrance,
		"language":     cfg.Language,
		"ssl":          cfg.SSL,
		"ssl_has_cert": func() bool { _, e := os.Stat(filepath.Join(config.DataDir(), "panelssl", "cert.pem")); return e == nil }(),
		"totp_enabled": cfg.TotpEnabled,
		"version":      Version,
		"created_at":   cfg.CreatedAt,
		"data_dir":     config.DataDir(),
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
	}
	for k, v := range avatarState() {
		resp[k] = v
	}
	OK(w, resp)
}

type settingsReq struct {
	PanelName string `json:"panel_name"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	Entrance  string `json:"entrance"`
	Language  string `json:"language"`
}

// 合法入口:4-32 位小写字母/数字/中划线;"-" 或空 表示关闭
var entrancePattern = regexp.MustCompile(`^[a-z0-9-]{4,32}$`)

func normalizeEntranceInput(v string) (string, error) {
	v = strings.ToLower(strings.Trim(strings.TrimSpace(v), "/"))
	if v == "" || v == "off" || v == "-" {
		return "-", nil
	}
	if !entrancePattern.MatchString(v) {
		return "", fmt.Errorf("入口仅支持 4-32 位小写字母、数字和中划线")
	}
	return v, nil
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	entrance, err := normalizeEntranceInput(req.Entrance)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	restartNeeded := false
	err = config.M.Update(func(c *config.Config) {
		if req.PanelName != "" {
			c.PanelName = req.PanelName
		}
		if req.Port > 0 && req.Port < 65536 && req.Port != c.Port {
			c.Port = req.Port
			restartNeeded = true
		}
		if req.Username != "" {
			c.Username = req.Username
		}
		c.Entrance = entrance
		if req.Language != "" {
			c.Language = req.Language
		}
	})
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	msg := "保存成功"
	if entrance != "-" {
		msg += ",请通过 http://<IP>:" + strconv.Itoa(config.M.Get().Port) + "/" + entrance + " 访问面板"
	}
	if restartNeeded {
		msg += ";端口修改将在重启面板后生效"
	}
	OK(w, map[string]any{"restart": restartNeeded, "message": msg, "entrance": entrance})
}

type passwdReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req passwdReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if !config.M.CheckPassword(req.OldPassword) {
		Fail(w, http.StatusBadRequest, "原密码错误")
		return
	}
	if len(req.NewPassword) < 6 {
		Fail(w, http.StatusBadRequest, "新密码至少 6 位")
		return
	}
	if err := config.M.SetPassword(req.NewPassword); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 密码已改,删除初始密码文件
	_ = os.Remove(config.InitialPwdFile())
	logsx.AppendLoginResult(ClientIP(r), true, "修改密码")
	OK(w, nil)
}

// handlePanelRestart 重启面板服务(systemd / openrc / 手动)
func (s *Server) handlePanelRestart(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("systemctl"); err == nil {
			cmd := exec.Command("systemctl", "restart", "kunpanel")
			if err := cmd.Start(); err == nil {
				OK(w, "面板服务重启中,页面将在数秒后恢复")
				return
			}
		}
		if _, err := exec.LookPath("rc-service"); err == nil {
			cmd := exec.Command("rc-service", "kunpanel", "restart")
			if err := cmd.Start(); err == nil {
				OK(w, "面板服务重启中,页面将在数秒后恢复")
				return
			}
		}
	}
	if inDocker() {
		OK(w, "Docker 部署请执行: docker restart kunpanel")
		return
	}
	// 通用回退:异步重启自身
	if exe, err := os.Executable(); err == nil {
		cmd := exec.Command("sh", "-c", fmt.Sprintf("nohup %s >/dev/null 2>&1 & sleep 1; kill %d", exe, os.Getpid()))
		if runtime.GOOS == "windows" {
			_ = cmd
			Fail(w, http.StatusBadRequest, "Windows 下请在服务管理器中重启 kunpanel 服务")
			return
		}
		if err := cmd.Start(); err == nil {
			OK(w, "面板进程重启中")
			return
		}
	}
	Fail(w, http.StatusInternalServerError, "无法自动重启,请手动重启面板服务")
}

func inDocker() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	return err == nil && strings.Contains(string(b), "docker") || exists("/.dockerenv")
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ---------- 后台任务查询 ----------

func (s *Server) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法任务 ID")
		return
	}
	t, ok := taskx.M.Get(id)
	if !ok {
		Fail(w, http.StatusNotFound, "任务不存在")
		return
	}
	OK(w, t)
}

// handleTaskList 后台任务列表(运行中的在前,其余按启动时间倒序;列表只带日志尾部)
func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	list := taskx.M.List()
	type row struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Status    string    `json:"status"`
		Error     string    `json:"error,omitempty"`
		StartedAt time.Time `json:"started_at"`
		EndedAt   time.Time `json:"ended_at,omitempty"`
		LogTail   string    `json:"log_tail"`
	}
	out := make([]row, 0, len(list))
	for _, t := range list {
		tail := t.Log
		if len(tail) > 2048 {
			tail = tail[len(tail)-2048:]
		}
		out = append(out, row{ID: t.ID, Name: t.Name, Status: t.Status, Error: t.Error, StartedAt: t.StartedAt, EndedAt: t.EndedAt, LogTail: tail})
	}
	sort.Slice(out, func(a, b int) bool {
		if (out[a].Status == "running") != (out[b].Status == "running") {
			return out[a].Status == "running"
		}
		return out[a].StartedAt.After(out[b].StartedAt)
	})
	OK(w, out)
}

// ---------- 日志查询 ----------

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "login" && kind != "operation" {
		Fail(w, http.StatusBadRequest, "未知日志类型")
		return
	}
	n := atoiDefault(r.URL.Query().Get("n"), 200)
	list, err := logsx.Read(kind, n)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, list)
}

// handleLogsClear 清空登录/操作日志
func (s *Server) handleLogsClear(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if err := logsx.Clear(kind); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handlePanelUpdateCheck 检查 GitHub 最新版本(force=1 跳过缓存强制拉取)
func (s *Server) handlePanelUpdateCheck(w http.ResponseWriter, r *http.Request) {
	var info *panelupdate.Info
	var err error
	if r.URL.Query().Get("force") == "1" {
		info, err = panelupdate.Check()
	} else {
		info = panelupdate.Cached()
		if info == nil {
			info, err = panelupdate.Check()
		}
	}
	if err != nil {
		// 有缓存就带错误提示返回缓存,否则报错
		if info = panelupdate.Cached(); info != nil {
			OK(w, info)
			return
		}
		Fail(w, http.StatusBadGateway, err.Error())
		return
	}
	OK(w, info)
}

// handlePanelUpdateApply 一键在线更新:下载对应平台安装包 → 替换二进制 → 自动重启
func (s *Server) handlePanelUpdateApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	req.Tag = strings.TrimSpace(req.Tag)
	if _, ok := panelupdate.Semver(req.Tag); !ok {
		Fail(w, http.StatusBadRequest, "非法版本号")
		return
	}
	if latest := panelupdate.Cached(); latest != nil && req.Tag != latest.Latest {
		Fail(w, http.StatusBadRequest, "目标版本与检查结果不一致,请重新检查更新")
		return
	}
	t, err := taskx.M.Start("panel-update", func(w io.Writer) error {
		return panelupdate.Apply(req.Tag, w)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, t)
}

// handleLogsExport 导出登录/操作日志为 CSV
func (s *Server) handleLogsExport(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "login" && kind != "operation" {
		Fail(w, http.StatusBadRequest, "未知日志类型")
		return
	}
	entries, err := logsx.Read(kind, 1000)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+kind+"_log_"+time.Now().Format("20060102")+".csv")
	// UTF-8 BOM 让 Excel 正确识别中文
	w.Write([]byte{0xEF, 0xBB, 0xBF})
	w.Write([]byte("时间,行为,IP,详情,结果\n"))
	for _, e := range entries {
		row := []string{e.Time, e.Action, e.IP, e.Detail, map[bool]string{true: "成功", false: "失败"}[e.OK]}
		for i, col := range row {
			if i > 0 {
				w.Write([]byte(","))
			}
			w.Write([]byte(`"` + csvSafe(strings.ReplaceAll(col, `"`, `""`)) + `"`))
		}
		w.Write([]byte("\n"))
	}
}

// csvSafe 防 CSV 公式注入:Excel 会把 =+-@ 开头的单元格当公式执行
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
