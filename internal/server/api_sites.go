package server

import (
	"net/http"
	"strconv"

	"kunpanel/internal/appstore"
	"kunpanel/internal/dbmgr"
	"kunpanel/internal/websites"
)

func (s *Server) handleSiteList(w http.ResponseWriter, r *http.Request) {
	list, err := websites.List()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, list)
}

func (s *Server) handleNginxStatus(w http.ResponseWriter, r *http.Request) {
	OK(w, websites.DetectNginx())
}

// handleSiteUpdate 编辑站点(代理目标 / 端口 / 备注)
func (s *Server) handleSiteUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	var req struct {
		ProxyTo string `json:"proxy_to"`
		Remark  string `json:"remark"`
		PhpKey  string `json:"php_key"` // PHP 站点:切换 PHP 实例(多版本)
		Port    int    `json:"port"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.UpdateSite(id, req.ProxyTo, req.Remark, req.Port); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.PhpKey != "" {
		if err := websites.UpdateSitePhp(id, req.PhpKey); err != nil {
			Fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	site, err := websites.GetSite(id)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, site)
}

type siteAddReq struct {
	Domain     string `json:"domain"`
	Type       string `json:"type"`
	ProxyTo    string `json:"proxy_to"`
	Port       int    `json:"port"`
	Remark     string `json:"remark"`
	RootDir    string `json:"root_dir"`
	DbInstance string `json:"db_instance"`
	DbName     string `json:"db_name"`
	PhpKey     string `json:"php_key"` // PHP 站点:选择 PHP 实例(多版本)
}

func (s *Server) handleSiteAdd(w http.ResponseWriter, r *http.Request) {
	var req siteAddReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	site, err := websites.Create(websites.CreateOpts{
		Domain: req.Domain, SiteType: req.Type, ProxyTo: req.ProxyTo,
		RootDir: req.RootDir, Remark: req.Remark, Port: req.Port, PhpKey: req.PhpKey,
	})
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// PHP 站点需确保 PHP 运行时挂载了站点根目录
	if req.Type == "php" {
		_ = appstore.EnsurePhpMounts(nil)
	}
	// 可选:同步创建数据库
	dbErr := ""
	if req.DbInstance != "" && req.DbName != "" {
		if err := dbmgr.CreateDatabase(req.DbInstance, req.DbName); err != nil {
			dbErr = err.Error()
		}
	}
	OK(w, map[string]any{"site": site, "db_error": dbErr})
}

func (s *Server) handleSiteToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.Toggle(id, req.Enabled); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleSiteDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	if err := websites.Delete(id); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

var _ = strconv.Itoa

// handleSiteDomains 站点域名管理(查看/修改)
func (s *Server) handleSiteDomains(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	if r.Method == http.MethodGet {
		site, err := websites.GetSite(id)
		if err != nil {
			Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		OK(w, site)
		return
	}
	var req struct {
		Domain string `json:"domain"`
	}
	if err := readJSON(r, &req); err != nil || req.Domain == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.UpdateDomains(id, req.Domain); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handleSiteConfGet 读取站点 nginx 配置原文
func (s *Server) handleSiteConfGet(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	content, err := websites.GetSiteConf(id)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, map[string]string{"content": content})
}

// handleSiteConfSave 保存站点 nginx 配置(校验失败自动还原)
func (s *Server) handleSiteConfSave(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.SaveSiteConf(id, req.Content); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handleSiteHotlink 站点防盗链开关
func (s *Server) handleSiteHotlink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool   `json:"enabled"`
		Allow   string `json:"allow"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.SetSiteHotlink(r.PathValue("id"), req.Enabled, req.Allow); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handleAntiParsingGet / handleAntiParsingSet 防恶意解析开关
func (s *Server) handleAntiParsingGet(w http.ResponseWriter, r *http.Request) {
	OK(w, map[string]bool{"enabled": websites.AntiParsingEnabled()})
}

func (s *Server) handleAntiParsingSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.AntiParsingStatusToggle(req.Enabled); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

// handleSiteRewriteGet / Save 伪静态(重写规则)
func (s *Server) handleSiteRewriteGet(w http.ResponseWriter, r *http.Request) {
	content, err := websites.GetRewrite(r.PathValue("id"))
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"content": content})
}

func (s *Server) handleSiteRewriteSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.SetRewrite(r.PathValue("id"), req.Content); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handleSiteExtras 站点 HTTPS 增强/默认文档
func (s *Server) handleSiteExtras(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HTTP2        bool   `json:"http2"`
		HSTS         bool   `json:"hsts"`
		IndexFiles   string `json:"index_files"`
		BrowserCache bool   `json:"browser_cache"`
		RateLimit    int    `json:"rate_limit"`
		Maintenance  bool   `json:"maintenance"`
		SecHeaders   bool   `json:"sec_headers"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.SetSiteExtras(r.PathValue("id"), req.HTTP2, req.HSTS, req.IndexFiles, req.BrowserCache, req.RateLimit, req.Maintenance, req.SecHeaders); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handleGzipGet / Set 全站 Gzip 开关
func (s *Server) handleGzipGet(w http.ResponseWriter, r *http.Request) {
	OK(w, map[string]bool{"enabled": websites.GzipEnabled()})
}

func (s *Server) handleGzipSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.ApplyGzip(req.Enabled); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

// handleSiteUpstreamsGet / Save 反代负载均衡后端
func (s *Server) handleSiteUpstreamsGet(w http.ResponseWriter, r *http.Request) {
	OK(w, map[string]string{"content": websites.GetSiteUpstreams(r.PathValue("id"))})
}

func (s *Server) handleSiteUpstreamsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.SetSiteUpstreams(r.PathValue("id"), req.Content); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}
