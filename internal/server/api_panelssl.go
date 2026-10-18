package server

// api_panelssl.go —— 面板自身 HTTPS(面板 SSL)
import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/taskx"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

func panelSSLCert() string { return filepath.Join(config.DataDir(), "panelssl", "cert.pem") }
func panelSSLKey() string  { return filepath.Join(config.DataDir(), "panelssl", "key.pem") }

// handlePanelSSLGet 当前状态(含已存证书的类别/域名/有效期,
// 供面板 HTTPS 设置区显示"当前使用什么证书")
func (s *Server) handlePanelSSLGet(w http.ResponseWriter, r *http.Request) {
	cfg := config.M.Get()
	info := map[string]any{
		"enabled":  cfg.SSL,
		"has_cert": false,
		"port":     cfg.Port,
	}
	if meta := websites.GetPanelLEMeta(); meta.Domain != "" {
		info["managed_domain"] = meta.Domain
	}
	if b, err := os.ReadFile(panelSSLCert()); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if crt, err := x509.ParseCertificate(blk.Bytes); err == nil {
				selfSigned := bytes.Equal(crt.RawIssuer, crt.RawSubject)
				issuer := crt.Issuer.CommonName + " " + strings.Join(crt.Issuer.Organization, " ")
				category := "上传证书"
				switch {
				case selfSigned:
					category = "自签证书"
				case strings.Contains(issuer, "Let's Encrypt"):
					category = "Let's Encrypt 证书"
				case strings.Contains(issuer, "ZeroSSL"):
					category = "ZeroSSL 证书"
				case strings.Contains(issuer, "Google Trust Services"), strings.Contains(issuer, "GTS"):
					category = "Google Trust Services 证书"
				}
				domains := append([]string{}, crt.DNSNames...)
				for _, ip := range crt.IPAddresses {
					domains = append(domains, ip.String())
				}
				info["has_cert"] = true
				info["category"] = category
				info["issuer"] = crt.Issuer.CommonName
				info["domains"] = domains
				info["expire"] = crt.NotAfter.Format("2006-01-02")
				info["days"] = int(time.Until(crt.NotAfter).Hours() / 24)
				info["self_signed"] = selfSigned
			} else {
				info["parse_error"] = fmt.Sprintf("证书解析失败: %v", err)
			}
		}
	}
	OK(w, info)
}

// handlePanelSSLSave 保存证书/开关;变更后需重启面板生效
func (s *Server) handlePanelSSLSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool   `json:"enabled"`
		Cert    string `json:"cert"`
		Key     string `json:"key"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	_ = os.MkdirAll(filepath.Dir(panelSSLCert()), 0o755)
	if req.Cert != "" || req.Key != "" {
		// 校验证书/私钥配对后再落盘
		if _, err := tls.X509KeyPair([]byte(req.Cert), []byte(req.Key)); err != nil {
			Fail(w, http.StatusBadRequest, "证书与私钥不匹配或格式错误: "+err.Error())
			return
		}
		if !isPEM(req.Cert) || !isPEM(req.Key) {
			Fail(w, http.StatusBadRequest, "PEM 格式错误")
			return
		}
		if err := util.AtomicWrite(panelSSLCert(), []byte(req.Cert+"\n"), 0o644); err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := util.AtomicWrite(panelSSLKey(), []byte(req.Key+"\n"), 0o600); err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		websites.ClearPanelLEMeta() // 手动上传的证书不再由自动续期管理
	}
	if req.Enabled {
		if _, err := os.Stat(panelSSLCert()); err != nil {
			Fail(w, http.StatusBadRequest, "尚未配置证书,请先上传或生成自签证书")
			return
		}
	}
	var sslOn bool
	stateChanged := false
	_ = config.M.Update(func(c *config.Config) {
		if c.SSL != req.Enabled {
			c.SSL = req.Enabled
			stateChanged = true
		}
		sslOn = c.SSL
	})
	if stateChanged {
		// 让响应先到达浏览器,再自动重启面板使 HTTPS 生效
		go func() {
			time.Sleep(1500 * time.Millisecond)
			_, _ = util.RunWithTimeout(30*time.Second, "systemctl", "restart", "kunpanel")
		}()
	}
	OK(w, map[string]any{"enabled": sslOn, "need_restart": stateChanged})
}

// handlePanelSSLSelfSign 为面板生成自签证书(本机 IP/主机名)
func (s *Server) handlePanelSSLSelfSign(w http.ResponseWriter, r *http.Request) {
	_ = os.MkdirAll(filepath.Dir(panelSSLCert()), 0o755)
	if _, err := util.RunWithTimeout(30*time.Second, "openssl", "req", "-x509", "-newkey", "rsa:2048", "-sha256", "-nodes",
		"-days", "3650",
		"-subj", "/CN=KunPanel",
		"-addext", "subjectAltName=IP:"+util.LocalIP()+",IP:127.0.0.1,DNS:localhost",
		"-keyout", panelSSLKey(), "-out", panelSSLCert()); err != nil {
		Fail(w, http.StatusInternalServerError, "生成失败: "+err.Error())
		return
	}
	_ = os.Chmod(panelSSLKey(), 0o600)
	OK(w, "自签证书已生成(浏览器会提示不受信任,属正常现象)")
}

// handlePanelSSLIssueLE 为面板申请 Let's Encrypt 证书(DNS API 验证)
// 并自动部署启用:签发成功后面板以 HTTPS 模式重启
func (s *Server) handlePanelSSLIssueLE(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Domain) == "" {
		Fail(w, http.StatusBadRequest, "请填写面板访问域名")
		return
	}
	t, err := taskx.M.Start("panel-le", func(w io.Writer) error {
		if err := websites.IssuePanelLE(req.Domain, w); err != nil {
			return err
		}
		// 部署生效:启用面板 SSL 并重启加载
		_ = config.M.Update(func(c *config.Config) { c.SSL = true })
		go func() {
			time.Sleep(1500 * time.Millisecond)
			_, _ = util.RunWithTimeout(30*time.Second, "systemctl", "restart", "kunpanel")
		}()
		return nil
	})
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

func isPEM(s string) bool {
	return strings.Contains(s, "-----BEGIN") && strings.Contains(s, "-----END")
}
