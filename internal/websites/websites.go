package websites

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/appstore"
	"kunpanel/internal/config"
	"kunpanel/internal/util"
)

// domainTokenRe 严格限制域名可出现字符,阻断 Nginx 配置注入
var domainTokenRe = regexp.MustCompile(`^[a-zA-Z0-9*_.-]{1,253}$`)

// ValidateDomainsPublic 供测试与外部调用的域名校验入口
func ValidateDomainsPublic(domain string) error {
	return validateDomains(domain)
}

// validateDomains 校验空格分隔的多域名
func validateDomains(domain string) error {
	tokens := strings.Fields(domain)
	if len(tokens) == 0 {
		return fmt.Errorf("域名不能为空")
	}
	if len(tokens) > 10 {
		return fmt.Errorf("域名数量过多(最多 10 个)")
	}
	for _, t := range tokens {
		if !domainTokenRe.MatchString(t) {
			return fmt.Errorf("域名包含非法字符: %q(仅允许字母数字与 . * _ -)", t)
		}
	}
	return nil
}

// Site 一个网站条目
type Site struct {
	ID          string `json:"id"`
	Domain      string `json:"domain"` // 多个用空格分隔
	Alias       string `json:"alias"`
	Type        string `json:"type"` // static / proxy
	Root        string `json:"root"`
	ProxyTo     string `json:"proxy_to"`
	Port        int    `json:"port"` // 监听端口,默认 80
	Enabled     bool   `json:"enabled"`
	SslCertID   string `json:"ssl_cert_id"`  // 绑定的证书 ID,空 = 未启用 HTTPS
	SslRedirect bool   `json:"ssl_redirect"` // HTTP 强制跳转 HTTPS
	AuthEnabled bool   `json:"auth_enabled"` // 密码访问(HTTP Basic Auth)
	AuthUser    string `json:"auth_user,omitempty"`
	// 防盗链:静态资源 Referer 不在白名单时返回 403
	HotlinkProtect bool   `json:"hotlink_protect"`
	HotlinkAllow   string `json:"hotlink_allow,omitempty"` // 额外允许的 referer 域名(空格分隔)
	// HTTPS 增强
	HTTP2      bool   `json:"http2"`                 // listen 443 ssl http2
	HSTS       bool   `json:"hsts"`                  // Strict-Transport-Security 响应头
	IndexFiles string `json:"index_files,omitempty"` // 默认文档(空 = index.html index.htm)
	// 静态资源浏览器缓存(30 天)
	BrowserCache bool `json:"browser_cache"`
	// 维护模式:全部请求返回 503 维护页
	Maintenance bool `json:"maintenance"`
	// 安全响应头(XFO/XCTO/Referrer-Policy)
	SecHeaders bool `json:"sec_headers"`
	// 流量限制(KB/s,0 = 不限速)
	RateLimit int `json:"rate_limit,omitempty"`
	// 反向代理负载均衡后端(非空时替代 ProxyTo,加权轮询)
	Upstreams []Upstream `json:"upstreams,omitempty"`
	CreatedAt      string `json:"created_at"`
	Remark         string `json:"remark"`
}

// Upstream 反代负载均衡后端
type Upstream struct {
	Target string `json:"target"` // http://host:port
	Weight int    `json:"weight,omitempty"`
}

var (
	mu   sync.Mutex
	list []Site
)

func baseDir() string  { return filepath.Join(config.DataDir(), "websites") }
func confDir() string  { return filepath.Join(baseDir(), "conf.d") }
func rootsDir() string { return filepath.Join(baseDir(), "roots") }
func storeFile() string {
	return filepath.Join(baseDir(), "sites.json")
}

func load() {
	if b, err := os.ReadFile(storeFile()); err == nil {
		_ = json.Unmarshal(b, &list)
	}
}

func save() error {
	_ = os.MkdirAll(baseDir(), 0o755)
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(storeFile(), b, 0o600)
}

// ReloadMode 说明当前 nginx 的应用方式
type ReloadMode struct {
	Mode   string `json:"mode"` // container / host / none
	Target string `json:"target"`
}

// WafEnabled 由插件模块注入(plugins.Init);返回 true 时新站点配置自动注入 WAF 防护
var WafEnabled func() bool

func wafOn() bool {
	return WafEnabled != nil && WafEnabled()
}

// RewriteAllConfs 按当前状态重新生成全部已启用站点配置并重载(WAF 启停时调用)
func RewriteAllConfs() error {
	mu.Lock()
	load()
	sites := make([]Site, len(list))
	copy(sites, list)
	mu.Unlock()
	for i := range sites {
		if sites[i].Enabled {
			if err := writeConf(&sites[i]); err != nil {
				return err
			}
		} else {
			_ = os.Remove(filepath.Join(confDir(), sites[i].ID+".conf"))
		}
	}
	return Reload()
}

// DetectNginx 探测可用的 nginx:面板管理的 openresty/nginx 容器 > 宿主机 nginx
func DetectNginx() ReloadMode {
	// 面板管理的容器(应用商店的 OpenResty 与 Nginx 均可)
	for _, name := range []string{"kun-openresty", "kun-nginx"} {
		if out, err := exec.Command("docker", "ps", "--filter", "name="+name, "--format", "{{.ID}}").CombinedOutput(); err == nil && strings.TrimSpace(string(out)) != "" {
			return ReloadMode{Mode: "container", Target: name}
		}
	}
	if _, err := exec.LookPath("nginx"); err == nil {
		return ReloadMode{Mode: "host", Target: "nginx"}
	}
	return ReloadMode{Mode: "none", Target: ""}
}

// List 站点列表
func List() ([]Site, error) {
	mu.Lock()
	load()
	out := make([]Site, len(list))
	copy(out, list)
	mu.Unlock()
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt > out[b].CreatedAt })
	return out, nil
}

// CreateOpts 创建站点参数
type CreateOpts struct {
	Domain   string
	SiteType string // static / php / proxy
	ProxyTo  string // proxy 类型必填
	RootDir  string // 可选:自定义根目录(默认自动生成)
	Remark   string
	Port     int
}

// Add 新建站点(兼容旧签名)
func Add(domain, siteType, proxyTo, remark string, port int) (*Site, error) {
	return Create(CreateOpts{Domain: domain, SiteType: siteType, ProxyTo: proxyTo, Remark: remark, Port: port})
}

// Create 新建站点(宝塔式):静态 / PHP / 反向代理,支持自定义根目录
func Create(o CreateOpts) (*Site, error) {
	mu.Lock()
	defer mu.Unlock()
	load()
	domain := strings.TrimSpace(strings.Join(strings.Fields(o.Domain), " "))
	if err := validateDomains(domain); err != nil {
		return nil, err
	}
	if o.SiteType != "static" && o.SiteType != "proxy" && o.SiteType != "php" {
		return nil, fmt.Errorf("不支持的站点类型")
	}
	if o.SiteType == "proxy" && strings.TrimSpace(o.ProxyTo) == "" {
		return nil, fmt.Errorf("代理目标不能为空")
	}
	if o.Port <= 0 {
		o.Port = 80
	}
	for _, s := range list {
		if s.Domain == domain && s.Port == o.Port {
			return nil, fmt.Errorf("站点已存在: %s:%d", domain, o.Port)
		}
	}
	id := fmt.Sprintf("site_%d", time.Now().UnixNano()/1000000)
	s := &Site{
		ID: id, Domain: domain, Alias: strings.Fields(domain)[0], Type: o.SiteType,
		Port: o.Port, Enabled: true,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"), Remark: o.Remark,
		Root: filepath.Join(rootsDir(), id),
	}
	if o.SiteType == "php" {
		// PHP 站点:全部请求转发到 PHP 运行时容器,容器内按站点目录区分
		s.ProxyTo = PhpUpstream(id)
	}
	if o.SiteType == "proxy" {
		s.ProxyTo = strings.TrimSpace(o.ProxyTo)
	}
	// 自定义根目录(仅 static/php;需绝对路径且不与系统目录冲突)
	if rd := strings.TrimSpace(o.RootDir); rd != "" && o.SiteType != "proxy" {
		if !filepath.IsAbs(rd) || rd == "/" || strings.Contains(rd, "..") {
			return nil, fmt.Errorf("根目录格式不正确")
		}
		if strings.HasPrefix(filepath.Clean(rd), "/etc") || strings.HasPrefix(filepath.Clean(rd), "/usr") ||
			strings.HasPrefix(filepath.Clean(rd), "/boot") || strings.HasPrefix(filepath.Clean(rd), "/proc") ||
			strings.HasPrefix(filepath.Clean(rd), "/sys") {
			return nil, fmt.Errorf("根目录不允许使用系统目录")
		}
		s.Root = filepath.Clean(rd)
	}
	if o.SiteType != "proxy" {
		if err := os.MkdirAll(s.Root, 0o755); err != nil {
			return nil, err
		}
		index := filepath.Join(s.Root, "index.html")
		if !fileExists(index) {
			_ = util.AtomicWrite(index, []byte(fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>%s</title></head>
<body style="font-family:sans-serif;display:flex;align-items:center;justify-content:center;height:90vh">
<h1>%s 部署成功 — 由坤面板驱动</h1>
</body></html>
`, domain, domain)), 0o644)
		}
	}
	list = append(list, *s)
	if err := save(); err != nil {
		return nil, err
	}
	if err := writeConf(s); err != nil {
		return nil, err
	}
	_ = Reload()
	return s, nil
}

// UpdateDomains 修改站点域名(域名管理)
func UpdateDomains(id, domain string) error {
	mu.Lock()
	load()
	domain = strings.TrimSpace(strings.Join(strings.Fields(domain), " "))
	if err := validateDomains(domain); err != nil {
		mu.Unlock()
		return err
	}
	var site *Site
	for i := range list {
		if list[i].ID == id {
			site = &list[i]
			break
		}
	}
	if site == nil {
		mu.Unlock()
		return fmt.Errorf("站点不存在")
	}
	site.Domain = domain
	site.Alias = strings.Fields(domain)[0]
	if err := save(); err != nil {
		mu.Unlock()
		return err
	}
	cp := *site
	mu.Unlock()
	if err := writeConf(&cp); err != nil {
		return err
	}
	return Reload()
}

// GetSiteConf 读取站点 nginx 配置原文
func GetSiteConf(id string) (string, error) {
	if _, err := GetSite(id); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(confDir(), id+".conf"))
	if err != nil {
		return "", fmt.Errorf("配置文件不存在")
	}
	return string(b), nil
}

// SaveSiteConf 保存站点 nginx 配置:先校验再落盘,失败自动还原
func SaveSiteConf(id, content string) error {
	if _, err := GetSite(id); err != nil {
		return err
	}
	path := filepath.Join(confDir(), id+".conf")
	old, _ := os.ReadFile(path)
	if err := util.AtomicWrite(path, []byte(content), 0o644); err != nil {
		return err
	}
	if err := Reload(); err != nil {
		// 校验失败还原
		_ = util.AtomicWrite(path, old, 0o644)
		_ = Reload()
		return fmt.Errorf("配置校验失败已还原: %v", err)
	}
	return nil
}

// GetSite 按 ID 获取站点(导出供备份等使用)
func GetSite(id string) (Site, error) {
	mu.Lock()
	defer mu.Unlock()
	load()
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return Site{}, fmt.Errorf("站点不存在")
}

// Toggle 启用/禁用站点
func Toggle(id string, enabled bool) error {
	mu.Lock()
	defer mu.Unlock()
	load()
	for i := range list {
		if list[i].ID == id {
			list[i].Enabled = enabled
			if err := save(); err != nil {
				return err
			}
			if err := writeConf(&list[i]); err != nil {
				return err
			}
			return Reload()
		}
	}
	return fmt.Errorf("站点不存在")
}

// Delete 删除站点(可选保留根目录数据)
func Delete(id string) error {
	mu.Lock()
	defer mu.Unlock()
	load()
	var kept []Site
	found := false
	for _, s := range list {
		if s.ID == id {
			found = true
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		return fmt.Errorf("站点不存在")
	}
	list = kept
	if err := save(); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(confDir(), id+".conf"))
	return Reload()
}

// UpdateSite 编辑站点(代理目标 / 端口 / 备注)
func UpdateSite(id, proxyTo, remark string, port int) error {
	mu.Lock()
	load()
	var site *Site
	for i := range list {
		if list[i].ID == id {
			site = &list[i]
			break
		}
	}
	if site == nil {
		mu.Unlock()
		return fmt.Errorf("站点不存在")
	}
	if port <= 0 {
		port = 80
	}
	if port > 65535 {
		mu.Unlock()
		return fmt.Errorf("端口超出范围")
	}
	if site.Type == "proxy" && strings.TrimSpace(proxyTo) == "" {
		mu.Unlock()
		return fmt.Errorf("代理目标不能为空")
	}
	// 换端口时防重复
	for _, s := range list {
		if s.ID != id && s.Domain == site.Domain && s.Port == port {
			mu.Unlock()
			return fmt.Errorf("端口 %d 已被站点 %s 使用", port, s.Alias)
		}
	}
	site.ProxyTo = strings.TrimSpace(proxyTo)
	site.Remark = remark
	site.Port = port
	if err := save(); err != nil {
		mu.Unlock()
		return err
	}
	cp := *site // 解锁后写 conf 用副本
	mu.Unlock()
	if err := writeConf(&cp); err != nil {
		return err
	}
	return Reload()
}

// writeConf 生成站点 nginx 配置
func writeConf(s *Site) error {
	var b strings.Builder
	if s.Type == "proxy" && len(s.Upstreams) > 0 {
		// upstream 是 http 上下文指令,须写在 server 块之前的文件顶层
		b.WriteString("# 负载均衡后端(由坤面板生成)\nupstream kun_up_" + s.ID + " {\n")
		for _, u := range s.Upstreams {
			addr := strings.TrimPrefix(strings.TrimPrefix(u.Target, "http://"), "https://")
			if u.Weight > 1 {
				b.WriteString("    server " + addr + " weight=" + strconv.Itoa(u.Weight) + ";\n")
			} else {
				b.WriteString("    server " + addr + ";\n")
			}
		}
		b.WriteString("}\n\n")
	}
	if err := os.MkdirAll(confDir(), 0o755); err != nil {
		return err
	}
	path := filepath.Join(confDir(), s.ID+".conf")
	if !s.Enabled {
		_ = os.Remove(path)
		return nil
	}

	mode := DetectNginx()
	root := s.Root
	challengeRoot := s.Root
	if mode.Mode == "container" {
		// 容器内挂载路径:roots 目录 -> /var/www
		root = "/var/www/" + s.ID
		challengeRoot = root
	}

	b.WriteString(fmt.Sprintf("# 由坤面板生成,请勿手动修改站点 ID: %s\n", s.ID))

	// ACME HTTP-01 挑战目录(^~ 优先于业务 location,静态/代理站点通用)
	challengeLoc := fmt.Sprintf("    location ^~ /.well-known/acme-challenge/ {\n        root %s;\n    }\n\n", challengeRoot)

	// HTTPS:证书绑定时同时监听 443;强制跳转时主块仅监听 443,由独立块接管 80
	redirectActive := s.SslCertID != "" && s.SslRedirect
	sslLines := ""
	if s.SslCertID != "" {
		if crt, key, err := CertFilePaths(s.SslCertID); err == nil {
			sslLines = fmt.Sprintf("    ssl_certificate %s;\n    ssl_certificate_key %s;\n    ssl_protocols TLSv1.2 TLSv1.3;\n    ssl_session_cache shared:SSL_%s:10m;\n",
				crt, key, s.ID)
			if s.HSTS {
				sslLines += "    add_header Strict-Transport-Security \"max-age=31536000; includeSubDomains\" always;\n"
			}
		}
	}
		h2 := ""
	if s.HTTP2 {
		h2 = " http2"
	}
	b.WriteString("server {\n")
	if redirectActive && s.Port == 80 {
		b.WriteString("    listen 443 ssl" + h2 + ";\n")
	} else {
		b.WriteString(fmt.Sprintf("    listen %d;\n", s.Port))
		if s.SslCertID != "" {
			b.WriteString("    listen 443 ssl" + h2 + ";\n")
		}
	}
	b.WriteString(fmt.Sprintf("    server_name %s;\n", s.Domain))
	if s.Maintenance {
		// 维护模式:全部请求(含子路径)返回 503 维护页;ACME 验证在维护期间不可用
		b.WriteString(`    # 维护模式(由坤面板生成)
    default_type text/html;
    return 503 "<!DOCTYPE html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>网站维护中</title><style>body{font-family:-apple-system,'PingFang SC','Microsoft YaHei',sans-serif;background:#f2f4f7;display:flex;align-items:center;justify-content:center;height:96vh;margin:0}.card{background:#fff;border-radius:16px;padding:52px 64px;text-align:center;box-shadow:0 12px 40px rgba(0,60,160,.10);max-width:92vw}.ic{width:64px;height:64px;margin:0 auto 18px;border-radius:50%;background:#fff7e6;display:flex;align-items:center;justify-content:center;font-size:30px}.card h1{font-size:19px;color:#1a202c;margin:0 0 10px}p{color:#909399;font-size:13.5px;margin:4px 0}</style></head><body><div class=\"card\"><div class=\"ic\">🛠</div><h1>网站维护中</h1><p>我们正在升级站点,请稍后访问</p><p>给您带来不便,敬请谅解</p></div></body></html>";
`)
	}
	if s.SecHeaders {
		b.WriteString(`    # 安全响应头(由坤面板生成)
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;

`)
	}
	// 站点独立日志文件(落在挂载的 /var/log/nginx,可被「日志切割」任务轮转归档)
	safeName := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s.Domain)
	fmt.Fprintf(&b, "    access_log /var/log/nginx/%s.access.log;\n", safeName)
	fmt.Fprintf(&b, "    error_log /var/log/nginx/%s.error.log;\n\n", safeName)
	if sslLines != "" {
		b.WriteString("    # HTTPS 证书\n" + sslLines + "\n")
	}
	if wafOn() {
		// WAF 插件生效:在 server 级别挂载访问检测
		b.WriteString("    # 坤面板 WAF 防火墙\n    access_by_lua_file /usr/local/kunwaf/waf.lua;\n\n")
	}
	b.WriteString("    # ACME 证书签发验证\n" + challengeLoc)
	if s.AuthEnabled {
		b.WriteString(authConfLines(s))
	}
	if s.HotlinkProtect {
		b.WriteString(hotlinkConfLines(s))
	}
	if s.BrowserCache {
		b.WriteString(browserCacheConfLines())
	}
	if s.RateLimit > 0 {
		if s.RateLimit > 1024*1024 {
			s.RateLimit = 1024 * 1024
		}
		b.WriteString(fmt.Sprintf("    # 流量限制(由坤面板生成)\n    limit_rate %dk;\n\n", s.RateLimit))
	}
	switch s.Type {
	case "static":
		idx := strings.TrimSpace(s.IndexFiles)
		if idx == "" {
			idx = "index.html index.htm"
		}
		fmt.Fprintf(&b, "    root %s;\n    index %s;\n\n", root, idx)
		b.WriteString("    location / {\n        try_files $uri $uri/ =404;\n    }\n")
	case "proxy", "php":
		fmt.Fprintf(&b, "    location / {\n        proxy_pass %s;\n", func() string {
			if len(s.Upstreams) > 0 {
				return "http://kun_up_" + s.ID
			}
			return s.ProxyTo
		}())
		b.WriteString(`        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 300s;
    }
`)
	}
			if rl := rewriteConfLines(s.ID); rl != "" {
			b.WriteString(rl)
		}
	b.WriteString("}\n")

	// HTTP → HTTPS 强制跳转(独立 80 端口跳转块;仅当站点本身监听 80 且启用 HTTPS)
	// 注意:return 不能放 server 级(会抢占 ACME 挑战路径),需包在 location / 内
	if s.SslCertID != "" && s.SslRedirect && s.Port == 80 {
		b.WriteString(fmt.Sprintf(`
# HTTP 强制跳转 HTTPS(由坤面板生成)
server {
    listen 80;
    server_name %s;
%s    location / {
        return 301 https://$host$request_uri;
    }
}
`, s.Domain, challengeLoc))
	}
	return util.AtomicWrite(path, []byte(b.String()), 0o644)
}

// Reload 平滑重载 nginx(容器或宿主机)
func Reload() error {
	mode := DetectNginx()
	switch mode.Mode {
	case "container":
		out, err := exec.Command("docker", "exec", mode.Target, "nginx", "-t").CombinedOutput()
		if err != nil {
			return fmt.Errorf("配置检测失败: %s", strings.TrimSpace(string(out)))
		}
		out, err = exec.Command("docker", "exec", mode.Target, "nginx", "-s", "reload").CombinedOutput()
		if err != nil {
			return fmt.Errorf("重载失败: %s", strings.TrimSpace(string(out)))
		}
	case "host":
		out, err := exec.Command("nginx", "-t").CombinedOutput()
		if err != nil {
			return fmt.Errorf("配置检测失败: %s", strings.TrimSpace(string(out)))
		}
		out, err = exec.Command("nginx", "-s", "reload").CombinedOutput()
		if err != nil {
			return fmt.Errorf("重载失败: %s", strings.TrimSpace(string(out)))
		}
	default:
		return fmt.Errorf("未检测到可用的 Nginx/OpenResty,请先在应用商店安装 OpenResty")
	}
	return nil
}

// SetSiteSSL 绑定/解绑站点 HTTPS 证书
// certID 为空表示关闭 HTTPS;redirect 仅在绑定证书时生效
func SetSiteSSL(id, certID string, redirect bool) error {
	if certID != "" {
		if _, ok := GetCert(certID); !ok {
			return fmt.Errorf("证书不存在: %s", certID)
		}
		// 确保容器挂载了证书目录(旧实例自动补挂载并重建)
		if mode := DetectNginx(); mode.Mode == "container" {
			if err := appstore.EnsureNginxMounts(strings.TrimPrefix(mode.Target, "kun-"), io.Discard); err != nil {
				return err
			}
		}
	}
	mu.Lock()
	load()
	var site *Site
	for i := range list {
		if list[i].ID == id {
			site = &list[i]
			break
		}
	}
	if site == nil {
		mu.Unlock()
		return fmt.Errorf("站点不存在")
	}
	site.SslCertID = certID
	site.SslRedirect = redirect && certID != ""
	if err := save(); err != nil {
		// 必须落盘:否则进程重启/后续磁盘重载会把证书绑定冲掉
		mu.Unlock()
		return err
	}
	cp := *site // 解锁后写 conf 用副本,避免与并发变更竞争
	mu.Unlock()
	if err := writeConf(&cp); err != nil {
		return err
	}
	return Reload()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// PhpUpstream PHP 运行时的反代上游(http://<容器网关>:<端口>/<站点ID>/)
// PHP 容器把全部站点根目录挂载在 /var/www/html,按站点子目录区分
func PhpUpstream(siteID string) string {
	port := "8088"
	insts, _ := appstore.List()
	for _, inst := range insts {
		if inst.Key == "php" {
			if p := strings.TrimSpace(inst.Params["Port"]); p != "" {
				port = p
			}
		}
	}
	gw := "172.17.0.1"
	for _, c := range []string{"kun-openresty", "kun-nginx"} {
		if out, err := util.RunWithTimeout(5*time.Second, "docker", "exec", c, "sh", "-c", "ip route | awk '/default/{print $3}'"); err == nil && strings.TrimSpace(out) != "" {
			gw = strings.TrimSpace(out)
			break
		}
	}
	return "http://" + gw + ":" + port + "/" + siteID + "/"
}

// rewriteDir 站点伪静态/重写规则目录(每个站点一个 .conf 片段)
func rewriteDir() string { return filepath.Join(baseDir(), "rewrite") }

// RewritePath 站点重写规则文件路径(空内容表示未启用)
func RewritePath(id string) string { return filepath.Join(rewriteDir(), id+".conf") }

// GetRewrite 读取站点重写规则
func GetRewrite(id string) (string, error) {
	b, err := os.ReadFile(RewritePath(id))
	if err != nil {
		return "", nil // 未设置视为空
	}
	return string(b), nil
}

// SetRewrite 保存站点重写规则并校验+重载:
// 内容会注入 server 块内,非法指令会导致 nginx -t 失败,此时自动回滚
func SetRewrite(id, content string) error {
	mu.Lock()
	load()
	var site *Site
	for i := range list {
		if list[i].ID == id {
			site = &list[i]
			break
		}
	}
	if site == nil {
		mu.Unlock()
		return fmt.Errorf("站点不存在")
	}
	cp := *site
	mu.Unlock()
	if strings.TrimSpace(content) != "" && len(content) > 64*1024 {
		return fmt.Errorf("重写规则过大(>64KB)")
	}
	old, _ := GetRewrite(id)
	if content == old {
		return nil
	}
	if err := os.MkdirAll(rewriteDir(), 0o755); err != nil {
		return err
	}
	if err := util.AtomicWrite(RewritePath(id), []byte(content), 0o644); err != nil {
		return err
	}
	// 校验:写入失败自动回滚
	if err := writeConf(&cp); err != nil {
		_ = util.AtomicWrite(RewritePath(id), []byte(old), 0o644)
		_ = writeConf(&cp)
		return fmt.Errorf("规则校验失败,已回滚: %v", err)
	}
	if err := Reload(); err != nil {
		_ = util.AtomicWrite(RewritePath(id), []byte(old), 0o644)
		_ = writeConf(&cp)
		_ = Reload()
		return fmt.Errorf("重载失败,已回滚: %v", err)
	}
	return nil
}

// rewriteConfLines 伪静态规则注入(存在才输出),包在 server 块内
func rewriteConfLines(id string) string {
	if b, err := os.ReadFile(RewritePath(id)); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return fmt.Sprintf("    # 伪静态/重写规则(由坤面板生成)\n    %s\n", strings.TrimRight(string(b), "\n"))
	}
	return ""
}
