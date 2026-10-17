// p1reverse —— 坤面板 → 1Panel 反向导出。
//
// 把面板站点(静态/PHP/反代)导出为 1Panel OpenResty 可直接生效的形态:
//   - 站点文件拷贝到 1Panel www/sites/<别名>/index
//   - 证书拷贝到 www/ssl/<域名>/
//   - 生成 nginx conf 到 conf.d/<域名>.conf
//   - 重载 1Panel OpenResty 容器
//
// 注意:这些站点由外部写入,1Panel 面板界面不会列出(功能不受影响);
// 1Panel 自身网站配置存于其数据库,不做逆向写入。
package p1migrate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

// P1ReversePlan 反向导出预检
type P1ReversePlan struct {
	P1Ready    bool            `json:"p1_ready"`
	ConfDir    string          `json:"conf_dir"`
	WwwDir     string          `json:"www_dir"`
	GW         string          `json:"gw"` // 1Panel OpenResty 容器名
	Sites      []websites.Site `json:"sites"`
	Warnings   []string        `json:"warnings"`
}

// P1ReverseScan 预检
func P1ReverseScan() (*P1ReversePlan, error) {
	p := &P1ReversePlan{Sites: []websites.Site{}, Warnings: []string{}}
	if !P1Installed() {
		p.Warnings = append(p.Warnings, "未检测到 1Panel("+p1Base+")")
		return p, nil
	}
	p.P1Ready = true
	p.ConfDir = p1ConfDir()
	p.WwwDir = p1WwwDir()
	p.GW = p1OpenRestyContainer()
	if p.GW == "" {
		p.Warnings = append(p.Warnings, "未检测到运行中的 1Panel OpenResty 容器,导出后将无法自动重载(1Panel 重载后生效)")
	}
	if list, err := websites.List(); err == nil {
		p.Sites = list
	}
	return p, nil
}

// P1ReverseApply 执行导出
func P1ReverseApply(opts P1ReverseOptions, w io.Writer) error {
	if !P1Installed() {
		return fmt.Errorf("未检测到 1Panel(%s)", p1Base)
	}
	confDir := p1ConfDir()
	wwwDir := p1WwwDir()
	if confDir == "" || wwwDir == "" {
		return fmt.Errorf("未找到 1Panel OpenResty 目录结构")
	}
	sel := map[string]bool{}
	for _, id := range opts.Sites {
		sel[id] = true
	}
	all, err := websites.List()
	if err != nil {
		return err
	}
	exported := 0
	for _, s := range all {
		if !sel[s.ID] {
			continue
		}
		fmt.Fprintf(w, ">> 导出站点 %s(%s)到 1Panel ...\n", s.Domain, s.Type)
		if err := exportSiteToP1(s, confDir, wwwDir, w); err != nil {
			fmt.Fprintf(w, "   失败: %v(继续下一个)\n", err)
			continue
		}
		exported++
	}
	// 重载 1Panel OpenResty
	if gw := p1OpenRestyContainer(); gw != "" {
		if out, err := dockerx.ExecIn(gw, "nginx", "-t"); err != nil {
			fmt.Fprintf(w, ">> 注意:1Panel OpenResty 配置校验未通过: %s\n", tailOf(out, 300))
		} else {
			_, _ = dockerx.ExecIn(gw, "nginx", "-s", "reload")
			fmt.Fprintf(w, ">> 1Panel OpenResty 已重载,导出完成:站点 %d 个\n", exported)
			return nil
		}
	}
	fmt.Fprintf(w, ">> 导出完成:站点 %d 个(1Panel 重载 OpenResty 后生效)\n", exported)
	return nil
}

// P1ReverseOptions 反向导出选项
type P1ReverseOptions struct {
	Sites []string // 面板站点 ID
}

// exportSiteToP1 单站点导出:目录/证书/conf
func exportSiteToP1(s websites.Site, confDir, wwwDir string, w io.Writer) error {
	alias := s.Alias
	if alias == "" {
		alias = strings.Fields(s.Domain)[0]
	}
	safe := sanitizeIdent1(alias)
	siteDir := filepath.Join(wwwDir, "sites", safe, "index")
	// 1) 站点目录(proxy 站点建空目录占位)
	if s.Type != "proxy" {
		if s.Root != "" {
			if _, err := os.Stat(s.Root); err == nil {
				fmt.Fprintf(w, "   复制站点目录 %s → %s ...\n", s.Root, siteDir)
				if out, err := util.Run("sh", "-c", fmt.Sprintf("mkdir -p %q && cp -a %q/. %q/", siteDir, s.Root, siteDir)); err != nil {
					return fmt.Errorf("复制站点目录失败: %v: %s", err, tailOf(out, 150))
				}
			} else {
				_ = os.MkdirAll(siteDir, 0o755)
			}
		} else {
			_ = os.MkdirAll(siteDir, 0o755)
		}
	} else {
		_ = os.MkdirAll(siteDir, 0o755)
	}
	// 2) 证书
	sslLines := ""
	if s.SslCertID != "" {
		if crt, key, err := websites.CertFilePaths(s.SslCertID); err == nil && crt != "" {
			certDir := filepath.Join(wwwDir, "ssl", s.Domain)
			_ = os.MkdirAll(certDir, 0o755)
			if b, e := os.ReadFile(crt); e == nil {
				_ = os.WriteFile(filepath.Join(certDir, "fullchain.pem"), b, 0o644)
			}
			if b, e := os.ReadFile(key); e == nil {
				_ = os.WriteFile(filepath.Join(certDir, "privkey.pem"), b, 0o600)
			}
			sslLines = fmt.Sprintf("    listen 443 ssl;\n    ssl_certificate %s;\n    ssl_certificate_key %s;\n    ssl_protocols TLSv1.2 TLSv1.3;\n",
				filepath.Join(certDir, "fullchain.pem"), filepath.Join(certDir, "privkey.pem"))
		}
	}
	// 3) conf(1Panel 网关容器可直接挂载读取的独立 server 块)
	var b strings.Builder
	b.WriteString("server {\n    listen 80;\n    server_name " + s.Domain + ";\n")
	b.WriteString(sslLines)
	b.WriteString("    access_log /var/log/nginx/access.log;\n")
	switch s.Type {
	case "proxy", "php":
		// 面板 PHP 站点 = 反代到容器 PHP 运行时(上游已在 proxy_to 里,
		// 1Panel 网关经网桥可达面板已发布端口)
		fmt.Fprintf(&b, `    location / {
        proxy_pass %s;
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_http_version 1.1;
    }
`, s.ProxyTo)
	default:
		fmt.Fprintf(&b, "    root %s;\n    index index.html index.htm;\n    location / {\n        try_files $uri $uri/ =404;\n    }\n", siteDir)
	}
	b.WriteString("}\n")
	_ = os.MkdirAll(confDir, 0o755)
	confPath := filepath.Join(confDir, "kunpanel-"+safe+".conf")
	if err := os.WriteFile(confPath, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "   已生成 %s(目录/证书)%s\n", confPath, sslNote1(sslLines))
	return nil
}

func sslNote1(ssl string) string {
	if ssl != "" {
		return " + SSL"
	}
	return ""
}

// sanitizeIdent1 站点别名安全化(文件名用)
func sanitizeIdent1(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "site"
	}
	return b.String()
}

// p1OpenRestyContainer 1Panel 的 OpenResty 容器名(排除面板自身网关)
func p1OpenRestyContainer() string {
	out, err := util.Run("docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return ""
	}
	for _, n := range strings.Split(out, "\n") {
		n = strings.TrimSpace(n)
		l := strings.ToLower(n)
		if n == "" || strings.HasPrefix(l, "kun-") {
			continue
		}
		if strings.Contains(l, "openresty") {
			return n
		}
	}
	return ""
}
