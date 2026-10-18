package websites

// 面板自身 HTTPS 的 Let's Encrypt 证书:通过 DNS API 自动验证(DNS-01)直接
// 为面板访问域名签发/续期证书并部署到 data/panelssl/——不需要网关、不需要
// 建站、不需要开放 80 端口;签发后自动启用面板 HTTPS,到期自动续期。

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"kunpanel/internal/config"
	"kunpanel/internal/util"
)

type PanelLEMeta struct {
	Domain   string `json:"domain"`
	Provider string `json:"provider"` // DNS 服务商(续期复用其凭据)
	IssuedAt string `json:"issued_at"`
}

func PanelSSLDir() string          { return filepath.Join(config.DataDir(), "panelssl") }
func panelLEMetaPath() string      { return filepath.Join(PanelSSLDir(), "meta.json") }
func PanelSSLCertPath() string     { return filepath.Join(PanelSSLDir(), "cert.pem") }
func PanelSSLKeyPath() string      { return filepath.Join(PanelSSLDir(), "key.pem") }

// PanelLEMeta 面板 LE 证书的托管信息(空 = 无托管证书,自动续期跳过)
func GetPanelLEMeta() PanelLEMeta {
	var m PanelLEMeta
	if b, err := os.ReadFile(panelLEMetaPath()); err == nil {
		_ = jsonUnmarshalPanel(b, &m)
	}
	return m
}

func savePanelLEMeta(m PanelLEMeta) error {
	_ = os.MkdirAll(PanelSSLDir(), 0o755)
	b, err := jsonMarshalPanel(m)
	if err != nil {
		return err
	}
	return util.AtomicWrite(panelLEMetaPath(), b, 0o600)
}

// ClearPanelLEMeta 手动上传证书覆盖后调用:面板证书不再由自动续期管理
func ClearPanelLEMeta() { _ = os.Remove(panelLEMetaPath()) }

// IssuePanelLE 为面板域名签发 LE 证书(DNS API 验证)并部署
func IssuePanelLE(domain string, w io.Writer) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || strings.Contains(domain, "*") || strings.Contains(domain, " ") {
		return fmt.Errorf("域名不合法: %s", domain)
	}
	if net.ParseIP(domain) != nil {
		return fmt.Errorf("IP 证书不适用 DNS 验证:面板 LE 证书需要使用域名(如 panel.example.com)")
	}
	if !strings.Contains(domain, ".") {
		return fmt.Errorf("域名不合法: %s", domain)
	}
	if loadDNSAPI().Provider == "" {
		return fmt.Errorf("尚未配置 DNS 服务商 API 凭据")
	}
	prov, err := buildDNS01Provider()
	if err != nil {
		return err
	}
	accountProv := loadACMEAccount().Provider
	client, user, err := acmeClientFor(accountProv, w)
	if err != nil {
		return err
	}
	_ = user
	client.Challenge.SetDNS01Provider(prov,
		dns01.AddRecursiveNameservers([]string{"8.8.8.8:53", "1.1.1.1:53"}))

	fmt.Fprintf(w, ">> 为 %s 申请 Let's Encrypt 证书(DNS API 自动验证)...\n", domain)
	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: []string{domain},
		Bundle:  true,
	})
	if err != nil {
		return fmt.Errorf("签发失败: %v(请检查 DNS API 凭据是否有效、域名是否托管在该服务商)", err)
	}
	if err := util.AtomicWrite(PanelSSLCertPath(), res.Certificate, 0o644); err != nil {
		return err
	}
	if err := util.AtomicWrite(PanelSSLKeyPath(), res.PrivateKey, 0o600); err != nil {
		return err
	}
	expire, days := parseCertPEM(res.Certificate)
	if err := savePanelLEMeta(PanelLEMeta{
		Domain: domain, Provider: loadDNSAPI().Provider,
		IssuedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		return err
	}
	fmt.Fprintf(w, ">> 证书已签发并部署,有效期至 %s(%d 天),到期前 30 天自动续期\n", expire, days)
	fmt.Fprintf(w, ">> 请使用 https://%s:%s 访问面板\n", domain, currentPanelPort())
	return nil
}

// RenewPanelLE 面板 LE 证书自动续期(到期前 30 天;由证书巡检循环调用)
func RenewPanelLE(notify func(msg string)) {
	meta := GetPanelLEMeta()
	if meta.Domain == "" {
		return
	}
	b, err := os.ReadFile(PanelSSLCertPath())
	if err != nil {
		return
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return
	}
	crt, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return
	}
	days := int(time.Until(crt.NotAfter).Hours() / 24)
	if days > 30 {
		return
	}
	if loadDNSAPI().Provider == "" {
		if notify != nil {
			notify(fmt.Sprintf("【面板证书即将到期】%s 剩余 %d 天,但 DNS API 凭据未配置,无法自动续期;请重新申请或手动上传", meta.Domain, days))
		}
		return
	}
	var buf strings.Builder
	if err := IssuePanelLE(meta.Domain, &buf); err != nil {
		if notify != nil {
			notify(fmt.Sprintf("【面板证书续期失败】%s: %v", meta.Domain, err))
		}
		return
	}
	// 面板正以 HTTPS 运行时,重启加载新证书(短暂中断,数秒恢复)
	if config.M.Get().SSL {
		go func() {
			time.Sleep(time.Second)
			_, _ = util.RunWithTimeout(30*time.Second, "systemctl", "restart", "kunpanel")
		}()
	}
	if notify != nil {
		notify(fmt.Sprintf("【面板证书已自动续期】%s 已部署新证书", meta.Domain))
	}
}

// ---- 小工具(避免引入多余依赖)----

func jsonUnmarshalPanel(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

func jsonMarshalPanel(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

func currentPanelPort() string {
	return fmt.Sprintf("%d", config.M.Get().Port)
}

