package websites

import (
	"net"
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
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
)

// domainTokenRe 严格限制域名可出现字符,阻断 Nginx 配置注入
var domainTokenRe = regexp.MustCompile(`^[a-zA-Z0-9*_.-]{1,253}$`)

// ValidateDomainsPublic 供测试与外部调用的域名校验入口
func ValidateDomainsPublic(domain string) error {
	return validateDomains(domain)
}

// ValidateWebRootPublic 供设置页校验建站根目录的公开入口
func ValidateWebRootPublic(dir string) error {
	return validateWebRoot(dir)
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
	Type        string `json:"type"` // static / php / phpfpm / proxy
	Root        string `json:"root"`
	ProxyTo     string `json:"proxy_to"`
	// 反代/PHP 站点发送给上游的 Host 头:空 = $http_host(默认),
	// 可自定义域名或变量(如 api.example.com、$host)
	ProxyHost  string `json:"proxy_host,omitempty"`
	Port       int    `json:"port"` // 监听端口,默认 80
	Enabled    bool   `json:"enabled"`
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
	PhpKey    string    `json:"php_key,omitempty"` // 使用的 PHP 实例键(php/php82...;空=默认)
	FpmPass   string    `json:"fpm_pass,omitempty"` // phpfpm 站点:fastcgi 上游(unix:... 或 host:port)
	// 子域名绑定:子域名指向站点下的子目录(如 m.example.com → example.com/m)
	Subs []SiteSub `json:"subs,omitempty"`
}

// SiteSub 子域名绑定
type SiteSub struct {
	SubDomain string `json:"sub_domain"` // 完整子域名(如 m.example.com)
	Dir       string `json:"dir"`        // 相对站点 root 的子目录(空 = 站点根)
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

// WebRoot 新建站点的默认根目录:面板设置可自定义(config.WebRoot,绝对路径),
// 空 = 面板数据目录下 roots。仅影响新建站点,已有站点不受影响
func WebRoot() string {
	if config.M != nil {
		if w := strings.TrimSpace(config.M.Get().WebRoot); w != "" && filepath.IsAbs(w) {
			return filepath.Clean(w)
		}
	}
	return rootsDir()
}

// validateWebRoot 校验自定义根目录(建站目录设置与站点自定义根目录共用):
// 绝对路径、无 ..、不落系统目录
func validateWebRoot(rd string) error {
	c := filepath.Clean(strings.TrimSpace(rd))
	if !filepath.IsAbs(c) || c == "/" || strings.Contains(rd, "..") {
		return fmt.Errorf("根目录格式不正确(需为绝对路径)")
	}
	for _, sys := range []string{"/etc", "/usr", "/boot", "/proc", "/sys", "/dev"} {
		if c == sys || strings.HasPrefix(c, sys+string(filepath.Separator)) {
			return fmt.Errorf("根目录不允许使用系统目录")
		}
	}
	return nil
}
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
	return saveChecked(false)
}

// saveChecked allowShrink=true 供用户主动的批量删除使用:防误删守卫只针对
// "任何路径的漏加载/竞态把列表静默写没",不应拦下明确传入 ID 列表的删除
func saveChecked(allowShrink bool) error {
	_ = os.MkdirAll(baseDir(), 0o755)
	// 保护:列表骤缩(丢一半以上且仍有站点)时拒写并保留现场,
	// 防止任何路径的漏加载/竞态把用户站点列表静默写没(曾发生过 9→1 的静默丢失)
	if !allowShrink && len(list) > 0 {
		if old, err := os.ReadFile(storeFile()); err == nil && len(old) > 0 {
			var prev []Site
			if json.Unmarshal(old, &prev) == nil && len(prev) > 1 && len(list)*2 < len(prev) {
				_ = util.AtomicWrite(storeFile()+".lostbak", old, 0o600)
				return fmt.Errorf("检测到站点列表从 %d 骤减到 %d,已拒写并备份原文件到 sites.json.lostbak,请反馈此提示排查", len(prev), len(list))
			}
		}
	}
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
var (
	ngDetMu      sync.Mutex
	ngDetCache   ReloadMode
	ngDetCacheAt time.Time
)

// DetectNginx 检测网关(容器/宿主机),带 5s 缓存避免高频 docker exec
func DetectNginx() ReloadMode {
	ngDetMu.Lock()
	if time.Since(ngDetCacheAt) < 5*time.Second {
		m := ngDetCache
		ngDetMu.Unlock()
		return m
	}
	ngDetMu.Unlock()
	m := detectNginxReal()
	ngDetMu.Lock()
	ngDetCache, ngDetCacheAt = m, time.Now()
	ngDetMu.Unlock()
	return m
}

func detectNginxReal() ReloadMode {
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
	Domain    string
	SiteType  string // static / php / phpfpm / proxy
	ProxyTo   string // proxy 类型必填
	ProxyHost string // 可选:发往上游的 Host 头(空 = $http_host)
	RootDir   string // 可选:自定义根目录(默认自动生成)
	Remark    string
	Port      int
	PhpKey    string // php 类型:PHP 实例键(php/php82...;空=默认)
	FpmPass   string // phpfpm 类型:fastcgi 上游(如 172.17.0.1:9072)
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
	if o.SiteType != "static" && o.SiteType != "proxy" && o.SiteType != "php" && o.SiteType != "phpfpm" {
		return nil, fmt.Errorf("不支持的站点类型")
	}
	if o.SiteType == "phpfpm" && strings.TrimSpace(o.FpmPass) == "" {
		return nil, fmt.Errorf("phpfpm 站点必须提供 fastcgi 上游")
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
	// 站点目录以主域名命名(kun.bh.cx),比数字 ID 直观;重名追加序号,
	// 非法字符转连字符,通配符剥掉 *. 前缀,极端情况回退站点 ID
	dirBase := siteDirBase(domain)
	if dirBase == "" {
		dirBase = id
	}
	webroot := WebRoot()
	usedRoots := map[string]bool{}
	for _, s0 := range list {
		if s0.Root != "" {
			usedRoots[strings.ToLower(filepath.Clean(s0.Root))] = true
		}
	}
	for i := 1; ; i++ {
		candidate := dirBase
		if i > 1 {
			candidate = fmt.Sprintf("%s-%d", dirBase, i)
		}
		full := filepath.Join(webroot, candidate)
		if !usedRoots[strings.ToLower(full)] && !dirExists(full) {
			dirBase = candidate
			break
		}
	}
	s := &Site{
		ID: id, Domain: domain, Alias: strings.Fields(domain)[0], Type: o.SiteType,
		Port: o.Port, Enabled: true,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"), Remark: o.Remark,
		Root: filepath.Join(webroot, dirBase),
	}
	if o.SiteType == "php" {
		// PHP 站点:全部请求转发到所选 PHP 运行时实例,容器内按站点目录区分;
		// 上游路径在根目录最终确定后赋值(见下方自定义根目录之后)
		s.PhpKey = normalizePHPKey(o.PhpKey)
	}
	if o.SiteType == "phpfpm" {
		// PHP-FPM 直连站点:root 保持宿主机原路径,fastcgi 直连宝塔 php-fpm
		s.Type = "phpfpm"
		s.FpmPass = strings.TrimSpace(o.FpmPass)
	}
	if o.SiteType == "proxy" {
		s.ProxyTo = strings.TrimSpace(o.ProxyTo)
		s.ProxyHost = SanitizeProxyHost(o.ProxyHost)
	}
	// 自定义根目录(仅 static/php;需绝对路径且不与系统目录冲突)
	if rd := strings.TrimSpace(o.RootDir); rd != "" && o.SiteType != "proxy" {
		if err := validateWebRoot(rd); err != nil {
			return nil, err
		}
		s.Root = filepath.Clean(rd)
	}
	if o.SiteType == "php" {
		// 根目录已最终确定:上游路径=容器内站点子目录(目录名)
		s.ProxyTo = PhpUpstreamFor(s.PhpKey, filepath.Base(s.Root))
	}
	if o.SiteType != "proxy" {
		if err := os.MkdirAll(s.Root, 0o755); err != nil {
			return nil, err
		}
		// 容器网关:站点根目录不在默认 roots 下(自定义默认目录/自定义根目录)时,
		// 以同路径挂载进容器,conf 的 root 与 ACME 挑战目录无需换算;幂等,已挂载不重建
		if mode := DetectNginx(); mode.Mode == "container" && !underRootsDir(s.Root) {
			if err := appstore.EnsureWebrootMount(strings.TrimPrefix(mode.Target, "kun-"), s.Root, io.Discard); err != nil {
				return nil, fmt.Errorf("网关容器挂载站点目录失败: %v", err)
			}
		}
		// PHP 容器同理:站点文件由它提供(DocumentRoot=/var/www/html),
		// 根目录不在默认 roots 下时必须挂进 /var/www/html/<目录名>,否则 404
		if o.SiteType == "php" {
			ensureSitePHPMount(s, io.Discard)
		}
		// 挂载变更可能重建了容器(容器 ID 变化),主动失效快照缓存
		dockerx.InvalidateDockerCache()
		index := filepath.Join(s.Root, "index.html")
		if !fileExists(index) {
			_ = util.AtomicWrite(index, []byte(fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>%s</title></head>
<body style="font-family:sans-serif;display:flex;align-items:center;justify-content:center;height:90vh">
<h1>%s 部署成功 — 由坤面板驱动</h1>
</body></html>
`, domain, domain)), 0o644)
		}
		// PHP 站点默认文件加 index.php(php:apache 容器 DirectoryIndex 默认
		// index.php 优先,phpfpm 的 index 指令也以 index.php 打头,见 writeConf)
		if o.SiteType == "php" || o.SiteType == "phpfpm" {
			phpIdx := filepath.Join(s.Root, "index.php")
			if !fileExists(phpIdx) {
				_ = util.AtomicWrite(phpIdx, []byte(fmt.Sprintf(`<?php
// 由坤面板生成,可自行替换
echo '<!DOCTYPE html><html><head><meta charset="utf-8"><title>%s</title></head>';
echo '<body style="font-family:sans-serif;display:flex;align-items:center;justify-content:center;height:90vh">';
echo '<h1>%s 部署成功 — PHP ' . PHP_VERSION . '</h1>';
echo '</body></html>';
`, domain, domain)), 0o644)
			}
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
	// 跨站点冲突检测:同端口下域名不能与其他站点重复
	newDomains := map[string]bool{}
	for _, d := range strings.Fields(domain) {
		newDomains[strings.ToLower(d)] = true
	}
	for _, other := range list {
		if other.ID == id || other.Port != site.Port {
			continue
		}
		for _, d := range strings.Fields(other.Domain) {
			if newDomains[strings.ToLower(d)] {
				mu.Unlock()
				return fmt.Errorf("域名 %s 已被站点 %s(端口 %d)占用", d, other.Alias, other.Port)
			}
		}
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
// guardSiteDirRemoval 删除站点根目录前的安全护栏:路径至少三级(/a/b/c)、
// 不得是面板站点根/默认根/数据目录本身、不得包含其他站点的根目录
func guardSiteDirRemoval(root string, remaining []Site) error {
	clean := filepath.Clean(root)
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("根目录不是绝对路径")
	}
	if strings.Count(strings.Trim(clean, string(filepath.Separator)), string(filepath.Separator)) < 2 {
		return fmt.Errorf("目录层级过浅(如 /www 或 /www/wwwroot),为安全起见不予删除,请手动清理")
	}
	for _, r := range []string{baseDir(), rootsDir(), WebRoot()} {
		if clean == filepath.Clean(r) {
			return fmt.Errorf("目录 %s 是面板站点根目录,不予删除", clean)
		}
	}
	for _, s := range remaining {
		if s.Root != "" && strings.HasPrefix(filepath.Clean(s.Root), clean+string(filepath.Separator)) {
			return fmt.Errorf("其他站点(%s)的根目录位于该目录下,为安全起见不予删除", s.Domain)
		}
	}
	return nil
}

// cleanupSiteMounts 站点目录删除后,清掉各应用 compose 里引用该目录的挂载行
// (网关/PHP 容器),有变更的容器自动重建;尽力而为,失败不影响删除流程
func cleanupSiteMounts(root string) {
	insts, err := appstore.List()
	if err != nil {
		return
	}
	for _, inst := range insts {
		if changed, err := appstore.RemoveComposeMountLine(inst.Key, root); err == nil && changed {
			_ = appstore.Apply(inst.Key, "up -d", nil)
		}
	}
}

func Delete(id string, removeFiles bool) error {
	mu.Lock()
	load()
	var kept []Site
	var target *Site
	found := false
	for i := range list {
		if list[i].ID == id {
			found = true
			target = &list[i]
			continue
		}
		kept = append(kept, list[i])
	}
	if !found {
		mu.Unlock()
		return fmt.Errorf("站点不存在")
	}
	list = kept
	if err := save(); err != nil {
		mu.Unlock()
		return err
	}
	cp := *target
	mu.Unlock()
	_ = os.Remove(filepath.Join(confDir(), id+".conf"))
	_ = os.Remove(RewritePath(id))
	_ = os.Remove(ExtraPath(id)) // 保真迁移的自定义配置一并清理,避免孤儿文件
	var dirErr error
	if removeFiles && strings.TrimSpace(cp.Root) != "" {
		// 同步删除根目录(用户勾选):带安全护栏,失败不影响站点删除本身
		if err := guardSiteDirRemoval(cp.Root, kept); err != nil {
			dirErr = fmt.Errorf("站点已删除,但根目录未删除: %v", err)
		} else if err := os.RemoveAll(cp.Root); err != nil {
			dirErr = fmt.Errorf("站点已删除,但根目录删除失败: %v", err)
		} else {
			cleanupSiteMounts(cp.Root)
			dockerx.InvalidateDockerCache()
		}
	}
	if err := Reload(); err != nil {
		return err
	}
	return dirErr
}

// DeleteMany 批量删除站点:一次保存、逐个清理产物、最后只重载一次
// (逐个调 Delete 会做 N 次完整 reload,批量时既慢又闪断)
func DeleteMany(ids []string, removeFiles bool) (int, error) {
	if len(ids) == 0 {
		return 0, fmt.Errorf("未选择站点")
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	mu.Lock()
	load()
	var kept, removed []Site
	for _, s := range list {
		if want[s.ID] {
			removed = append(removed, s)
			continue
		}
		kept = append(kept, s)
	}
	if len(removed) == 0 {
		mu.Unlock()
		return 0, fmt.Errorf("选中的站点均不存在")
	}
	orig := list
	list = kept
	if err := saveChecked(true); err != nil {
		list = orig // 落盘失败回滚内存,保持与文件一致
		mu.Unlock()
		return 0, err
	}
	for _, s := range removed {
		_ = os.Remove(filepath.Join(confDir(), s.ID+".conf"))
		_ = os.Remove(RewritePath(s.ID))
		_ = os.Remove(ExtraPath(s.ID))
	}
	mu.Unlock()
	var dirErrs []string
	if removeFiles {
		// 同步删除根目录(用户勾选):逐站安全护栏,失败逐项记录不影响其他
		for _, s := range removed {
			if strings.TrimSpace(s.Root) == "" {
				continue
			}
			if err := guardSiteDirRemoval(s.Root, kept); err != nil {
				dirErrs = append(dirErrs, s.Domain+": "+err.Error())
				continue
			}
			if err := os.RemoveAll(s.Root); err != nil {
				dirErrs = append(dirErrs, s.Domain+": "+err.Error())
				continue
			}
			cleanupSiteMounts(s.Root)
			dockerx.InvalidateDockerCache()
		}
	}
	// 配置文件已清理;重载失败如实上报(站点列表已不含被删站点)
	if err := Reload(); err != nil {
		return len(removed), err
	}
	if len(dirErrs) > 0 {
		return len(removed), fmt.Errorf("站点已删除,部分根目录未删除: %s", strings.Join(dirErrs, "; "))
	}
	return len(removed), nil
}

// UpdateSite 编辑站点(代理目标 / 端口 / 备注 / 发送域名)
func UpdateSite(id, proxyTo, remark string, port int) error {
	return UpdateSiteEx(id, proxyTo, remark, port, "")
}

// UpdateSiteEx 同 UpdateSite,额外支持自定义发送域名 Host(空 = 默认 $http_host)
func UpdateSiteEx(id, proxyTo, remark string, port int, proxyHost string) error {
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
	site.ProxyHost = SanitizeProxyHost(proxyHost)
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

// proxyHostHeader 反代/PHP 站点发往上游的 Host 头:自定义值优先,默认 $http_host
func proxyHostHeader(s *Site) string {
	if h := SanitizeProxyHost(s.ProxyHost); h != "" {
		return h
	}
	return "$http_host"
}

// SanitizeProxyHost 校验自定义 Host:仅允许域名/变量/IPv6 括号字符;
// 分号、花括号、空白会破坏 nginx conf(指令注入),出现即整体丢弃回默认
func SanitizeProxyHost(h string) string {
	h = strings.TrimSpace(h)
	if h == "" || len(h) > 200 {
		return ""
	}
	var b strings.Builder
	for _, r := range h {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '-' || r == '_' || r == '$' || r == '[' || r == ']' || r == ':'
		if !ok {
			return ""
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SetSiteType 站点类型互转(static / proxy / php)。
// → proxy 需要 target(反代目标);→ php 可选 phpKey(PHP 实例键);
// → static 沿用站点现有根目录。转换即重写配置并重载,失败不动类型。
func SetSiteType(id, newType, target, phpKey, proxyHost string) error {
	newType = strings.TrimSpace(newType)
	if newType != "static" && newType != "proxy" && newType != "php" {
		return fmt.Errorf("不支持的目标类型: %s", newType)
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
	if site.Type == newType {
		mu.Unlock()
		return fmt.Errorf("站点已经是 %s 类型", newType)
	}
	switch newType {
	case "proxy":
		target = strings.TrimSpace(target)
		if target == "" {
			mu.Unlock()
			return fmt.Errorf("代理目标不能为空")
		}
		site.ProxyTo = target
	case "php":
		site.PhpKey = normalizePHPKey(phpKey)
		site.ProxyTo = PhpUpstreamFor(site.PhpKey, filepath.Base(site.Root))
	}
	site.Type = newType
	site.ProxyHost = SanitizeProxyHost(proxyHost)
	if err := save(); err != nil {
		mu.Unlock()
		return err
	}
	cp := *site
	mu.Unlock()
	if cp.Type == "php" {
		// 转为 PHP 站点时,自定义根目录需要挂进 PHP 容器
		ensureSitePHPMount(&cp, io.Discard)
	}
	if err := writeConf(&cp); err != nil {
		return err
	}
	return Reload()
}

// siteDirBase 主域名转站点目录名:小写、剥通配符前缀、非法字符转连字符;
// 结果为空时由调用方回退站点 ID
func siteDirBase(domain string) string {
	tokens := strings.Fields(domain)
	primary := ""
	if len(tokens) > 0 {
		primary = tokens[0]
	}
	primary = strings.TrimPrefix(primary, "*.")
	primary = strings.ToLower(primary)
	var b strings.Builder
	for _, r := range primary {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ensureSitePHPMount PHP 站点根目录不在默认 roots 下时(自定义默认目录/建站
// 自定义根目录),把根目录挂进 PHP 运行时容器的 /var/www/html/<siteID>
// (php:apache 的 DocumentRoot),否则容器看不到站点文件、反代过去全是 404。
// 幂等:已挂载或实例未安装时静默跳过;默认目录站点走既有 roots:/var/www/html 挂载
func ensureSitePHPMount(s *Site, w io.Writer) {
	if s == nil || s.Type != "php" || strings.TrimSpace(s.Root) == "" {
		return
	}
	if underRootsDir(s.Root) {
		return
	}
	key := normalizePHPKey(s.PhpKey)
	if key == "" {
		key = "php"
	}
	if err := appstore.EnsureComposeMount(key, s.Root, "/var/www/html/"+filepath.Base(s.Root), w); err != nil {
		if w != nil {
			fmt.Fprintf(w, ">> 警告: 挂载站点目录到 PHP 容器失败: %v\n", err)
		}
	}
}

// EnsurePHPWebrootMounts 启动自愈:为所有根目录不在默认 roots 下的 PHP 站点
// 补挂载(升级到本版本前创建的站点在启动后自动恢复)
func EnsurePHPWebrootMounts(w io.Writer) {
	sites, err := List()
	if err != nil {
		return
	}
	for i := range sites {
		ensureSitePHPMount(&sites[i], w)
	}
}

// underRootsDir 目录是否在面板默认站点目录(roots)下:容器模式仅默认目录
// 映射为 /var/www/<id>,其余走同路径挂载
func underRootsDir(p string) bool {
	return strings.HasPrefix(filepath.Clean(p), rootsDir()+string(filepath.Separator))
}

// firstDomainIsIP 站点首个域名是否为 IP 地址(IP 证书与 default_server 判定用)
func firstDomainIsIP(domain string) bool {
	tokens := strings.Fields(domain)
	if len(tokens) == 0 {
		return false
	}
	return net.ParseIP(tokens[0]) != nil
}

// otherConfHasDefault443 其他站点 conf 是否已声明 443 default_server
// (nginx 每端口只允许一个;防恶意解析的 80 default_server 不冲突,
// IP 站点仅在 443 无人占位时接管)
func otherConfHasDefault443(selfID string) bool {
	matches, _ := filepath.Glob(filepath.Join(confDir(), "*.conf"))
	for _, f := range matches {
		if strings.EqualFold(strings.TrimSuffix(filepath.Base(f), ".conf"), selfID) {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "default_server") && strings.Contains(line, "443") {
				return true
			}
		}
	}
	return false
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
	if mode.Mode == "container" && s.Type != "phpfpm" && underRootsDir(s.Root) {
		// 默认站点目录在网关容器内挂载为 /var/www;自定义根目录走同路径
		// 挂载(见 Create 的 EnsureWebrootMount),容器内外路径保持一致
		root = "/var/www/" + filepath.Base(s.Root)
		challengeRoot = root
	}
	// 站点额外配置(保真迁移):自定义 location / include 等,原样注入 server 块;
	// 自带 location / 时不再生成默认块(否则 nginx duplicate location)
	extra := GetSiteRawExtra(s.ID)
	extraHasRoot := rawExtraHasRootLocation(extra)

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
	// IP 站点:浏览器对 IP 地址不发 SNI(RFC 6066),nginx 会落到 443 的
	// default_server 块——IP 站点必须自己当 default_server,否则出的证书和
	// 内容都是别的站点。全局面板 conf 已有 default_server 时不重复添加
	// (nginx -t 会报重复)
	ipSite := firstDomainIsIP(s.Domain)
	extraListen := ""
	if ipSite && s.SslCertID != "" && !otherConfHasDefault443(s.ID) {
		extraListen = " default_server"
	}
	b.WriteString("server {\n")
	if redirectActive && s.Port == 80 {
		b.WriteString("    listen 443 ssl" + h2 + extraListen + ";\n")
	} else {
		b.WriteString(fmt.Sprintf("    listen %d;\n", s.Port))
		if s.SslCertID != "" {
			b.WriteString("    listen 443 ssl" + h2 + extraListen + ";\n")
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
	case "phpfpm":
		// PHP-FPM(fastcgi)直连:root 用宿主机原路径(容器内路径映射由面板处理),
		// 宝塔迁移站点保持代码/目录/PHP 版本完全不变
		idx := strings.TrimSpace(s.IndexFiles)
		if idx == "" {
			idx = "index.html index.htm index.php"
		}
		hostRoot := s.Root
		fmt.Fprintf(&b, "    root %s;\n    index %s;\n\n", hostRoot, idx)
		// fastcgi 参数内联(自包含):定制镜像不含 fastcgi.conf
		b.WriteString(fmt.Sprintf(`    location ~ \.php$ {
        fastcgi_pass %s;
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param QUERY_STRING $query_string;
        fastcgi_param REQUEST_METHOD $request_method;
        fastcgi_param CONTENT_TYPE $content_type;
        fastcgi_param CONTENT_LENGTH $content_length;
        fastcgi_param REQUEST_URI $request_uri;
        fastcgi_param DOCUMENT_ROOT $document_root;
        fastcgi_param SERVER_PROTOCOL $server_protocol;
        fastcgi_param HTTPS $https if_not_empty;
    }
`, s.FpmPass))
		if !extraHasRoot {
			b.WriteString("    location / {\n        try_files $uri $uri/ =404;\n    }\n")
		}
	case "static":
		idx := strings.TrimSpace(s.IndexFiles)
		if idx == "" {
			idx = "index.html index.htm"
		}
		fmt.Fprintf(&b, "    root %s;\n    index %s;\n\n", root, idx)
		if !extraHasRoot {
			b.WriteString("    location / {\n        try_files $uri $uri/ =404;\n    }\n")
		}
	case "proxy", "php":
		if !extraHasRoot {
			fmt.Fprintf(&b, "    location / {\n        proxy_pass %s;\n", func() string {
				if len(s.Upstreams) > 0 {
					return "http://kun_up_" + s.ID
				}
				return s.ProxyTo
			}())
			b.WriteString(`        proxy_set_header Host ` + proxyHostHeader(s) + `;
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
	}
	// 站点额外配置注入(保真迁移的自定义 location / 反代子目录 / websocket 等)
	if strings.TrimSpace(extra) != "" {
		b.WriteString("    # 站点自定义配置(迁移保真,面板原样注入)\n")
		b.WriteString(extra)
		if !strings.HasSuffix(extra, "\n") {
			b.WriteString("\n")
		}
	}
	if rl := rewriteConfLines(s.ID); rl != "" {
		b.WriteString(rl)
	}
	b.WriteString("}\n")

	// 子域名绑定:每个绑定生成独立 server 块(共享主站 root 与安全特性)
	if len(s.Subs) > 0 {
		subRoot := s.Root
		if mode.Mode == "container" && s.Type != "phpfpm" && underRootsDir(s.Root) {
			subRoot = "/var/www/" + filepath.Base(s.Root)
		}
		for _, sub := range s.Subs {
			dir := subRoot
			if sub.Dir != "" {
				dir = strings.TrimSuffix(subRoot, "/") + "/" + sub.Dir
			}
			b.WriteString(fmt.Sprintf("\n# 子域名绑定 %s (由坤面板生成)\nserver {\n", sub.SubDomain))
			b.WriteString(fmt.Sprintf("    listen %d;\n    server_name %s;\n", s.Port, sub.SubDomain))
			if s.SslCertID != "" {
				if crt, key, err := CertFilePaths(s.SslCertID); err == nil {
					b.WriteString(fmt.Sprintf("    ssl_certificate %s;\n    ssl_certificate_key %s;\n", crt, key))
				}
			}
			b.WriteString("    # ACME 证书签发验证\n    location ^~ /.well-known/acme-challenge/ {\n        root " + challengeRoot + ";\n    }\n")
			if wafOn() {
				b.WriteString("    access_by_lua_file /usr/local/kunwaf/waf.lua;\n")
			}
			fmt.Fprintf(&b, "    access_log /var/log/nginx/%s.access.log;\n", safeName+"."+strings.Split(sub.SubDomain, ".")[0])
			idx := strings.TrimSpace(s.IndexFiles)
			if idx == "" {
				// phpfpm 站点的子域名块同样以 index.php 打头(与主块一致)
				idx = "index.php index.html index.htm"
			}
			b.WriteString(fmt.Sprintf("    root %s;\n    index %s;\n\n", dir, idx))
			switch s.Type {
			case "php":
				b.WriteString(fmt.Sprintf("    location / {\n        proxy_pass %s;\n", strings.TrimSuffix(s.ProxyTo, "/") + "/" + sub.Dir + "/"))
				b.WriteString(`        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_http_version 1.1;
    }
`)
			case "phpfpm":
				b.WriteString(fmt.Sprintf(`    location ~ \.php$ {
        fastcgi_pass %s;
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param QUERY_STRING $query_string;
        fastcgi_param REQUEST_METHOD $request_method;
        fastcgi_param CONTENT_TYPE $content_type;
        fastcgi_param CONTENT_LENGTH $content_length;
    }
`, s.FpmPass))
				b.WriteString("    location / {\n        try_files $uri $uri/ =404;\n    }\n")
			default:
				b.WriteString("    location / {\n        try_files $uri $uri/ =404;\n    }\n")
			}
			b.WriteString("}\n")
		}
	}

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
	return PhpUpstreamFor("", siteID)
}

// PhpUpstreamFor 指定 PHP 实例键的反代上游;phpKey 为空时按顺序取:php → 第一个 php 系实例
func PhpUpstreamFor(phpKey, siteID string) string {
	phpKey = normalizePHPKey(phpKey)
	port := ""
	insts, _ := appstore.List()
	// 目标实例
	for _, inst := range insts {
		if inst.Key == phpKey && phpKey != "" {
			if p := strings.TrimSpace(inst.Params["Port"]); p != "" {
				port = p
			}
			break
		}
	}
	// 未指定/未找到:php 主实例 → 第一个运行中的 php 系实例
	if port == "" {
		for _, inst := range insts {
			if inst.Key == "php" {
				if p := strings.TrimSpace(inst.Params["Port"]); p != "" {
					port = p
				}
				break
			}
		}
	}
	if port == "" {
		for _, inst := range insts {
			if appstore.IsPHPInstanceKeyExported(inst.Key) {
				if p := strings.TrimSpace(inst.Params["Port"]); p != "" {
					port = p
				}
				break
			}
		}
	}
	if port == "" {
		port = "8088"
	}
	gw := dockerBridgeGateway()
	return "http://" + gw + ":" + port + "/" + siteID + "/"
}

// normalizePHPKey 归一 PHP 实例键
func normalizePHPKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" || k == "phpmyadmin" {
		return ""
	}
	return k
}

// dockerBridgeGateway 获取 docker 默认网桥的宿主机网关 IP。
// 首选宿主机侧 `docker network inspect`(不依赖容器内工具链);
// 容器内 `ip route` 探测作兜底——但必须校验输出是合法 IPv4:
// 容器可能没有 ip 命令,sh 的报错文本("sh: 1: ip: not found")会进 CombinedOutput
// 且 awk 退出码为 0,不校验就会把错误串当网关写进 proxy_pass(已发生的线上 bug)。
func dockerBridgeGateway() string {
	if out, err := util.RunWithTimeout(5*time.Second, "docker", "network", "inspect", "bridge",
		"--format", "{{(index .IPAM.Config 0).Gateway}}"); err == nil {
		if g := strings.TrimSpace(out); net.ParseIP(g) != nil {
			return g
		}
	}
	for _, c := range []string{"kun-openresty", "kun-nginx"} {
		if out, err := util.RunWithTimeout(5*time.Second, "docker", "exec", c, "sh", "-c", "ip route | awk '/default/{print $3}'"); err == nil {
			if g := strings.TrimSpace(out); net.ParseIP(g) != nil {
				return g
			}
		}
	}
	return "172.17.0.1"
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

// RefreshSiteConf 按当前存储状态重写指定站点配置(不触发重载)。
// 供批量迁移场景使用:extras/伪静态落盘后统一由调用方重载
func RefreshSiteConf(id string) error {
	mu.Lock()
	load()
	var site *Site
	for i := range list {
		if list[i].ID == id {
			cp := list[i]
			site = &cp
			break
		}
	}
	if site == nil {
		mu.Unlock()
		return fmt.Errorf("站点不存在")
	}
	mu.Unlock()
	return writeConf(site)
}

// ExtraPath 站点原始额外配置文件路径(保真迁移用:自定义 location/include 等,
// 原样注入生成的 server 块;与 rewrite 分离,避免面板重新生成时丢失)
func ExtraPath(id string) string { return filepath.Join(extrasDir(), id+".conf") }

func extrasDir() string { return filepath.Join(baseDir(), "extras") }

// GetSiteRawExtra 读取站点额外配置(空=未设置)
func GetSiteRawExtra(id string) string {
	if !idPattern2.MatchString(id) {
		return ""
	}
	b, err := os.ReadFile(ExtraPath(id))
	if err != nil {
		return ""
	}
	return string(b)
}

// SetSiteRawExtra 保存站点额外配置(空内容=删除);只写文件不重载,
// 调用方(writeConf → Reload)负责校验生效
func SetSiteRawExtra(id, content string) error {
	if !idPattern2.MatchString(id) {
		return fmt.Errorf("非法站点标识")
	}
	if strings.TrimSpace(content) == "" {
		_ = os.Remove(ExtraPath(id))
		return nil
	}
	if err := os.MkdirAll(extrasDir(), 0o755); err != nil {
		return err
	}
	return util.AtomicWriteString(ExtraPath(id), content, 0o644)
}

// rawExtraHasRootLocation 额外配置是否自带 location / 块
// (自带时 writeConf 不再生成默认 location /,避免 nginx duplicate location)
func rawExtraHasRootLocation(extra string) bool {
	return regexp.MustCompile(`(?m)^\s*location\s+/\s*\{`).MatchString(extra)
}

var idPattern2 = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

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

// UpdateSitePhp 切换 PHP 站点使用的 PHP 实例(多版本):更新 PhpKey、重写 conf 并重载
func UpdateSitePhp(id, phpKey string) error {
	phpKey = normalizePHPKey(phpKey)
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
	if site.Type != "php" {
		mu.Unlock()
		return fmt.Errorf("仅 PHP 站点支持切换 PHP 版本")
	}
	if site.PhpKey == phpKey {
		mu.Unlock()
		return nil // 无变化
	}
	site.PhpKey = phpKey
	site.ProxyTo = PhpUpstreamFor(phpKey, filepath.Base(site.Root))
	if err := save(); err != nil {
		mu.Unlock()
		return err
	}
	cp := *site
	mu.Unlock()
	// 切换 PHP 实例后,自定义根目录要挂到新实例的容器(kun-<phpKey>)
	ensureSitePHPMount(&cp, io.Discard)
	if err := writeConf(&cp); err != nil {
		return err
	}
	return Reload()
}

// PHPSitesUsing 返回正在使用指定 PHP 实例键的站点域名列表(卸载保护)
func PHPSitesUsing(phpKey string) []string {
	mu.Lock()
	defer mu.Unlock()
	load()
	var out []string
	for _, s := range list {
		if s.Type != "php" {
			continue
		}
		k := normalizePHPKey(s.PhpKey)
		if k == "" {
			k = "php"
		}
		if k == phpKey {
			out = append(out, s.Alias)
		}
	}
	return out
}

// SetSiteSubs 保存站点子域名绑定并重写配置
// subDomainRe 子域名严格白名单(字母/数字/点/连字符,不带协议/路径/注释符)
var subDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func SetSiteSubs(id string, subs []SiteSub) error {
	clean := make([]SiteSub, 0, len(subs))
	seen := map[string]bool{}
	for _, sub := range subs {
		sub.SubDomain = strings.ToLower(strings.TrimSpace(sub.SubDomain))
		sub.Dir = strings.Trim(strings.TrimSpace(sub.Dir), "/")
		if sub.SubDomain == "" {
			continue
		}
		// 严格白名单:仅字母/数字/点/连字符(换行、引号、#、空格等都会写进 nginx conf)
		if !subDomainRe.MatchString(sub.SubDomain) || len(sub.SubDomain) > 253 {
			return fmt.Errorf("子域名不合法(仅支持字母/数字/点/连字符): %s", sub.SubDomain)
		}
		if strings.Contains(sub.Dir, "..") {
			return fmt.Errorf("子目录不合法: %s", sub.Dir)
		}
		key := sub.SubDomain
		if seen[key] {
			return fmt.Errorf("子域名重复: %s", key)
		}
		seen[key] = true
		clean = append(clean, sub)
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
	oldSubs := site.Subs
	site.Subs = clean
	if err := save(); err != nil {
		mu.Unlock()
		return err
	}
	cp := *site
	mu.Unlock()
	// 默认创建子目录与欢迎页:目录不存在时绑定的子域名打开就是 404
	// (站点根目录对网关/PHP 容器均可见,宿主机建目录即可)
	if cp.Type != "proxy" && strings.TrimSpace(cp.Root) != "" {
		for _, sub := range clean {
			if sub.Dir == "" {
				continue
			}
			dir := filepath.Join(cp.Root, sub.Dir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("创建子目录失败: %v", err)
			}
			idx := filepath.Join(dir, "index.html")
			if !fileExists(idx) {
				_ = util.AtomicWrite(idx, []byte(fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>%s</title></head>
<body style="font-family:sans-serif;display:flex;align-items:center;justify-content:center;height:90vh">
<h1>%s 部署成功 — 由坤面板驱动</h1>
</body></html>
`, sub.SubDomain, sub.SubDomain)), 0o644)
			}
			if cp.Type == "php" || cp.Type == "phpfpm" {
				phpIdx := filepath.Join(dir, "index.php")
				if !fileExists(phpIdx) {
					_ = util.AtomicWrite(phpIdx, []byte(fmt.Sprintf(`<?php
// 由坤面板生成,可自行替换
echo '<!DOCTYPE html><html><head><meta charset="utf-8"><title>%s</title></head>';
echo '<body style="font-family:sans-serif;display:flex;align-items:center;justify-content:center;height:90vh">';
echo '<h1>%s 部署成功 — PHP ' . PHP_VERSION . '</h1>';
echo '</body></html>';
`, sub.SubDomain, sub.SubDomain)), 0o644)
				}
			}
		}
	}
	if err := writeConf(&cp); err != nil {
		return err
	}
	if err := Reload(); err != nil {
		// 回滚:恢复原子域名列表并重写配置,保持存储与生效配置一致
		mu.Lock()
		load()
		var rb *Site
		for i := range list {
			if list[i].ID == id {
				list[i].Subs = oldSubs
				cp := list[i]
				rb = &cp
				break
			}
		}
		if rb != nil {
			_ = save() // 持锁落盘
		}
		mu.Unlock()
		if rb != nil {
			_ = writeConf(rb)
		}
		return fmt.Errorf("配置校验失败已回滚: %v", err)
	}
	return nil
}

// AppendRemark 追加站点备注(保留原内容)
func AppendRemark(id, extra string) error {
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
	site.Remark = strings.TrimSpace(strings.TrimSpace(site.Remark) + "; " + extra)
	err := save()
	mu.Unlock()
	return err
}
