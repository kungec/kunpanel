// websites/acme.go —— Let's Encrypt 免费证书:ACME 账户管理、HTTP-01 签发、原位续期
package websites

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"

	"kunpanel/internal/taskx"
	"kunpanel/internal/util"
)

// ACME 目录(个人版面板固定官方源;staging 供测试避免触发正式环境限频)
const (
	acmeDirProd    = "https://acme-v02.api.letsencrypt.org/directory"
	acmeDirStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// ACMEAccount ACME 账户配置(存储于 ssl 目录)
type ACMEAccount struct {
	Email      string `json:"email"`
	Directory  string `json:"directory"` // 完整目录 URL
	KeyPEM     string `json:"key_pem"`
	Registered bool   `json:"registered"`
}

func acmeAccountPath() string { return filepath.Join(sslDir(), "acme_account.json") }

func loadACMEAccount() *ACMEAccount {
	b, err := os.ReadFile(acmeAccountPath())
	if err != nil {
		return &ACMEAccount{}
	}
	var a ACMEAccount
	if json.Unmarshal(b, &a) != nil {
		return &ACMEAccount{}
	}
	return &a
}

func saveACMEAccount(a *ACMEAccount) error {
	_ = os.MkdirAll(sslDir(), 0o755)
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(acmeAccountPath(), b, 0o600)
}

// GetACMEConfig 返回账户配置(隐藏私钥)
func GetACMEConfig() map[string]any {
	a := loadACMEAccount()
	return map[string]any{
		"email":      a.Email,
		"staging":    a.Directory == acmeDirStaging,
		"registered": a.Registered && a.KeyPEM != "",
	}
}

// SetACMEConfig 设置邮箱与测试开关;切换环境后需重新注册账户
func SetACMEConfig(email string, staging bool) error {
	a := loadACMEAccount()
	email = strings.TrimSpace(email)
	if email != "" && (!strings.Contains(email, "@") || strings.ContainsAny(email, " \t;/\\'\"")) {
		return fmt.Errorf("邮箱格式不正确")
	}
	a.Email = email
	dir := acmeDirProd
	if staging {
		dir = acmeDirStaging
	}
	if a.Directory != dir {
		a.Registered = false // 换环境必须重新注册
		a.Directory = dir
	}
	return saveACMEAccount(a)
}

// acmeClient 用账户私钥构造客户端,必要时自动注册
func acmeClient(ctx context.Context, a *ACMEAccount) (*acme.Client, error) {
	if a.KeyPEM == "" {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		a.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		if err := saveACMEAccount(a); err != nil {
			return nil, err
		}
	}
	blk, _ := pem.Decode([]byte(a.KeyPEM))
	if blk == nil {
		return nil, fmt.Errorf("ACME 账户私钥损坏,请删除 %s 后重试", acmeAccountPath())
	}
	signer, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	dirURL := a.Directory
	if dirURL == "" {
		dirURL = acmeDirProd
	}
	client := &acme.Client{Key: signer.(crypto.Signer), DirectoryURL: dirURL}
	if !a.Registered {
		// Discover 预热目录并验证连通性(TOS 由 prompt 直接接受)
		if _, err := client.Discover(ctx); err != nil {
			return nil, fmt.Errorf("连接 CA 失败: %v", err)
		}
		var contact []string
		if a.Email != "" {
			contact = []string{"mailto:" + a.Email}
		}
		if _, err := client.Register(ctx, &acme.Account{Contact: contact}, func(tos string) bool { return true }); err != nil {
			// 账户已存在(密钥复用)不算错误
			if ae, ok := err.(*acme.Error); !ok || ae.StatusCode != 409 {
				return nil, fmt.Errorf("ACME 注册失败: %v", err)
			}
		}
		a.Registered = true
		if err := saveACMEAccount(a); err != nil {
			return nil, err
		}
	}
	return client, nil
}

// siteForChallenge 签发挑战文件目录(站点根),v1 仅支持面板管理的站点域名
func acmeChallengeDir(site *Site) string {
	return filepath.Join(site.Root, ".well-known", "acme-challenge")
}

func cleanupChallenge(site *Site) {
	_ = os.RemoveAll(filepath.Join(site.Root, ".well-known"))
}

// IssueCertForSite 为站点域名申请 Let's Encrypt 证书并自动绑定(异步任务体)
// 成功后证书 ID 返回;签发过程输出进度到 w
func IssueCertForSite(siteID string, staging bool, w io.Writer) (string, error) {
	site, err := GetSite(siteID)
	if err != nil {
		return "", err
	}
	domains := strings.Fields(site.Domain)
	if len(domains) == 0 {
		return "", fmt.Errorf("站点没有域名")
	}
	// ACME 不接受通配符走 HTTP-01
	for _, d := range domains {
		if strings.Contains(d, "*") {
			return "", fmt.Errorf("HTTP-01 不支持通配符域名 %s,请使用 DNS 验证(暂未支持)", d)
		}
	}
	fmt.Fprintf(w, ">> 为 %s 申请 Let's Encrypt 证书(%s)...\n", site.Domain, stageName(staging))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	a := loadACMEAccount()
	client, err := acmeClient(ctx, a)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(w, ">> 已连接 CA,创建订单...\n")
	ids := make([]acme.AuthzID, len(domains))
	for i, d := range domains {
		ids[i] = acme.AuthzID{Type: "dns", Value: d}
	}
	order, err := client.AuthorizeOrder(ctx, ids)
	if err != nil {
		return "", fmt.Errorf("创建订单失败: %v", err)
	}

	// 写入 HTTP-01 挑战文件并应答
	for _, azURL := range order.AuthzURLs {
		az, err := client.GetAuthorization(ctx, azURL)
		if err != nil {
			return "", fmt.Errorf("获取验证信息失败: %v", err)
		}
		if az.Status == "valid" {
			continue
		}
		var chal *acme.Challenge
		for _, ch := range az.Challenges {
			if ch.Type == "http-01" {
				chal = ch
				break
			}
		}
		if chal == nil {
			return "", fmt.Errorf("CA 未提供 http-01 验证方式")
		}
		keyAuth, err := client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return "", err
		}
		chDir := acmeChallengeDir(&site)
		if err := os.MkdirAll(chDir, 0o755); err != nil {
			return "", err
		}
		tokenFile := filepath.Join(chDir, chal.Token)
		if err := util.AtomicWrite(tokenFile, []byte(keyAuth), 0o644); err != nil {
			return "", err
		}
		fmt.Fprintf(w, ">> 验证文件已写入: %s\n", "/.well-known/acme-challenge/"+chal.Token)
		if _, err := client.Accept(ctx, chal); err != nil {
			cleanupChallenge(&site)
			return "", fmt.Errorf("提交验证失败: %v", err)
		}
	}

	// 轮询等待全部验证通过
	fmt.Fprintf(w, ">> 等待 CA 验证(需域名解析到本服务器且 80 端口可达,最长 3 分钟)...\n")
	for _, azURL := range order.AuthzURLs {
		if _, err := client.WaitAuthorization(ctx, azURL); err != nil {
			cleanupChallenge(&site)
			if ae, ok := err.(*acme.AuthorizationError); ok {
				return "", fmt.Errorf("域名验证失败(%s): 请确认域名已解析到本服务器 IP,且 80 端口对外开放", ae.Error())
			}
			return "", fmt.Errorf("域名验证超时或失败: %v", err)
		}
	}
	cleanupChallenge(&site)

	// 生成证书私钥与 CSR
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: domains[0]},
		DNSNames:           domains,
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}, certKey)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(w, ">> 验证通过,签发证书...\n")
	der, _, err := client.CreateOrderCert(ctx, order.URI, csrDER, true)
	if err != nil {
		return "", fmt.Errorf("签发失败: %v", err)
	}

	// 存储证书链 + 私钥
	var chain strings.Builder
	for _, d := range der {
		chain.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d}))
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(certKey)
	if err != nil {
		return "", err
	}
	certID := "cert_" + fmt.Sprintf("%d", time.Now().UnixNano()/1000000)
	_ = os.MkdirAll(sslDir(), 0o755)
	if err := util.AtomicWrite(certCrtPath(certID), []byte(chain.String()), 0o644); err != nil {
		return "", err
	}
	if err := util.AtomicWrite(certKeyPath(certID), []byte(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))), 0o600); err != nil {
		return "", err
	}
	// 解析叶证书有效期
	expire, days := parseCertExpiry(der[0])
	info := CertInfo{
		ID: certID, Name: domains[0] + "(LE)", Domains: strings.Join(domains, " "),
		Expire: expire, Days: days, SelfSigned: false,
		Acme: true, SiteID: siteID, Staging: staging,
		Created: time.Now().Format("2006-01-02 15:04:05"),
	}
	if err := saveCertMeta(info); err != nil {
		return "", err
	}
	fmt.Fprintf(w, ">> 证书已签发,有效期至 %s(%d 天)\n", expire, days)
	if err := SetSiteSSL(siteID, certID, true); err != nil {
		return certID, fmt.Errorf("证书已签发但站点绑定失败: %v", err)
	}
	fmt.Fprintf(w, ">> 已绑定站点并启用 HTTPS 强制跳转\n")
	return certID, nil
}

// renewCertInPlace 原位续期:保持证书 ID 与站点绑定不变,仅替换证书内容
func renewCertInPlace(id string, w io.Writer) error {
	info, ok := GetCert(id)
	if !ok {
		return fmt.Errorf("证书不存在")
	}
	if !info.Acme {
		return fmt.Errorf("非 ACME 证书不支持自动续期")
	}
	domains := strings.Fields(info.Domains)
	if len(domains) == 0 {
		return fmt.Errorf("证书没有域名信息")
	}
	fmt.Fprintf(w, ">> 续期 %s ...\n", info.Domains)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	a := loadACMEAccount()
	client, err := acmeClient(ctx, a)
	if err != nil {
		return err
	}
	ids := make([]acme.AuthzID, len(domains))
	for i, d := range domains {
		ids[i] = acme.AuthzID{Type: "dns", Value: d}
	}
	order, err := client.AuthorizeOrder(ctx, ids)
	if err != nil {
		return fmt.Errorf("创建订单失败: %v", err)
	}
	// 续期时站点可能已删除;挑战文件写入原站点根目录,站点不存在则报错提示
	var site Site
	if info.SiteID != "" {
		if s, err := GetSite(info.SiteID); err == nil {
			site = s
		}
	}
	if site.ID == "" {
		return fmt.Errorf("原站点已删除,无法续期(请删除该证书后重新申请)")
	}
	for _, azURL := range order.AuthzURLs {
		az, err := client.GetAuthorization(ctx, azURL)
		if err != nil {
			return err
		}
		if az.Status == "valid" {
			continue
		}
		var chal *acme.Challenge
		for _, ch := range az.Challenges {
			if ch.Type == "http-01" {
				chal = ch
			}
		}
		if chal == nil {
			return fmt.Errorf("CA 未提供 http-01 验证方式")
		}
		keyAuth, err := client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return err
		}
		chDir := acmeChallengeDir(&site)
		if err := os.MkdirAll(chDir, 0o755); err != nil {
			return err
		}
		if err := util.AtomicWrite(filepath.Join(chDir, chal.Token), []byte(keyAuth), 0o644); err != nil {
			return err
		}
		if _, err := client.Accept(ctx, chal); err != nil {
			cleanupChallenge(&site)
			return err
		}
	}
	fmt.Fprintf(w, ">> 等待 CA 验证...\n")
	for _, azURL := range order.AuthzURLs {
		if _, err := client.WaitAuthorization(ctx, azURL); err != nil {
			cleanupChallenge(&site)
			return fmt.Errorf("域名验证失败: %v", err)
		}
	}
	cleanupChallenge(&site)

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: domains[0]},
		DNSNames:           domains,
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}, certKey)
	if err != nil {
		return err
	}
	der, _, err := client.CreateOrderCert(ctx, order.URI, csrDER, true)
	if err != nil {
		return fmt.Errorf("签发失败: %v", err)
	}
	var chain strings.Builder
	for _, d := range der {
		chain.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d}))
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(certKey)
	if err != nil {
		return err
	}
	if err := util.AtomicWrite(certCrtPath(id), []byte(chain.String()), 0o644); err != nil {
		return err
	}
	if err := util.AtomicWrite(certKeyPath(id), []byte(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))), 0o600); err != nil {
		return err
	}
	expire, days := parseCertExpiry(der[0])
	info.Expire, info.Days = expire, days
	info.Created = time.Now().Format("2006-01-02 15:04:05")
	if err := saveCertMeta(info); err != nil {
		return err
	}
	fmt.Fprintf(w, ">> 续期完成,新有效期至 %s(%d 天)\n", expire, days)
	return Reload()
}

// StartACMERenew 在证书巡检中自动续期:剩余 ≤30 天的 ACME 证书每天尝试一次
func StartACMERenew(stop <-chan struct{}, notify func(msg string)) {
	go func() {
		time.Sleep(90 * time.Second)
		lastTry := map[string]string{} // certID -> 日期,防止一天内反复触发
		run := func() {
			today := time.Now().Format("2006-01-02")
			for _, c := range ListCerts() {
				if !c.Acme || c.Expire == "" || c.Days > 30 {
					continue
				}
				if lastTry[c.ID] == today {
					continue
				}
				lastTry[c.ID] = today
				cert := c
				go func() {
					var buf strings.Builder
					err := renewCertInPlace(cert.ID, &buf)
					if notify != nil {
						if err != nil {
							notify(fmt.Sprintf("【证书续期失败】%s: %v", cert.Domains, err))
						} else {
							notify(fmt.Sprintf("【证书已自动续期】%s 新有效期至 %s", cert.Domains, cert.Expire))
						}
					}
				}()
			}
		}
		run()
		tick := time.NewTicker(6 * time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				run()
			}
		}
	}()
}

func parseCertExpiry(der []byte) (string, int) {
	if crt, err := x509.ParseCertificate(der); err == nil {
		return crt.NotAfter.Format("2006-01-02"), int(time.Until(crt.NotAfter).Hours() / 24)
	}
	return "", 0
}

func stageName(staging bool) string {
	if staging {
		return "测试环境 staging"
	}
	return "正式环境"
}

// IssueCertTask 以异步任务方式发起签发(供 API 调用)
func IssueCertTask(siteID string, staging bool) (string, error) {
	t, err := taskx.M.Start("acme-issue", func(w io.Writer) error {
		_, err := IssueCertForSite(siteID, staging, w)
		return err
	})
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// RenewCertTask 以异步任务方式续期(供 API 调用)
func RenewCertTask(certID string, w io.Writer) error {
	return renewCertInPlace(certID, w)
}

// httpClient 留作自定义超时扩展位
var _ = http.DefaultClient
