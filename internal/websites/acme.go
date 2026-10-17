// websites/acme.go —— 免费证书签发(lego 引擎):
//   - 多 CA 可选:Let's Encrypt / ZeroSSL / Google Trust Services / BuyPass / 测试环境
//   - HTTP-01(写站点根验证文件)与 DNS-01(手动 TXT + 轮询,支持别名 CNAME 委托)
//   - 账户与注册信息持久化,到期自动续期(DNS 证书到期前提醒手动续)
package websites

import (
	"sync"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"

	"kunpanel/internal/taskx"
	"kunpanel/internal/util"
)

const (
	acmeDirProd    = "https://acme-v02.api.letsencrypt.org/directory"
	acmeDirStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// ACMEProvider CA 服务商预设(ZeroSSL/Google/BuyPass 需要 EAB 凭据)
type ACMEProvider struct {
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	NeedEAB bool   `json:"need_eab"`
	Staging bool   `json:"staging,omitempty"`
}

var acmeProviders = map[string]ACMEProvider{
	"letsencrypt":      {Name: "Let's Encrypt", Dir: acmeDirProd},
	"zerossl":          {Name: "ZeroSSL", Dir: "https://acme.zerossl.com/v2/DV90", NeedEAB: true},
	"google":           {Name: "Google Trust Services", Dir: "https://dv.acme-v02.api.pki.goog/directory", NeedEAB: true},
	"buypass":          {Name: "BuyPass Go SSL", Dir: "https://api.buypass.com/acme/directory", NeedEAB: true},
	"letsencrypt_test": {Name: "Let's Encrypt 测试", Dir: acmeDirStaging, Staging: true},
}

// ListACMEProviders 供前端下拉渲染
func ListACMEProviders() []ACMEProvider {
	order := []string{"letsencrypt", "zerossl", "google", "buypass", "letsencrypt_test"}
	out := make([]ACMEProvider, 0, len(order))
	for _, k := range order {
		p := acmeProviders[k]
		out = append(out, p)
	}
	return out
}

func providerDir(provider string) string {
	if p, ok := acmeProviders[provider]; ok && p.Dir != "" {
		return p.Dir
	}
	return acmeDirProd
}

func providerStaging(provider string) bool {
	p, ok := acmeProviders[provider]
	return ok && p.Staging
}

// acmeUser 实现 lego registration.User
type acmeUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// ACMEAccount 账户持久化结构
type ACMEAccount struct {
	Email        string          `json:"email"`
	Provider     string          `json:"provider"`
	Directory    string          `json:"directory"`
	KeyPEM       string          `json:"key_pem"`
	Registration *registration.Resource `json:"registration,omitempty"`
	EABKeyID     string          `json:"eab_key_id,omitempty"`
	EABHMAC      string          `json:"eab_hmac,omitempty"`
}

func acmeAccountPath() string { return filepath.Join(sslDir(), "acme_account.json") }

func loadACMEAccount() *ACMEAccount {
	a := &ACMEAccount{Provider: "letsencrypt"}
	b, err := os.ReadFile(acmeAccountPath())
	if err == nil {
		_ = json.Unmarshal(b, a)
	}
	if a.Provider == "" {
		a.Provider = "letsencrypt"
		if a.Directory == acmeDirStaging {
			a.Provider = "letsencrypt_test"
		}
	}
	return a
}

func saveACMEAccount(a *ACMEAccount) error {
	_ = os.MkdirAll(sslDir(), 0o755)
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(acmeAccountPath(), b, 0o600)
}

// GetACMEConfig 返回账户配置(隐藏私钥;HMAC 掩码)
func GetACMEConfig() map[string]any {
	a := loadACMEAccount()
	hmacMasked := ""
	if a.EABHMAC != "" {
		hmacMasked = "••••••••"
		if len(a.EABHMAC) > 8 {
			hmacMasked += a.EABHMAC[len(a.EABHMAC)-4:]
		}
	}
	return map[string]any{
		"email":      a.Email,
		"provider":   a.Provider,
		"staging":    providerStaging(a.Provider),
		"registered": a.Registration != nil && a.KeyPEM != "",
		"eab_key_id": a.EABKeyID,
		"eab_hmac":   hmacMasked,
		"has_eab":    a.EABKeyID != "" && a.EABHMAC != "",
	}
}

// SetACMEConfig 兼容旧签名
func SetACMEConfig(email string, staging bool) error {
	p := "letsencrypt"
	if staging {
		p = "letsencrypt_test"
	}
	return SetACMEConfigEx(email, p, "", "")
}

// SetACMEConfigEx 设置邮箱、CA 服务商与 EAB;切换后旧注册作废(自动重注册)
func SetACMEConfigEx(email, provider, eabKeyID, eabHMAC string) error {
	email = strings.TrimSpace(email)
	if email != "" && (!strings.Contains(email, "@") || strings.ContainsAny(email, " \t;/\\'\"")) {
		return fmt.Errorf("邮箱格式不正确")
	}
	if provider == "" {
		provider = "letsencrypt"
	}
	p, ok := acmeProviders[provider]
	if !ok {
		return fmt.Errorf("不支持的证书服务商: %s", provider)
	}
	if p.NeedEAB && (strings.TrimSpace(eabKeyID) == "" || strings.TrimSpace(eabHMAC) == "") {
		// 调用方未带 EAB 时沿用已保存的凭据(此前签发请求切 CA 会因 HMAC 为空被拒)
		a0 := loadACMEAccount()
		if a0.Provider == provider && a0.EABKeyID != "" && a0.EABHMAC != "" {
			eabKeyID, eabHMAC = a0.EABKeyID, a0.EABHMAC
		} else {
			return fmt.Errorf("%s 需要填写 EAB 凭据(KeyID 与 HMAC)", p.Name)
		}
	}
	a := loadACMEAccount()
	a.Email = email
	a.Provider = provider
	// 先比较再赋值:此处判断必须基于旧 Directory,否则永远相等、旧注册永不作废
	dirChanged := a.Directory != providerDir(provider)
	a.Directory = providerDir(provider)
	eabHMAC = strings.TrimSpace(eabHMAC)
	if strings.HasPrefix(eabHMAC, "••••") {
		eabHMAC = a.EABHMAC // 掩码回显占位:保留已存值
	}
	keyIDChanged := strings.TrimSpace(eabKeyID) != "" && strings.TrimSpace(eabKeyID) != a.EABKeyID
	if strings.TrimSpace(eabKeyID) != "" || eabHMAC != "" {
		a.EABKeyID = strings.TrimSpace(eabKeyID)
		a.EABHMAC = eabHMAC
	}
	if !p.NeedEAB {
		a.EABKeyID, a.EABHMAC = "", ""
	}
	if dirChanged || keyIDChanged {
		a.Registration = nil // 换 CA/凭据:重新注册
	}
	return saveACMEAccount(a)
}

// acmeClientFor 构造 lego 客户端(自动注册/恢复账户)
func acmeClientFor(provider string, w io.Writer) (*lego.Client, *acmeUser, error) {
	return acmeClientForStaged(provider, false, w)
}

// acmeClientForStaged stagingOverride=true 时本次使用 LE staging 目录,
// 但不改动已保存的账户目录(生产账户继续用于正常签发)
func acmeClientForStaged(provider string, stagingOverride bool, w io.Writer) (*lego.Client, *acmeUser, error) {
	dir := providerDir(provider)
	if stagingOverride && !providerStaging(provider) {
		dir = acmeDirStaging
	}
	a := loadACMEAccount()
	if a.Provider != provider {
		a.Provider = provider
		a.Directory = dir
	}
	if a.KeyPEM == "" {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, nil, err
		}
		a.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		if err := saveACMEAccount(a); err != nil {
			return nil, nil, err
		}
	}
	blk, _ := pem.Decode([]byte(a.KeyPEM))
	if blk == nil {
		return nil, nil, fmt.Errorf("ACME 账户私钥损坏,请删除 %s 后重试", acmeAccountPath())
	}
	key, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("ACME 账户私钥损坏,请删除 %s 后重试", acmeAccountPath())
	}
	// Registration 必须在 NewClient 前设置:lego core 构造时一次性固化 KID,
	// 之后设置无效(恢复账户场景会导致所有请求缺 kid 而 400)
	u := &acmeUser{Email: a.Email, key: key, Registration: a.Registration}
	// staging 覆盖签发:生产账户的 KID 在 staging 无效(报 No embedded JWK),
	// 必须清空走独立注册
	if stagingOverride {
		u.Registration = nil
	}
	cfg := lego.NewConfig(u)
	cfg.CADirURL = dir
	cfg.Certificate.KeyType = certcrypto.EC256
	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("初始化 ACME 客户端失败: %v", err)
	}
	// 注册信息一致性:registration URI 必须属于当前 CA(存量脏数据自动作废重注册)
	regOK := a.Registration != nil && a.Registration.URI != "" && !stagingOverride
	if regOK {
		if ru, err := url.Parse(a.Registration.URI); err == nil {
			if du, err2 := url.Parse(dir); err2 == nil {
				regOK = ru.Host == du.Host
			}
		}
	}
	if !regOK {
		fmt.Fprintf(w, ">> 向 %s 注册账户...\n", providerName(provider))
		if a.EABKeyID != "" && a.EABHMAC != "" {
			hmacKey, err := base64.RawURLEncoding.DecodeString(a.EABHMAC)
			if err != nil {
				hmacKey, err = base64.StdEncoding.DecodeString(a.EABHMAC)
			}
			if err != nil {
				return nil, nil, fmt.Errorf("EAB HMAC 格式不正确(应为 base64): %v", err)
			}
			a.EABHMAC = base64.RawURLEncoding.EncodeToString(hmacKey)
			reg, err := client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{
				TermsOfServiceAgreed: true,
				Kid:                  a.EABKeyID,
				HmacEncoded:          a.EABHMAC,
			})
			if err != nil {
				return nil, nil, fmt.Errorf("EAB 注册失败: %v", err)
			}
			u.Registration = reg
		} else {
			reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
			if err != nil {
				return nil, nil, fmt.Errorf("ACME 注册失败: %v", err)
			}
			u.Registration = reg
		}
		a.Registration = u.Registration
		if !stagingOverride {
			if err := saveACMEAccount(a); err != nil {
				return nil, nil, err
			}
		}
	} else {
		u.Registration = a.Registration
	}
	return client, u, nil
}

// acmeChallengeDir 挑战文件目录(站点根)
func acmeChallengeDir(site *Site) string {
	return filepath.Join(site.Root, ".well-known", "acme-challenge")
}

// httpFileProvider HTTP-01:写站点根验证文件
type httpFileProvider struct{ site *Site }

func (p *httpFileProvider) Present(domain, token, keyAuth string) error {
	chDir := acmeChallengeDir(p.site)
	if err := os.MkdirAll(chDir, 0o755); err != nil {
		return err
	}
	if err := util.AtomicWrite(filepath.Join(chDir, token), []byte(keyAuth), 0o644); err != nil {
		return err
	}
	fmt.Printf(">> 验证文件已写入: %s\n", "/.well-known/acme-challenge/"+token)
	return nil
}

func (p *httpFileProvider) CleanUp(domain, token, keyAuth string) error {
	_ = os.RemoveAll(filepath.Join(p.site.Root, ".well-known"))
	return nil
}

// manualDNSProvider DNS-01:输出 TXT 记录 → 轮询生效(present 阻塞等待用户操作)
type manualDNSProvider struct {
	w     io.Writer
	alias string
}

func (p *manualDNSProvider) Present(domain, token, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	if p.alias != "" {
		fmt.Fprintf(p.w, ">> [别名模式] 请添加 TXT 记录: 主机 %s | 值 %s\n", "_acme-challenge."+p.alias, info.Value)
		fmt.Fprintf(p.w, ">> (需在主域 DNS 添加 CNAME: %s → %s)\n", info.FQDN, "_acme-challenge."+p.alias)
	} else {
		fmt.Fprintf(p.w, ">> 请添加 TXT 记录: 主机 %s | 值 %s\n", info.FQDN, info.Value)
	}
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		txts, err := net.LookupTXT(info.FQDN)
		if err == nil {
			for _, t := range txts {
				if strings.TrimSpace(t) == info.Value {
					fmt.Fprintf(p.w, ">> DNS 记录已生效(%s)\n", info.FQDN)
					return nil
				}
			}
		}
		time.Sleep(8 * time.Second)
	}
	return fmt.Errorf("等待 %s 的 TXT 记录超时:请确认记录已保存且已生效", info.FQDN)
}

func (p *manualDNSProvider) CleanUp(domain, token, keyAuth string) error { return nil }

// acmeIssueOpts 签发参数
type AcmeIssueOpts struct {
	Staging bool
	Method  string // http / dns
	Alias   string // DNS 别名域名(CNAME 委托目标,可选)
}

// orderDomains 校验并归一域名(通配符仅 DNS 验证支持)。
// 返回值 isIP:订单中含 IP 标识符(LE 的 IP 证书仅 shortlived 档案,6 天有效期)
func orderDomains(raw []string, method string) ([]string, bool, error) {
	var out []string
	isIP := false
	for _, d := range raw {
		d = strings.TrimSpace(d)
		// HTTP 验证剥掉 *. 前缀(且下方直接拒绝);DNS 验证必须保留通配符 SAN,
		// 否则签出的证书不含 *.example.com(此前静默降级为裸域证书)
		if method != "dns" {
			d = strings.TrimPrefix(d, "*.")
		}
		if d == "" {
			continue
		}
		if net.ParseIP(d) != nil {
			isIP = true
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, false, fmt.Errorf("站点没有域名")
	}
	if method == "http" {
		for _, d := range raw {
			if strings.Contains(d, "*") {
				return nil, isIP, fmt.Errorf("HTTP 验证不支持通配符域名,请改用 DNS 验证")
			}
		}
	}
	return out, isIP, nil
}

// IssueCertForSite 签发证书并自动绑定(异步任务体)
func IssueCertForSite(siteID string, opts AcmeIssueOpts, w io.Writer) (string, error) {
	site, err := GetSite(siteID)
	if err != nil {
		return "", err
	}
	method := opts.Method
	if method != "dns" {
		method = "http"
	}
	domains, isIP, err := orderDomains(strings.Fields(site.Domain), method)
	if err != nil {
		return "", err
	}
	// IP 证书:LE 仅提供 shortlived 档案(约 6 天),仅 HTTP 验证(DNS 验证对 IP 无意义)
	profile := ""
	if isIP {
		profile = "shortlived"
		if method == "dns" {
			method = "http"
			fmt.Fprintln(w, ">> 检测到 IP 标识:改用 HTTP 文件验证,并使用 shortlived 档案(约 6 天有效期,自动续期)")
		}
	}
	prov := loadACMEAccount().Provider
	staging := opts.Staging || providerStaging(prov)
	// staging 覆盖:账户目录仍是生产时,本次签发强制走 staging 目录
	stagingOverride := staging && !providerStaging(prov)
	fmt.Fprintf(w, ">> 为 %s 申请 %s 证书(%s / %s 验证)...\n", site.Domain, providerName(prov), stageName(staging), map[string]string{"http": "HTTP 文件", "dns": "DNS"}[method])
	alias := strings.TrimSpace(opts.Alias)
	if alias != "" && method == "dns" {
		alias = strings.TrimSuffix(strings.TrimPrefix(alias, "_acme-challenge."), ".")
	}

	client, _, err := acmeClientForStaged(prov, stagingOverride, w)
	if err != nil {
		return "", err
	}

	if method == "dns" {
		client.Challenge.SetDNS01Provider(&manualDNSProvider{w: w, alias: alias},
			dns01.AddRecursiveNameservers([]string{"8.8.8.8:53", "1.1.1.1:53"}))
	} else {
		client.Challenge.SetHTTP01Provider(&httpFileProvider{site: &site})
	}

	fmt.Fprintf(w, ">> 创建订单并验证域名...\n")
	var issuedCSR []byte
	var res *certificate.Resource
	if isIP {
		// LE 拒绝 CN 含 IP 的 CSR(badCSR),而 lego 默认把首个域放进 CN——
		// IP 证书改走自建 CSR:CN 留空,IP 进 SAN(IPAddress),域名进 DNS SAN。
		// CSR 存档后续期走同一 CSR 路径(RenewWithOptions 检测到 CSR 即
		// ObtainForCSR,天然绕开 CN 问题)
		var ips []net.IP
		var dnsNames []string
		for _, d := range domains {
			if ip := net.ParseIP(d); ip != nil {
				ips = append(ips, ip)
			} else {
				dnsNames = append(dnsNames, d)
			}
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return "", err
		}
		csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
			IPAddresses: ips,
			DNSNames:    dnsNames,
		}, key)
		if err != nil {
			return "", err
		}
		csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
		issuedCSR = csrPEM
		csrObj, err := x509.ParseCertificateRequest(csrDER)
		if err != nil {
			return "", err
		}
		res, err = client.Certificate.ObtainForCSR(certificate.ObtainForCSRRequest{
			CSR:        csrObj,
			PrivateKey: key,
			Bundle:     true,
			Profile:    profile,
		})
	} else {
		res, err = client.Certificate.Obtain(certificate.ObtainRequest{
			Domains: domains,
			Bundle:  true,
			Profile: profile,
		})
	}
	if err != nil {
		if method == "dns" {
			return "", fmt.Errorf("DNS 验证失败: %v(请检查 TXT 记录是否按要求添加并已生效)", err)
		}
		if isIP {
			return "", fmt.Errorf("签发失败: %v(请确认该 IP 的 80 端口对外开放且站点监听 80)", err)
		}
		return "", fmt.Errorf("签发失败: %v(请确认域名已解析到本服务器 IP,且 80 端口对外开放)", err)
	}

	certID := "cert_" + fmt.Sprintf("%d", time.Now().UnixNano()/1000000)
	_ = os.MkdirAll(sslDir(), 0o755)
	if err := util.AtomicWrite(certCrtPath(certID), res.Certificate, 0o644); err != nil {
		return "", err
	}
	if err := util.AtomicWrite(certKeyPath(certID), res.PrivateKey, 0o600); err != nil {
		return "", err
	}
	if len(issuedCSR) > 0 {
		// IP 证书:存档 CSR,续期走同一 CSR 路径
		if err := util.AtomicWrite(certCsrPath(certID), issuedCSR, 0o600); err != nil {
			return "", err
		}
	}
	expire, days := parseCertPEM(res.Certificate)
	info := CertInfo{
		ID: certID, Name: domains[0] + "(LE)", Domains: strings.Join(domains, " "),
		Expire: expire, Days: days, SelfSigned: false,
		Acme: true, SiteID: siteID, Staging: staging,
		VMethod: method, VAlias: alias, Profile: profile,
		Created: time.Now().Format("2006-01-02 15:04:05"),
	}
	if profile != "" {
		fmt.Fprintf(w, ">> shortlived 档案:有效期约 %d 天,到期前 3 天自动续期\n", days)
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

// renewCertInPlace 原位续期:按证书的验证方式重新验证并替换证书内容
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
	method := info.VMethod
	if method == "" {
		method = "http"
	}
	fmt.Fprintf(w, ">> 续期 %s(%s 验证)...\n", info.Domains, map[string]string{"http": "HTTP", "dns": "DNS"}[method])
	var site Site
	if info.SiteID != "" {
		if s0, err := GetSite(info.SiteID); err == nil {
			site = s0
		}
	}
	prov := loadACMEAccount().Provider
	client, user, err := acmeClientFor(prov, w)
	_ = user
	if err != nil {
		return err
	}
	alias := info.VAlias
	if method == "dns" {
		fmt.Fprintf(w, ">> DNS 验证的证书续期需要更新 TXT 记录(值每次不同),请按提示操作\n")
		client.Challenge.SetDNS01Provider(&manualDNSProvider{w: w, alias: alias},
			dns01.AddRecursiveNameservers([]string{"8.8.8.8:53", "1.1.1.1:53"}))
	} else {
		if site.ID == "" {
			return fmt.Errorf("原站点已删除,无法续期(请删除该证书后重新申请)")
		}
		client.Challenge.SetHTTP01Provider(&httpFileProvider{site: &site})
	}
	// lego Renew:从现有证书/私钥文件构造 Resource 并重签。存有 CSR 的证书
	// (IP 证书)走 CSR 路径——ObtainForCSR 校验的正是这份 CSR,可绕开
	// lego 默认 CN=首域导致的 badCSR;档案沿用签发时的 shortlived
	certRes := certificate.Resource{
		Certificate: certCrtBytes(id),
		PrivateKey:  certKeyBytes(id),
		CSR:         certCSRBytes(id),
	}
	res, err := client.Certificate.RenewWithOptions(certRes, &certificate.RenewOptions{
		Bundle:  true,
		Profile: info.Profile,
	})
	if err != nil {
		if method == "dns" {
			return fmt.Errorf("DNS 续期失败: %v", err)
		}
		return fmt.Errorf("续期失败: %v", err)
	}
	// CSR 路径续期时 lego 不回传私钥(沿用 CSR 对应的旧私钥),别把密钥文件写空
	if len(res.PrivateKey) == 0 {
		res.PrivateKey = certRes.PrivateKey
	}
	if err := util.AtomicWrite(certCrtPath(id), res.Certificate, 0o644); err != nil {
		return err
	}
	if err := util.AtomicWrite(certKeyPath(id), res.PrivateKey, 0o600); err != nil {
		return err
	}
	expire, days := parseCertPEM(res.Certificate)
	info.Expire, info.Days = expire, days
	info.Created = time.Now().Format("2006-01-02 15:04:05")
	if err := saveCertMeta(info); err != nil {
		return err
	}
	fmt.Fprintf(w, ">> 续期完成,新有效期至 %s(%d 天)\n", expire, days)
	return Reload()
}

// StartACMERenew 证书巡检自动续期;DNS 证书仅提醒手动续
// renewMu 续期互斥:自动续期与手动续期并发会同时覆盖证书/私钥/元数据
var renewMu sync.Mutex

func StartACMERenew(stop <-chan struct{}, notify func(msg string)) {
	go func() {
		time.Sleep(90 * time.Second)
		lastTry := map[string]string{}
		run := func() {
			today := time.Now().Format("2006-01-02")
			for _, c := range ListCerts() {
				if !c.Acme || c.Expire == "" || c.Days > c.renewDueDays() {
					continue
				}
				if lastTry[c.ID] == today {
					continue
				}
				lastTry[c.ID] = today
				if c.VMethod == "dns" {
					if notify != nil {
						notify(fmt.Sprintf("【证书即将到期】%s 使用 DNS 验证,需手动续期:请到站点 HTTPS 设置重新申请,或到证书列表点续期并按任务提示更新 TXT 记录(剩余 %d 天)", c.Domains, c.Days))
					}
					continue
				}
				cert := c
				go func() {
					renewMu.Lock()
					defer renewMu.Unlock()
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

func certCrtBytes(id string) []byte {
	b, _ := os.ReadFile(certCrtPath(id))
	return b
}

func certKeyBytes(id string) []byte {
	b, _ := os.ReadFile(certKeyPath(id))
	return b
}

func certCSRBytes(id string) []byte {
	b, _ := os.ReadFile(certCsrPath(id))
	return b
}

func parseCertPEM(chainPEM []byte) (string, int) {
	blk, _ := pem.Decode(chainPEM)
	if blk == nil {
		return "", 0
	}
	return parseCertExpiry(blk.Bytes)
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

func providerName(provider string) string {
	if p, ok := acmeProviders[provider]; ok {
		return p.Name
	}
	return provider
}

// IssueCertTask 兼容旧签名
func IssueCertTask(siteID string, staging bool) (string, error) {
	return IssueCertTaskEx(siteID, AcmeIssueOpts{Staging: staging})
}

// IssueCertTaskEx 异步签发任务
func IssueCertTaskEx(siteID string, opts AcmeIssueOpts) (string, error) {
	t, err := taskx.M.Start("acme-issue", func(w io.Writer) error {
		_, err := IssueCertForSite(siteID, opts, w)
		return err
	})
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// RenewCertTask 异步续期任务
func RenewCertTask(certID string, w io.Writer) error {
	renewMu.Lock()
	defer renewMu.Unlock()
	return renewCertInPlace(certID, w)
}
