package websites

// DNS API 自动验证:通过 DNS 服务商 API 自动添加/清理 TXT 记录,
// 免手动加记录,并解锁通配符证书。服务商实现来自内置 lego
// (对应 ACME 官方客户端支持列表,内置国内外常用 7 家)。
// 凭据保存在数据目录(0600),回显时打码,"••••" 开头的回传值视为沿用已存。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/alidns"
	"github.com/go-acme/lego/v4/providers/dns/baiducloud"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/dnspod"
	"github.com/go-acme/lego/v4/providers/dns/godaddy"
	"github.com/go-acme/lego/v4/providers/dns/huaweicloud"
	"github.com/go-acme/lego/v4/providers/dns/namesilo"
	"kunpanel/internal/util"
)

// DNSField 服务商凭据字段定义
type DNSField struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Secret   bool   `json:"secret,omitempty"`   // 回显打码
	Required bool   `json:"required,omitempty"` // 默认必填
}

// DNSProviderDef DNS 服务商预设
type DNSProviderDef struct {
	Key    string     `json:"key"`
	Name   string     `json:"name"`
	Fields []DNSField `json:"fields"`
}

var dnsProviders = []DNSProviderDef{
	{Key: "alidns", Name: "阿里云 DNS(万网)", Fields: []DNSField{
		{Key: "api_key", Name: "AccessKey ID"},
		{Key: "secret_key", Name: "AccessKey Secret", Secret: true, Required: true},
	}},
	{Key: "dnspod", Name: "腾讯云 DNSPod", Fields: []DNSField{
		{Key: "login_token", Name: "API Token(格式:ID,Token)", Secret: true, Required: true},
	}},
	{Key: "cloudflare", Name: "Cloudflare", Fields: []DNSField{
		{Key: "auth_token", Name: "API Token(Zone.DNS 编辑权限)", Secret: true, Required: true},
	}},
	{Key: "huaweicloud", Name: "华为云 DNS", Fields: []DNSField{
		{Key: "access_key", Name: "Access Key Id"},
		{Key: "secret_key", Name: "Secret Access Key", Secret: true, Required: true},
		{Key: "region", Name: "Region(如 cn-north-4)"},
	}},
	{Key: "baiducloud", Name: "百度智能云 DNS", Fields: []DNSField{
		{Key: "access_key", Name: "Access Key ID"},
		{Key: "secret_key", Name: "Secret Access Key", Secret: true, Required: true},
	}},
	{Key: "godaddy", Name: "GoDaddy", Fields: []DNSField{
		{Key: "api_key", Name: "API Key"},
		{Key: "api_secret", Name: "API Secret", Secret: true, Required: true},
	}},
	{Key: "namesilo", Name: "NameSilo", Fields: []DNSField{
		{Key: "api_key", Name: "API Key", Secret: true, Required: true},
	}},
}

// ListDNSProviders 供前端渲染凭据表单
func ListDNSProviders() []DNSProviderDef { return dnsProviders }

func dnsProviderDef(key string) (DNSProviderDef, bool) {
	for _, p := range dnsProviders {
		if p.Key == key {
			return p, true
		}
	}
	return DNSProviderDef{}, false
}

type dnsAPIConfig struct {
	Provider string            `json:"provider"`
	Creds    map[string]string `json:"creds"`
}

func dnsAPIPath() string { return filepath.Join(sslDir(), "dnsapi.json") }

func loadDNSAPI() dnsAPIConfig {
	c := dnsAPIConfig{}
	if b, err := os.ReadFile(dnsAPIPath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func saveDNSAPI(c dnsAPIConfig) error {
	_ = os.MkdirAll(sslDir(), 0o755)
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(dnsAPIPath(), b, 0o600)
}

// GetDNSAPI 当前配置(凭据打码)
func GetDNSAPI() map[string]any {
	c := loadDNSAPI()
	masked := map[string]string{}
	if def, ok := dnsProviderDef(c.Provider); ok && c.Provider != "" {
		for _, f := range def.Fields {
			v := c.Creds[f.Key]
			if v == "" {
				continue
			}
			if f.Secret {
				maskedVal := "••••"
				if len(v) > 4 {
					maskedVal += v[len(v)-4:]
				}
				masked[f.Key] = maskedVal
			} else {
				masked[f.Key] = v
			}
		}
	}
	return map[string]any{
		"providers": dnsProviders,
		"provider":  c.Provider,
		"creds":     masked,
	}
}

// SetDNSAPI 保存 DNS 服务商凭据;"••••" 开头的回传值沿用已存
func SetDNSAPI(provider string, creds map[string]string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		// 清空配置(用户主动停用 DNS API 自动验证)
		return saveDNSAPI(dnsAPIConfig{})
	}
	def, ok := dnsProviderDef(provider)
	if !ok {
		return fmt.Errorf("不支持的 DNS 服务商: %s", provider)
	}
	if creds == nil {
		creds = map[string]string{}
	}
	stored := loadDNSAPI()
	if stored.Provider == provider {
		// 打码回传沿用已存
		for k, v := range creds {
			if strings.HasPrefix(v, "••••") {
				if sv, ok := stored.Creds[k]; ok {
					creds[k] = sv
				} else {
					delete(creds, k)
				}
			}
		}
		// 未回传的字段沿用已存(签发请求只带变更项时)
		for _, f := range def.Fields {
			if _, ok := creds[f.Key]; !ok {
				if sv, ok := stored.Creds[f.Key]; ok {
					creds[f.Key] = sv
				}
			}
		}
	}
	for _, f := range def.Fields {
		if strings.HasPrefix(creds[f.Key], "••••") {
			delete(creds, f.Key) // 沿用来源已失效,不能落盘打码值
		}
		if f.Required && strings.TrimSpace(creds[f.Key]) == "" {
			return fmt.Errorf("%s 需要填写 %s", def.Name, f.Name)
		}
	}
	return saveDNSAPI(dnsAPIConfig{Provider: provider, Creds: creds})
}

// aliasMapProvider CNAME 别名委托包装:凭据所属服务商不托管目标域名的 DNS 时,
// 在主域 DNS 加 CNAME(_acme-challenge.主域 → _acme-challenge.别名域名),
// TXT 由 API 写到别名挑战名,Let's Encrypt 校验时沿 CNAME 读取
type aliasMapProvider struct {
	inner challenge.Provider
	alias string // 别名域名(不含 _acme-challenge. 前缀)
}

func (p *aliasMapProvider) Present(domain, token, keyAuth string) error {
	return p.inner.Present(p.alias, token, keyAuth)
}

func (p *aliasMapProvider) CleanUp(domain, token, keyAuth string) error {
	return p.inner.CleanUp(p.alias, token, keyAuth)
}

// buildDNS01ProviderWithAlias 构造 DNS 提供者;alias 非空时包装为别名映射
func buildDNS01ProviderWithAlias(alias string) (challenge.Provider, error) {
	inner, err := buildDNS01Provider()
	if err != nil {
		return nil, err
	}
	alias = strings.Trim(strings.TrimSpace(alias), ".")
	alias = strings.TrimPrefix(alias, "_acme-challenge.")
	if alias == "" {
		return inner, nil
	}
	return &aliasMapProvider{inner: inner, alias: alias}, nil
}

// buildDNS01Provider 按已存配置构造 lego DNS 提供者
func buildDNS01Provider() (challenge.Provider, error) {
	c := loadDNSAPI()
	if c.Provider == "" {
		return nil, fmt.Errorf("尚未配置 DNS 服务商 API,请先在 HTTPS 设置中选择并保存凭据")
	}
	cred := func(k string) string { return strings.TrimSpace(c.Creds[k]) }
	switch c.Provider {
	case "alidns":
		cfg := alidns.NewDefaultConfig()
		cfg.APIKey, cfg.SecretKey = cred("api_key"), cred("secret_key")
		if v := cred("region"); v != "" {
			cfg.RegionID = v
		}
		return alidns.NewDNSProviderConfig(cfg)
	case "dnspod":
		cfg := dnspod.NewDefaultConfig()
		cfg.LoginToken = cred("login_token")
		return dnspod.NewDNSProviderConfig(cfg)
	case "cloudflare":
		cfg := cloudflare.NewDefaultConfig()
		cfg.AuthToken = cred("auth_token")
		return cloudflare.NewDNSProviderConfig(cfg)
	case "huaweicloud":
		cfg := huaweicloud.NewDefaultConfig()
		cfg.AccessKeyID, cfg.SecretAccessKey = cred("access_key"), cred("secret_key")
		cfg.Region = cred("region")
		return huaweicloud.NewDNSProviderConfig(cfg)
	case "baiducloud":
		cfg := baiducloud.NewDefaultConfig()
		cfg.AccessKeyID, cfg.SecretAccessKey = cred("access_key"), cred("secret_key")
		return baiducloud.NewDNSProviderConfig(cfg)
	case "godaddy":
		cfg := godaddy.NewDefaultConfig()
		cfg.APIKey, cfg.APISecret = cred("api_key"), cred("api_secret")
		return godaddy.NewDNSProviderConfig(cfg)
	case "namesilo":
		cfg := namesilo.NewDefaultConfig()
		cfg.APIKey = cred("api_key")
		return namesilo.NewDNSProviderConfig(cfg)
	}
	return nil, fmt.Errorf("不支持的 DNS 服务商: %s", c.Provider)
}
