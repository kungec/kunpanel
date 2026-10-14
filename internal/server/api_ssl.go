package server

import (
	"io"
	"net/http"
	"net/url"
	"os"

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

// ---------- Let's Encrypt (ACME) ----------

func (s *Server) handleAcmeGet(w http.ResponseWriter, r *http.Request) {
	OK(w, websites.GetACMEConfig())
}

type acmeCfgReq struct {
	Email   string `json:"email"`
	Staging bool   `json:"staging"`
}

func (s *Server) handleAcmeSet(w http.ResponseWriter, r *http.Request) {
	var req acmeCfgReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.SetACMEConfig(req.Email, req.Staging); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, websites.GetACMEConfig())
}

func (s *Server) handleAcmeIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SiteID  string `json:"site_id"`
		Staging bool   `json:"staging"`
	}
	if err := readJSON(r, &req); err != nil || req.SiteID == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if _, err := websites.GetSite(req.SiteID); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// staging 旗标同步到 ACME 账户环境,确保签发 CA 与选择一致
	cfg := websites.GetACMEConfig()
	if err := websites.SetACMEConfig(cfg["email"].(string), req.Staging); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	taskID, err := websites.IssueCertTask(req.SiteID, req.Staging)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": taskID})
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
