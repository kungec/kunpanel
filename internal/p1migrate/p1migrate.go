// p1migrate —— 1Panel 面板一键迁移(双向互通)。
//
// 与 btmigrate(宝塔)同思路,适配 1Panel 的结构差异:
//   - 1Panel 的网站全部由其 OpenResty 容器承载:conf.d/<域名>.conf + www/sites/<别名>/index
//   - 静态站:root 指向 sites 目录;反代站:proxy_pass;PHP 站:fastcgi_pass 到
//     PHP 运行时(TCP 端口或 unix socket)
//   - 数据库:1Panel 自己的 MySQL 容器,root 密码可从容器环境变量读取
//   - SSL:www/ssl/<域名>/fullchain.pem + privkey.pem
//
// 迁移语义与宝塔版一致:不搬不动 1Panel 原站点,可随时回退;PHP(unix socket)
// 上游无法跨容器直连时保留原样并明确告警。
package p1migrate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

const p1Base = "/opt/1panel"

// P1Site 1Panel 站点(由 OpenResty conf 解析)
type P1Site struct {
	Conf     string `json:"conf"`
	Domain   string `json:"domain"`
	Aliases  string `json:"aliases"`
	Root     string `json:"root"`
	Type     string `json:"type"` // static / proxy / php
	ProxyTo  string `json:"proxy_to,omitempty"`
	FpmAddr  string `json:"fpm_addr,omitempty"`
	FpmUnix  bool   `json:"fpm_unix,omitempty"` // PHP 运行时为 unix socket(跨容器不可直连)
	SSL      bool   `json:"ssl"`
	CertDir  string `json:"cert_dir,omitempty"`
	Warnings string `json:"warnings,omitempty"`
}

// P1DB 1Panel 数据库
type P1DB struct {
	Name     string `json:"name"`
	User     string `json:"user"`
	Host     string `json:"host"` // 1Panel 记录的数据库容器名(如 mysql)
	Type     string `json:"type"` // mysql / postgresql
	Instance string `json:"instance"`
}

// P1ScanResult 扫描结果
type P1ScanResult struct {
	Found    bool      `json:"found"`
	ConfDir  string    `json:"conf_dir"`
	Sites    []P1Site  `json:"sites"`
	DBs      []P1DB    `json:"dbs"`
	Warnings []string  `json:"warnings"`
	Scanned  time.Time `json:"scanned"`
}

// ---------- 路径探测(v1/v2 兼容) ----------

func p1DBFile() string {
	for _, p := range []string{"/opt/1panel/db/1Panel.db", "/opt/1panel/db/core.db"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func p1ConfDir() string {
	for _, p := range []string{
		"/opt/1panel/apps/openresty/openresty/conf.d",
		"/opt/1panel/apps/openresty/openresty/www/conf.d",
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

func p1WwwDir() string {
	for _, p := range []string{
		"/opt/1panel/apps/openresty/openresty/www",
		"/opt/1panel/apps/openresty/openresty/www/sites",
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() && !strings.HasSuffix(p, "/sites") {
			return p
		}
	}
	return ""
}

// P1Installed 是否检测到 1Panel
func P1Installed() bool {
	return p1DBFile() != "" && p1ConfDir() != ""
}

// pythonJSONLite 系统级 python3 读 1Panel sqlite(1Panel 机器无宝塔 pyenv 可借,
// 仅依赖系统 python3;缺失时报错由调用方透出)
func pythonJSONLite(script string) (string, error) {
	out, err := util.RunWithTimeout(15*time.Second, "python3", "-c", script)
	if err != nil {
		return "", fmt.Errorf("系统 python3 不可用: %v", err)
	}
	return strings.TrimSpace(out), nil
}

// ---------- 扫描 ----------

var (
	reServerName = regexp.MustCompile(`server_name\s+([^;]+);`)
	reRoot       = regexp.MustCompile(`root\s+([^;]+);`)
	reSSLListen  = regexp.MustCompile(`listen\s+[^;]*443[^;]*ssl`)
	reSSLCrt     = regexp.MustCompile(`ssl_certificate\s+([^;]+?)(?:fullchain\.pem|fullchain\.pem[^;/]*|\.pem)\s*;`)
	reProxyPass  = regexp.MustCompile(`proxy_pass\s+(https?://[^;]+);`)
	reFpmPass    = regexp.MustCompile(`fastcgi_pass\s+([^;]+);`)
)

// P1Scan 扫描 1Panel 环境
func P1Scan() (*P1ScanResult, error) {
	res := &P1ScanResult{Sites: []P1Site{}, DBs: []P1DB{}, Warnings: []string{}, Scanned: time.Now()}
	if !P1Installed() {
		return res, nil
	}
	res.Found = true
	res.ConfDir = p1ConfDir()

	// 站点:解析 conf.d 下每个 <域名>.conf(1Panel 一个站点一个文件,
	// 内含 80 与 443 两个 server 块,取并集)
	entries, err := os.ReadDir(res.ConfDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			path := filepath.Join(res.ConfDir, e.Name())
			site, w := parseP1SiteConf(path)
			if site == nil {
				continue // 纯 include/碎片文件
			}
			if w != "" {
				res.Warnings = append(res.Warnings, "站点 "+site.Domain+": "+w)
			}
			if site.Domain != "" {
				res.Sites = append(res.Sites, *site)
			}
		}
	}

	// 数据库清单(1Panel v1 表 databases / v2 表 database,列名随版本漂移,
	// PRAGMA 探测后取交集)
	dbFile := p1DBFile()
	if dbFile == "" {
		res.Warnings = append(res.Warnings, "未找到 1Panel 数据库文件,数据库清单为空")
	} else {
		script := "import sqlite3,json\n" +
			"c=sqlite3.connect('" + dbFile + "')\n" +
			"tabs={r[0] for r in c.execute(\"select name from sqlite_master where type='table'\")}\n" +
			"tab='databases' if 'databases' in tabs else ('database' if 'database' in tabs else '')\n" +
			"if not tab:\n" +
			"    print(json.dumps([]))\n" +
			"else:\n" +
			"    cols={r[1] for r in c.execute('PRAGMA table_info('+tab+')')}\n" +
			"    name='databasename' if 'databasename' in cols else ('name' if 'name' in cols else '')\n" +
			"    user='username' if 'username' in cols else 'user'\n" +
			"    host='host' if 'host' in cols else 'server'\n" +
			"    if not name:\n" +
			"        print(json.dumps([]))\n" +
			"    else:\n" +
			"        rows=[dict(name=r[0],user=r[1],host=r[2],type=r[3]) for r in c.execute('select '+name+','+user+','+host+',type from '+tab)]\n" +
			"        print(json.dumps(rows))\n"
		out, err := pythonJSONLite(script)
		if err != nil {
			res.Warnings = append(res.Warnings, "读取 1Panel 数据库清单失败: "+err.Error())
		} else {
			var dbs []P1DB
			if json.Unmarshal([]byte(out), &dbs) != nil {
				res.Warnings = append(res.Warnings, "解析 1Panel 数据库清单失败")
			} else {
				for _, d := range dbs {
					if strings.EqualFold(d.Type, "mysql") {
						d.Type = "mysql"
						res.DBs = append(res.DBs, d)
					}
				}
				if len(dbs) != len(res.DBs) {
					res.Warnings = append(res.Warnings, "非 MySQL 数据库(PostgreSQL 等)暂不支持自动迁移,已从清单省略")
				}
			}
		}
	}

	if len(res.Sites) == 0 && len(res.DBs) == 0 {
		res.Warnings = append(res.Warnings, "未解析到任何站点/数据库:1Panel v2 结构若持续变化,请反馈扫描输出")
	}
	return res, nil
}

// parseP1SiteConf 解析一个 1Panel 站点 conf(全部 server 块取并集)。
// 无法识别站点身份(无 server_name)时返回 nil。
// 注意:不能用 strings.Split(content, "server")——会把 server_name 切碎。
func parseP1SiteConf(path string) (*P1Site, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}
	content := string(b)
	site := &P1Site{Conf: path}
	warns := []string{}
	parsed := false
	// 索引扫描所有 server 块
	pos := 0
	for {
		idx := strings.Index(content[pos:], "server")
		if idx < 0 {
			break
		}
		idx += pos
		pos = idx + len("server")
		rest := strings.TrimLeft(content[pos:], " \t\r\n")
		if !strings.HasPrefix(rest, "{") {
			continue // server_name 等以 server 开头的指令,跳过
		}
		// 花括号配对取块体
		brace := strings.Index(rest, "{")
		depth := 0
		end := -1
		for i := brace; i < len(rest); i++ {
			switch rest[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		if end < 0 {
			end = len(rest)
		}
		blk := rest[brace+1 : end]
		if m := reServerName.FindStringSubmatch(blk); m != nil {
			site.Aliases = strings.TrimSpace(m[1])
			f := strings.Fields(site.Aliases)
			if len(f) > 0 && site.Domain == "" {
				site.Domain = f[0]
			}
			parsed = true
		}
		if reSSLListen.MatchString(blk) {
			site.SSL = true
		}
		if m := reSSLCrt.FindStringSubmatch(blk); m != nil && site.CertDir == "" {
			site.CertDir = filepath.Dir(strings.TrimSpace(m[1]))
		}
		if m := reRoot.FindStringSubmatch(blk); m != nil && site.Root == "" {
			site.Root = strings.TrimSpace(m[1])
		}
		if m := reProxyPass.FindStringSubmatch(blk); m != nil && site.ProxyTo == "" {
			site.ProxyTo = strings.TrimSpace(m[1])
		}
		if m := reFpmPass.FindStringSubmatch(blk); m != nil && site.FpmAddr == "" {
			addr := strings.TrimSpace(m[1])
			site.FpmAddr = addr
			site.FpmUnix = strings.HasPrefix(addr, "unix:")
		}
	}
	if !parsed || site.Domain == "" {
		return nil, ""
	}
	switch {
	case site.FpmAddr != "":
		site.Type = "php"
		if site.FpmUnix {
			warns = append(warns, "PHP 运行时为 unix socket,跨容器不可直连,迁移后 PHP 请求将 502;请在 1Panel 侧将 PHP 运行时改为监听 TCP 端口后再迁移,或迁移后在本面板改用容器 PHP 运行时")
		}
	case site.ProxyTo != "":
		site.Type = "proxy"
	default:
		site.Type = "static"
	}
	if site.Root == "" && site.Type == "static" {
		site.Root = filepath.Join(p1WwwDir(), "sites", site.Domain, "index")
	}
	return site, strings.Join(warns, ";")
}

// ---------- 通用小件 ----------

// p1BridgeGateway docker0 网关(与 btmigrate.bridgeGateway 同义,独立实现避免跨包耦合)
func p1BridgeGateway() string {
	if out, err := util.RunWithTimeout(8*time.Second, "docker", "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}"); err == nil {
		if g := strings.TrimSpace(out); g != "" && !strings.Contains(g, " ") {
			return g
		}
	}
	return "172.17.0.1"
}

var reP1Loopback = regexp.MustCompile(`(https?://)(127\.0\.0\.1|\[::1\]|localhost)([:\s;/]|$)`)

func p1RewriteLoopback(s string) string {
	gw := p1BridgeGateway()
	return reP1Loopback.ReplaceAllString(s, "${1}"+gw+"${3}")
}

// P1RewriteProxyTarget 反代目标改写(回环 → 网桥),返回改写后的目标
func P1RewriteProxyTarget(target string) string {
	return p1RewriteLoopback(target)
}

// P1FpmRewrite fastcgi 上游改写:回环 TCP → 网桥;unix socket 原样保留
func P1FpmRewrite(addr string) string {
	if strings.HasPrefix(addr, "unix:") {
		return addr
	}
	return reP1Loopback.ReplaceAllString(addr, "${1}"+p1BridgeGateway()+"${3}")
}

// EnsureP1SiteMount 网关容器挂载 1Panel 的 www 目录(静态站 root 保持原路径),
// 模式与宝塔版 EnsureBtRootMount 一致:compose volumes 追加 + 重建。
// 只挂载当前运行中的面板网关(未运行的 openresty 应用重建可能抢 80/443)
func EnsureP1SiteMount(w io.Writer, wwwDir string) error {
	if wwwDir == "" {
		return fmt.Errorf("未探测到 1Panel www 目录,无法挂载静态站点根")
	}
	mode := websites.DetectNginx()
	if mode.Mode != "container" || mode.Target == "" {
		return fmt.Errorf("未检测到运行中的面板网关容器,请先在应用商店安装并启动 Nginx/OpenResty")
	}
	key := strings.TrimPrefix(mode.Target, "kun-")
	dir := GatewayComposeDir(mode.Target)
	composeFile := filepath.Join(dir, "docker-compose.yml")
	b, err := os.ReadFile(composeFile)
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %v", composeFile, err)
	}
	content := string(b)
	if strings.Contains(content, ":"+wwwDir) {
		return nil // 已挂载
	}
	idx := strings.Index(content, "volumes:")
	if idx < 0 {
		return fmt.Errorf("[%s] compose 无 volumes 段", key)
	}
	lineEnd := strings.Index(content[idx:], "\n")
	content = content[:idx+lineEnd] + "\n      - " + wwwDir + ":" + wwwDir + content[idx+lineEnd:]
	if err := util.AtomicWriteString(composeFile, content, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "  [%s] 追加挂载 %s,重建容器 ...\n", key, wwwDir)
	out, err := dockerx.Compose(dir, "up", "-d")
	fmt.Fprintln(w, out)
	if err != nil {
		return fmt.Errorf("[%s] 挂载重建失败: %s", key, tailOf(out, 200))
	}
	for i := 0; i < 20; i++ {
		if dockerx.ContainerRunning(mode.Target) {
			break
		}
		time.Sleep(time.Second)
	}
	return nil
}

// GatewayComposeDir 面板网关应用 compose 目录(容器名 kun-nginx 与安装 key
// nginx 差前缀;新版装在 /opt/kunpanel/apps、旧版在数据目录——组合探测)
func GatewayComposeDir(container string) string {
	keys := []string{container, strings.TrimPrefix(container, "kun-")}
	var first string
	for _, key := range keys {
		for _, base := range []string{"/opt/kunpanel/apps", filepath.Join(util.DataDir(), "apps")} {
			p := filepath.Join(base, key)
			if first == "" {
				first = p
			}
			if _, err := os.Stat(filepath.Join(p, "docker-compose.yml")); err == nil {
				return p
			}
		}
	}
	return first
}

// tailOf 取字符串尾部(错误输出截断)
func tailOf(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// validateP1Target 允许的站点根目录:必须为绝对路径且不落在系统敏感目录
func validateP1Root(root string) error {
	if !strings.HasPrefix(root, "/") {
		return fmt.Errorf("根目录必须为绝对路径")
	}
	for _, bad := range []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/proc", "/sys", "/dev"} {
		if root == bad || strings.HasPrefix(root, bad+"/") {
			return fmt.Errorf("根目录 %s 位于系统目录,拒绝迁移", root)
		}
	}
	return nil
}

