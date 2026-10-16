// reverse.go —— 面板 → 宝塔 反向迁移。
//
// 把坤面板管理的站点(域名/目录/SSL/伪静态)与数据库导出到本机宝塔:
//   - 站点文件复制到 /www/wwwroot/<别名>
//   - 生成宝塔格式 vhost(phpfpm 站点用宝塔 enable-php-<ver>;proxy 站点原样反代)
//   - 证书拷入宝塔证书目录(fullchain.pem/privkey.pem)
//   - 数据库 mysqldump 从面板 MySQL 导出到宿主机 MySQL(可选)
//
// 宝塔 Nginx 不动运行状态:配置写入后 nginx -t 校验,BT 重启/重载时生效。
package btmigrate

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"kunpanel/internal/dbmgr"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

// ReversePlanResult 反向迁移预检:面板站点/数据库 + 宝塔侧就绪信息
type ReversePlanResult struct {
	BTReady     bool            `json:"bt_ready"`
	NginxBin    string          `json:"nginx_bin"`
	PHPVersions []string        `json:"php_versions"` // 宝塔已装的 PHP(决定能否用 enable-php)
	HasMySQL    bool            `json:"has_mysql"`    // 宝塔 MySQL 客户端可用(决定能否迁库)
	Sites       []websites.Site `json:"sites"`
	DBs         []string        `json:"dbs"`
	Warnings    []string        `json:"warnings"`
}

// ReverseOptions 反向迁移选项
type ReverseOptions struct {
	Sites []string // 面板站点 ID
	DBs   []string // 面板数据库名
}

// ReversePlan 扫描
func ReversePlan() (*ReversePlanResult, error) {
	p := &ReversePlanResult{Sites: []websites.Site{}, DBs: []string{}, Warnings: []string{}, PHPVersions: []string{}}
	p.BTReady = BtInstalled()
	if !p.BTReady {
		p.Warnings = append(p.Warnings, "未检测到宝塔面板("+btPanelDir+")")
		return p, nil
	}
	p.NginxBin = btNginxBin
	if _, err := os.Stat(btNginxBin); err != nil {
		p.Warnings = append(p.Warnings, "未找到宝塔 Nginx,导出的配置将在宝塔重载时生效")
	}
	if dirs, err := os.ReadDir(btPHPDir); err == nil {
		for _, d := range dirs {
			name := d.Name()
			if !d.IsDir() || len(name) < 2 {
				continue
			}
			valid := true
			for _, r := range name {
				if r < '0' || r > '9' {
					valid = false
					break
				}
			}
			if valid {
				p.PHPVersions = append(p.PHPVersions, name)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(btMySQLBin, "mysql")); err == nil {
		p.HasMySQL = true
	} else if _, err := exec.LookPath("mysql"); err == nil {
		p.HasMySQL = true
	}
	if list, err := websites.List(); err == nil {
		p.Sites = list
	}
	// 面板数据库清单(mysql 实例)
	if insts := dbmgr.Instances(); len(insts) > 0 {
		for _, inst := range insts {
			if inst.Key != "mysql" || !inst.Running {
				continue
			}
			if dbs, err := dbmgr.Databases("mysql"); err == nil {
				for _, d := range dbs {
					p.DBs = append(p.DBs, d)
				}
			}
		}
	}
	return p, nil
}

// ReverseApply 执行面板 → 宝塔 导出
func ReverseApply(opts ReverseOptions, w io.Writer) error {
	if !BtInstalled() {
		return fmt.Errorf("未检测到宝塔面板(%s)", btPanelDir)
	}
	sel := map[string]bool{}
	for _, id := range opts.Sites {
		sel[id] = true
	}
	all, err := websites.List()
	if err != nil {
		return err
	}
	var exported int
	for _, s := range all {
		if !sel[s.ID] {
			continue
		}
		fmt.Fprintf(w, ">> 导出站点 %s(%s)到宝塔...\n", s.Domain, s.Type)
		if err := exportSiteToBT(s, w); err != nil {
			fmt.Fprintf(w, "   失败: %v(继续下一个)\n", err)
			continue
		}
		exported++
	}
	// 数据库
	selDB := map[string]bool{}
	for _, d := range opts.DBs {
		selDB[d] = true
	}
	migDB := 0
	if len(selDB) > 0 {
		for _, d := range opts.DBs {
			fmt.Fprintf(w, ">> 迁移数据库 %s 到宝塔宿主机 MySQL...\n", d)
			if err := MigratePanelDatabase(w, d); err != nil {
				fmt.Fprintf(w, "   失败: %v(继续)\n", err)
				continue
			}
			migDB++
		}
	}
	// BT nginx 配置校验(仅提示,不改运行状态)
	if _, err := os.Stat(btNginxBin); err == nil {
		if out, err := exec.Command(btNginxBin, "-t").CombinedOutput(); err != nil {
			fmt.Fprintf(w, ">> 注意:宝塔 Nginx 配置校验未通过: %s\n", tailStr(string(out), 300))
		} else {
			fmt.Fprintf(w, ">> 宝塔 Nginx 配置校验通过,导出完成:站点 %d 个,数据库 %d 个\n", exported, migDB)
			fmt.Fprintln(w, "   在宝塔面板里重载/重启 Nginx 后即可用新配置访问。")
			return nil
		}
	}
	fmt.Fprintf(w, ">> 导出完成:站点 %d 个,数据库 %d 个(宝塔 Nginx 重载后生效)\n", exported, migDB)
	return nil
}

// exportSiteToBT 单站点导出:目录/证书/伪静态/vhost
func exportSiteToBT(s websites.Site, w io.Writer) error {
	alias := s.Alias
	if alias == "" {
		alias = strings.Fields(s.Domain)[0]
	}
	safe := sanitizeIdent(strings.ReplaceAll(alias, ".", "_"))
	wwwroot := filepath.Join(btWwwRoot, safe)
	// 1) 站点目录
	if s.Type != "proxy" {
		if _, err := os.Stat(s.Root); err == nil {
			fmt.Fprintf(w, "   复制站点目录 %s → %s ...\n", s.Root, wwwroot)
			if out, err := exec.Command("cp", "-a", s.Root+"/.", wwwroot+"/").CombinedOutput(); err != nil {
				return fmt.Errorf("复制站点目录失败: %v: %s", err, tailStr(string(out), 150))
			}
		} else {
			_ = os.MkdirAll(wwwroot, 0o755)
		}
		_ = exec.Command("chown", "-R", "www:www", wwwroot).Run()
	} else {
		_ = os.MkdirAll(wwwroot, 0o755)
	}
	// 2) 证书
	ssl := ""
	if s.SslCertID != "" {
		if crt, key, err := websites.CertFilePaths(s.SslCertID); err == nil && crt != "" {
			certDir := filepath.Join(btCertDir, alias)
			if err := os.MkdirAll(certDir, 0o755); err == nil {
				if b, e := os.ReadFile(crt); e == nil {
					_ = util.AtomicWrite(filepath.Join(certDir, "fullchain.pem"), b, 0o644)
				}
				if b, e := os.ReadFile(key); e == nil {
					_ = util.AtomicWrite(filepath.Join(certDir, "privkey.pem"), b, 0o600)
				}
				ssl = "y"
			}
		}
	}
	// 3) 伪静态
	if rw, err := websites.GetRewrite(s.ID); err == nil && strings.TrimSpace(rw) != "" {
		_ = os.MkdirAll(btRewrite, 0o755)
		_ = util.AtomicWrite(filepath.Join(btRewrite, alias+".conf"), []byte(rw), 0o644)
	}
	// 4) BT vhost
	conf := btVhostFor(s, safe, ssl)
	_ = os.MkdirAll(btVhostDir, 0o755)
	if err := util.AtomicWriteString(filepath.Join(btVhostDir, alias+".conf"), conf, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "   已生成宝塔配置 %s.conf(目录/证书/伪静态)%s\n", alias, sslNote(ssl))
	return nil
}

func sslNote(ssl string) string {
	if ssl != "" {
		return " + SSL"
	}
	return ""
}

// btVhostFor 生成宝塔格式 vhost
func btVhostFor(s websites.Site, safe, ssl string) string {
	certDirName := s.Alias // 证书目录用原域名(带点),与宝塔一致;safe 仅用于日志等文件名
	var b strings.Builder
	b.WriteString("server {\n")
	b.WriteString("    listen 80;\n")
	b.WriteString("    server_name " + s.Domain + ";\n")
	if ssl != "" {
		b.WriteString("    listen 443 ssl http2;\n")
		b.WriteString("    ssl_certificate " + filepath.Join(btCertDir, certDirName, "fullchain.pem") + ";\n")
		b.WriteString("    ssl_certificate_key " + filepath.Join(btCertDir, certDirName, "privkey.pem") + ";\n")
		b.WriteString("    ssl_protocols TLSv1.2 TLSv1.3;\n")
	}
	b.WriteString("    index index.html index.htm index.php;\n")
	if s.Type != "proxy" {
		b.WriteString("    root " + filepath.Join(btWwwRoot, safe) + ";\n")
	}
	// PHP include(仅宝塔已装对应版本时)
	phpVer := ""
	if s.Type == "phpfpm" {
		// 宝塔迁移过来的 phpfpm 站点:备注里有「BT迁移 PHP <ver>」
		phpVer = phpVerFromRemark(s.Remark)
	}
	if phpVer != "" && btPHPEnabled(phpVer) {
		b.WriteString("    #PHP-INFO-START\n    include enable-php-" + phpVer + ".conf;\n    #PHP-INFO-END\n")
	} else if s.Type == "phpfpm" {
		b.WriteString("    # 注意:宝塔未安装该站点原 PHP 版本,PHP 请求将无法执行;请在宝塔安装对应版本后添加 include enable-php-<ver>.conf\n")
	}
	// 伪静态
	if _, err := os.Stat(filepath.Join(btRewrite, safe+".conf")); err == nil {
		b.WriteString("    #REWRITE-START\n    include /www/server/panel/vhost/rewrite/" + safe + ".conf;\n    #REWRITE-END\n")
	}
	// 业务 location
	switch s.Type {
	case "proxy", "php":
		target := s.ProxyTo
		b.WriteString("    location / {\n        proxy_pass " + target + ";\n")
		b.WriteString(`        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
`)
	default:
		b.WriteString("    location / {\n        try_files $uri $uri/ =404;\n    }\n")
	}
	// 面板侧自定义配置一并带过去(剔除面板专属 WAF 指令)
	if extra := websites.GetSiteRawExtra(s.ID); strings.TrimSpace(extra) != "" {
		cleaned := strings.ReplaceAll(extra, "access_by_lua_file /usr/local/kunwaf/waf.lua;", "# (宝塔侧无 WAF,该行已移除)")
		b.WriteString("    # ---- 站点自定义配置(自坤面板迁移)----\n")
		b.WriteString(cleaned)
		if !strings.HasSuffix(cleaned, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString("    access_log /www/wwwlogs/" + safe + ".log;\n")
	b.WriteString("    error_log /www/wwwlogs/" + safe + ".error.log;\n")
	b.WriteString("}\n")
	return b.String()
}

// btPHPEnabled 宝塔是否装了某 PHP 版本且 enable-php-<ver>.conf 存在
func btPHPEnabled(ver string) bool {
	if ver == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(btPHPDir, ver)); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join("/www/server/nginx/conf", "enable-php-"+ver+".conf"))
	return err == nil
}

// phpVerFromRemark 从站点备注解析宝塔 PHP 版本(BT迁移 PHP 74 (fastcgi))
func phpVerFromRemark(remark string) string {
	i := strings.Index(remark, "BT迁移 PHP ")
	if i < 0 {
		return ""
	}
	rest := remark[i+len("BT迁移 PHP "):]
	var b strings.Builder
	for _, r := range rest {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			break
		}
	}
	return b.String()
}
