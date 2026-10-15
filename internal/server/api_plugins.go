package server

import (
	"sync"
	"time"
	"fmt"
	"io"
	"net/http"

	"kunpanel/internal/plugins"
	"kunpanel/internal/taskx"
)

func (s *Server) handlePluginList(w http.ResponseWriter, r *http.Request) {
	OK(w, plugins.List())
}

func (s *Server) handlePluginAction(w http.ResponseWriter, r *http.Request) {
	key, ok := safeID(r.PathValue("key"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法插件标识")
		return
	}
	action := r.PathValue("action")
	var req struct{}
	_ = readJSON(r, &req)

	var run func(w io.Writer) error
	switch {
	case key == "waf" && action == "install":
		run = plugins.InstallWaf
	case key == "waf" && action == "uninstall":
		run = plugins.UninstallWaf
	default:
		Fail(w, http.StatusBadRequest, "未知插件或操作")
		return
	}
	// 安装/卸载涉及容器重建与配置重写,放后台任务执行并返回任务 ID 供前端跟踪
	t, err := taskx.M.Start("plugin-"+key+"-"+action, run)
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, t)
}

var (
	wafInfoMu    sync.Mutex
	wafInfoCache map[string]any
	wafInfoAt    time.Time
)

func (s *Server) handleWafInfo(w http.ResponseWriter, r *http.Request) {
	// 3s 缓存:stats 读日志文件 + 状态检测含多次 exec,高频轮询下开销集中
	wafInfoMu.Lock()
	if wafInfoCache != nil && time.Since(wafInfoAt) < 3*time.Second {
		c := wafInfoCache
		wafInfoMu.Unlock()
		OK(w, c)
		return
	}
	wafInfoMu.Unlock()
	list := plugins.List()
	var waf *plugins.Info
	for i := range list {
		if list[i].Key == "waf" {
			waf = &list[i]
		}
	}
	if waf == nil || waf.Status == "not_installed" {
		OK(w, map[string]any{"installed": false})
		return
	}
	out := map[string]any{
		"installed": true,
		"active":    waf.Active,
		"settings":  plugins.GetWafSettings(),
		"stats":     plugins.WafStats(),
	}
	wafInfoMu.Lock()
	wafInfoCache = out
	wafInfoAt = time.Now()
	wafInfoMu.Unlock()
	OK(w, out)
}

func (s *Server) handleWafSettings(w http.ResponseWriter, r *http.Request) {
	var st plugins.WafSettings
	if err := readJSON(r, &st); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if !wafInstalledNow() {
		Fail(w, http.StatusBadRequest, "WAF 插件未安装")
		return
	}
	// 自定义规则校验:正则可编译 + ReDoS 启发式 + 数量/长度上限
	for i, cr := range st.CustomRules {
		if err := plugins.ValidateRulePublic(cr.Pattern); err != nil {
			Fail(w, http.StatusBadRequest, fmt.Sprintf("自定义规则 #%d 无效: %v", i+1, err))
			return
		}
		if len(cr.Label) > 60 {
			Fail(w, http.StatusBadRequest, fmt.Sprintf("自定义规则 #%d 标签过长", i+1))
			return
		}
	}
	if len(st.CustomRules) > 100 {
		Fail(w, http.StatusBadRequest, "自定义规则最多 100 条")
		return
	}
	if err := plugins.SaveWafSettings(st); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, map[string]any{"active": plugins.WafActive()})
}

func wafInstalledNow() bool {
	for _, p := range plugins.List() {
		if p.Key == "waf" && p.Status == "installed" {
			return true
		}
	}
	return false
}

func (s *Server) handleWafLogs(w http.ResponseWriter, r *http.Request) {
	n := atoiDefault(r.URL.Query().Get("n"), 200)
	list, err := plugins.WafLogs(n)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []plugins.WafLogEntry{}
	}
	OK(w, list)
}

func (s *Server) handleWafLogsClear(w http.ResponseWriter, r *http.Request) {
	if err := plugins.ClearWafLogs(); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleWafStats(w http.ResponseWriter, r *http.Request) {
	OK(w, plugins.WafStats())
}

// ---------- WAF Apache 防护(PHP 容器 ModSecurity) ----------

func (s *Server) handleWafApacheGet(w http.ResponseWriter, r *http.Request) {
	OK(w, plugins.ApacheWafStatus())
}

func (s *Server) handleWafApacheSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if !wafInstalledNow() {
		Fail(w, http.StatusBadRequest, "WAF 插件未安装")
		return
	}
	if err := plugins.ApacheWafToggle(req.Enabled); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, plugins.ApacheWafStatus())
}
