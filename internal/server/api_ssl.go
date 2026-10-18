package server

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"kunpanel/internal/filemgr"
	"kunpanel/internal/taskx"
	"kunpanel/internal/websites"
)

func (s *Server) handleCertList(w http.ResponseWriter, r *http.Request) {
	OK(w, websites.ListCerts())
}

type certUploadReq struct {
	Name string `json:"name"`
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

func (s *Server) handleCertUpload(w http.ResponseWriter, r *http.Request) {
	var req certUploadReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	info, err := websites.UploadCert(req.Name, req.Cert, req.Key)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, info)
}

type selfSignReq struct {
	Domain string `json:"domain"`
	Days   int    `json:"days"`
}

func (s *Server) handleCertSelfSign(w http.ResponseWriter, r *http.Request) {
	var req selfSignReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	info, err := websites.SelfSignCert(req.Domain, req.Days)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, info)
}

func (s *Server) handleCertDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法证书 ID")
		return
	}
	if err := websites.DeleteCert(id); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handleCertBatchDelete 批量删除证书;被站点占用的逐项报错不影响其余
func (s *Server) handleCertBatchDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if len(req.IDs) == 0 {
		Fail(w, http.StatusBadRequest, "未选择证书")
		return
	}
	deleted := 0
	failed := map[string]string{}
	for _, id := range req.IDs {
		if err := websites.DeleteCert(id); err != nil {
			failed[id] = err.Error()
			continue
		}
		deleted++
	}
	OK(w, map[string]any{"deleted": deleted, "failed": failed})
}

// ---------- Let's Encrypt (ACME) ----------

func (s *Server) handleAcmeGet(w http.ResponseWriter, r *http.Request) {
	OK(w, websites.GetACMEConfig())
}

type acmeCfgReq struct {
	Email    string `json:"email"`
	Staging  bool   `json:"staging"`
	Provider string `json:"provider"` // letsencrypt/zerossl/google/buypass/letsencrypt_test
	EABKeyID string `json:"eab_key_id"`
	EABHMAC  string `json:"eab_hmac"`
}

func (s *Server) handleAcmeSet(w http.ResponseWriter, r *http.Request) {
	var req acmeCfgReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	provider := req.Provider
	if provider == "" {
		// 兼容旧前端:仅传 staging 时映射 LE 正式/测试
		provider = "letsencrypt"
		if req.Staging {
			provider = "letsencrypt_test"
		}
	}
	if err := websites.SetACMEConfigEx(req.Email, provider, req.EABKeyID, req.EABHMAC); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, websites.GetACMEConfig())
}

// handleAcmeProviders CA 服务商列表(前端下拉)
func (s *Server) handleAcmeProviders(w http.ResponseWriter, r *http.Request) {
	OK(w, websites.ListACMEProviders())
}

func (s *Server) handleAcmeIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SiteID      string   `json:"site_id"`
		Staging     bool     `json:"staging"`
		Method      string   `json:"method"` // http / dns / dnsapi(默认 http)
		Alias       string   `json:"alias"`  // DNS 别名域名(CNAME 委托目标,可选;仅手动 DNS)
		Provider    string   `json:"provider"`
		DNSApi      bool     `json:"dns_api"`   // DNS API 自动验证(凭据用已保存配置)
		DNSApiAlias string   `json:"dns_alias"` // DNS API 模式的 CNAME 别名域名(可选)
		Domains     []string `json:"domains"`   // 勾选要签入证书的域名(站点绑定域名的子集;空=全部)
		Wildcard    bool     `json:"wildcard"`  // 自动组合泛域名 *.主域(仅 DNS 验证)
	}
	if err := readJSON(r, &req); err != nil || req.SiteID == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if _, err := websites.GetSite(req.SiteID); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// 签发环境与账户环境保持一致(账户未切换时沿用其当前 provider)
	if req.Provider != "" {
		cfg := websites.GetACMEConfig()
		email, _ := cfg["email"].(string)
		hasEAB, _ := cfg["has_eab"].(bool)
		eabKey, _ := cfg["eab_key_id"].(string)
		_ = hasEAB
		if err := websites.SetACMEConfigEx(email, req.Provider, eabKey, ""); err != nil {
			Fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Method != "dns" {
		req.Method = "http"
	}
	taskID, err := websites.IssueCertTaskEx(req.SiteID, websites.AcmeIssueOpts{
		Staging: req.Staging, Method: req.Method, Alias: req.Alias, DNSApi: req.DNSApi, DNSApiAlias: req.DNSApiAlias,
		Domains: req.Domains, Wildcard: req.Wildcard,
	})
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": taskID})
}

// handleDNSAPIInfo DNS 服务商列表 + 当前已存凭据(打码)
func (s *Server) handleDNSAPIInfo(w http.ResponseWriter, r *http.Request) {
	OK(w, websites.GetDNSAPI())
}

// handleDNSAPISet 保存 DNS 服务商凭据
func (s *Server) handleDNSAPISet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string            `json:"provider"`
		Creds    map[string]string `json:"creds"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.SetDNSAPI(strings.TrimSpace(req.Provider), req.Creds); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, websites.GetDNSAPI())
}

func (s *Server) handleAcmeRenew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertID string `json:"cert_id"`
	}
	if err := readJSON(r, &req); err != nil || req.CertID == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if _, ok := websites.GetCert(req.CertID); !ok {
		Fail(w, http.StatusBadRequest, "证书不存在")
		return
	}
	t, err := taskx.M.Start("acme-renew", func(w io.Writer) error {
		return websites.RenewCertTask(req.CertID, w)
	})
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

type siteSSLReq struct {
	CertID   string `json:"cert_id"`
	Redirect bool   `json:"redirect"`
}

func (s *Server) handleSiteSSL(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	var req siteSSLReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.SetSiteSSL(id, req.CertID, req.Redirect); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

// handleCertDownload 打包下载证书与私钥(zip)
func (s *Server) handleCertDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法证书 ID")
		return
	}
	crt, key, err := websites.CertDownloadFiles(id)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	zipPath := crt + ".zip"
	if err := filemgr.Compress(zipPath, []string{crt, key}); err != nil {
		Fail(w, http.StatusInternalServerError, "打包失败: "+err.Error())
		return
	}
	defer os.Remove(zipPath)
	name := id
	if info, ok := websites.GetCert(id); ok {
		name = info.Name
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name)+"_cert.zip")
	f, _ := os.Open(zipPath)
	defer f.Close()
	io.Copy(w, f)
}
