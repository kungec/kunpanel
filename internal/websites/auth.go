// websites/auth.go —— 站点密码访问(HTTP Basic Auth):htpasswd 生成 + 配置注入
package websites

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"kunpanel/internal/appstore"
	"kunpanel/internal/util"
)

func authDir() string { return filepath.Join(baseDir(), "auth") }

func htpasswdPath(id string) string { return filepath.Join(authDir(), id+".htpasswd") }

// htpasswdSHA 生成 nginx 支持的 {SHA} 哈希行(纯 Go,无 openssl 依赖)
func htpasswdSHA(user, password string) string {
	sum := sha1.Sum([]byte(password))
	return user + ":{SHA}" + base64.StdEncoding.EncodeToString(sum[:])
}

// validateAuthUser 用户名限制在安全字符内(写入 nginx 可读文件)
func validateAuthUser(u string) error {
	if u == "" {
		return fmt.Errorf("用户名不能为空")
	}
	if len(u) > 64 {
		return fmt.Errorf("用户名过长")
	}
	for _, r := range u {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' || r == '@'
		if !ok {
			return fmt.Errorf("用户名仅支持字母数字与 . _ - @")
		}
	}
	return nil
}

// SetSiteAuth 开启/关闭站点密码访问
// enabled=true 时 user/password 必填(password 为空表示沿用已有密码文件)
func SetSiteAuth(id string, enabled bool, user, password string) error {
	// 开启时确保容器挂载了 auth 目录(旧实例自动补挂载并重建)
	if enabled {
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
	if enabled {
		if err := validateAuthUser(user); err != nil {
			mu.Unlock()
			return err
		}
		if password == "" {
			// 未填密码:必须已有密码文件
			if _, err := os.Stat(htpasswdPath(id)); err != nil {
				mu.Unlock()
				return fmt.Errorf("请填写访问密码")
			}
		} else if len(password) < 4 {
			mu.Unlock()
			return fmt.Errorf("密码至少 4 位")
		} else {
			// 目录 0755 / 文件 0644:nginx worker(容器内非 root)需要按请求读取
			if err := os.MkdirAll(authDir(), 0o755); err != nil {
				mu.Unlock()
				return err
			}
			if err := util.AtomicWrite(htpasswdPath(id), []byte(htpasswdSHA(user, password)+"\n"), 0o644); err != nil {
				mu.Unlock()
				return err
			}
		}
		site.AuthEnabled = true
		site.AuthUser = user
	} else {
		site.AuthEnabled = false
		site.AuthUser = ""
		_ = os.Remove(htpasswdPath(id))
	}
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

// authConfLines 站点配置中的 Basic Auth 指令
func authConfLines(s *Site) string {
	if !s.AuthEnabled {
		return ""
	}
	return fmt.Sprintf("    # 密码访问(由坤面板生成)\n    auth_basic \"KunPanel Auth\";\n    auth_basic_user_file %s;\n\n", htpasswdRefPath(s.ID))
}

func htpasswdRefPath(siteID string) string {
	mode := DetectNginx()
	if mode.Mode == "container" {
		return "/etc/nginx/auth/" + siteID + ".htpasswd"
	}
	return htpasswdPath(siteID)
}

// SetSiteHotlink 设置站点防盗链
func SetSiteHotlink(id string, enabled bool, allow string) error {
	mu.Lock()
	load()
	var site *Site
	for i := range list {
		if i < len(list) && list[i].ID == id {
			site = &list[i]
			break
		}
	}
	if site == nil {
		mu.Unlock()
		return fmt.Errorf("站点不存在")
	}
	// 允许的 referer 域名:去掉控制字符与换行,空格分隔
	cleaned := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r == ',' || r == ';' {
			return ' '
		}
		return r
	}, allow)), " ")
	if len(cleaned) > 500 {
		mu.Unlock()
		return fmt.Errorf("允许域名列表过长")
	}
	site.HotlinkProtect = enabled
	site.HotlinkAllow = cleaned
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

// hotlinkConfLines 站点配置中的防盗链指令:
// 静态资源请求 Referer 不在白名单时返回 403(set+if 组合兼容所有 nginx 版本)
func hotlinkConfLines(s *Site) string {
	refs := "none blocked server_names"
	for _, d := range strings.Fields(s.HotlinkAllow) {
		refs += " *." + d + " " + d
	}
	return fmt.Sprintf(`    # 防盗链(由坤面板生成)
    valid_referers %s;
    set $kun_block_refer 0;
    if ($invalid_referer) { set $kun_block_refer 1; }
    if ($uri ~* "\.(jpg|jpeg|png|gif|webp|svg|ico|bmp|mp3|mp4|woff|woff2|ttf|eot)$") { set $kun_block_refer "${kun_block_refer}1"; }
    if ($kun_block_refer = "11") { return 403; }

`, refs)
}

// SetSiteExtras 保存 HTTPS 增强与默认文档(HTTP2/HSTS/自定义 index/静态缓存)
func SetSiteExtras(id string, http2, hsts bool, indexFiles string, browserCache bool, rateLimit int, maintenance, secHeaders bool) error {
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
	// index 只允许安全字符(字母数字/点/横杠/空格),防指令注入
	idx := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == ' ' {
			return r
		}
		return -1
	}, indexFiles)), " ")
	if len(idx) > 200 {
		mu.Unlock()
		return fmt.Errorf("默认文档列表过长")
	}
	if hsts && site.SslCertID == "" {
		hsts = false // 未启用 HTTPS 时 HSTS 无意义
	}
	site.HTTP2 = http2 && site.SslCertID != ""
	site.HSTS = hsts
	site.IndexFiles = idx
	site.BrowserCache = browserCache
	if rateLimit < 0 {
		rateLimit = 0
	}
	site.RateLimit = rateLimit
	site.Maintenance = maintenance
	site.SecHeaders = secHeaders
	if browserCache {
		if err := EnsureBrowserCacheMap(); err != nil {
			mu.Unlock()
			return err
		}
	}
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

// browserCacheConfLines 静态资源浏览器缓存(server 级 add_header + http 级 map 取值,
// 值为空时 nginx 自动省略该头,静态/PHP/反代站通通兼容)
func browserCacheConfLines() string {
	return `    # 静态资源浏览器缓存 30 天(由坤面板生成,取值见 conf.d/kuncache.conf 的 map)
    add_header Cache-Control $kun_browser_cache;
`
}

// EnsureBrowserCacheMap 写入 http 级 map(idempotent,任一站点启用缓存时调用)
func EnsureBrowserCacheMap() error {
	conf := filepath.Join(confDir(), "kuncache.conf")
	content := `# 坤面板生成:静态资源浏览器缓存映射(被各站点 add_header 引用)
map $request_uri $kun_browser_cache {
    default "";
    ~*\.(css|mjs|js|jpg|jpeg|png|gif|webp|avif|svg|ico|bmp|woff|woff2|ttf|eot|otf|mp3|mp4|webm)$ "public, max-age=2592000, immutable";
}
`
	cur, err := os.ReadFile(conf)
	if err == nil && string(cur) == content {
		return nil
	}
	if err := util.AtomicWrite(conf, []byte(content), 0o644); err != nil {
		return err
	}
	return nil
}

// SetSiteUpstreams 设置反向代理负载均衡后端(lines: 每行 "目标 [权重]"),空 = 恢复单后端
func SetSiteUpstreams(id, raw string) error {
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
	if site.Type != "proxy" && site.Type != "php" {
		mu.Unlock()
		return fmt.Errorf("仅反向代理/PHP 站点支持负载均衡")
	}
	var ups []Upstream
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r == '\r' || r == '\t' || r == ',' || r == ';' {
				return ' '
			}
			return r
		}, line))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		t := fields[0]
		if !strings.HasPrefix(t, "http://") && !strings.HasPrefix(t, "https://") {
			t = "http://" + t
		}
		w := 1
		if len(fields) > 1 {
			if n, e := strconv.Atoi(fields[1]); e == nil && n >= 1 && n <= 100 {
				w = n
			}
		}
		ups = append(ups, Upstream{Target: t, Weight: w})
	}
	if len(ups) > 16 {
		mu.Unlock()
		return fmt.Errorf("后端最多 16 个")
	}
	site.Upstreams = ups
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

// GetSiteUpstreams 读取负载均衡后端(文本格式)
func GetSiteUpstreams(id string) string {
	mu.Lock()
	load()
	var out string
	for i := range list {
		if list[i].ID == id {
			for _, u := range list[i].Upstreams {
				if u.Weight > 1 {
					out += u.Target + " " + strconv.Itoa(u.Weight) + "\n"
				} else {
					out += u.Target + "\n"
				}
			}
			break
		}
	}
	mu.Unlock()
	return out
}
