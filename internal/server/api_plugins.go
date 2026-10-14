package server

import (
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

func (s *Server) handleWafInfo(w http.ResponseWriter, r *http.Request) {
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
	OK(w, map[string]any{
		"installed": true,
		"active":    waf.Active,
		"settings":  plugins.GetWafSettings(),
		"stats":     plugins.WafStats(),
	})
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
