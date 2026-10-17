// p1apply —— 1Panel → 坤面板 迁移执行:站点/证书/数据库,逐站校验可撤销。
package p1migrate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/btmigrate"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

// ApplyOptions 1Panel 迁移选项
type ApplyOptions struct {
	Sites       []string // 要迁移的站点域名
	ImportCerts bool
	Databases   []string // 要迁移的数据库名
}

// siteDone 回调:域名, 面板站点 ID
func Apply(opts ApplyOptions, w io.Writer, siteDone func(domain, siteID string)) error {
	res, err := P1Scan()
	if err != nil {
		return err
	}
	if !res.Found {
		return fmt.Errorf("未检测到 1Panel(%s)", p1Base)
	}
	sel := map[string]bool{}
	for _, d := range opts.Sites {
		sel[d] = true
	}
	if len(sel) == 0 && len(opts.Databases) == 0 {
		return fmt.Errorf("请选择要迁移的站点或数据库")
	}

	// 网关容器挂载 1Panel www 目录:静态站 root 保持原路径直接服务
	wwwDir := p1WwwDir()
	if err := EnsureP1SiteMount(w, wwwDir); err != nil {
		return err
	}

	migrated := 0
	for _, s := range res.Sites {
		if !sel[s.Domain] {
			continue
		}
		siteID, err := migrateOneSite(s, opts, w)
		if err != nil {
			if strings.Contains(err.Error(), "站点已存在") {
				fmt.Fprintf(w, "   %s 已在面板中(跳过,不覆盖)\n", s.Domain)
				continue
			}
			fmt.Fprintf(w, "   站点 %s 迁移中止: %v(继续迁移其余站点)\n", s.Domain, err)
			continue
		}
		migrated++
		if siteDone != nil {
			siteDone(s.Domain, siteID)
		}
	}
	fmt.Fprintf(w, ">> 站点迁移完成:%d 个\n", migrated)

	// 数据库(可选):从 1Panel 的 MySQL 容器导出 → 导入面板 MySQL
	for _, name := range opts.Databases {
		fmt.Fprintf(w, ">> 迁移数据库 %s ...\n", name)
		if err := migrateP1Database(w, name, res.DBs); err != nil {
			fmt.Fprintf(w, "   失败: %v(继续)\n", err)
		}
	}

	// 统一重载面板网关
	gw, gwErr := gatewayContainer()
	if gwErr != nil {
		return gwErr
	}
	if out, terr := dockerx.ExecIn(gw, "nginx", "-t"); terr != nil {
		return fmt.Errorf("Nginx 配置校验失败: %v: %s", terr, tailOf(out, 200))
	}
	_, _ = dockerx.ExecIn(gw, "nginx", "-s", "reload")
	fmt.Fprintf(w, ">> 1Panel 迁移完成。确认站点可访问后,再执行流量切换接管 80/443\n")
	return nil
}

// gatewayContainer 面板网关容器(容器 nginx/openresty 通用)
func gatewayContainer() (string, error) {
	mode := websites.DetectNginx()
	if mode.Mode == "container" && mode.Target != "" {
		return mode.Target, nil
	}
	return "", fmt.Errorf("未检测到面板网关容器,请先在应用商店安装并启动 Nginx/OpenResty")
}

// migrateOneSite 迁移单个 1Panel 站点;失败撤销产物
func migrateOneSite(s P1Site, opts ApplyOptions, w io.Writer) (string, error) {
	fmt.Fprintf(w, ">> 迁移站点 %s(%s)...\n", s.Domain, s.Type)

	// 1) 反代目标/PHP 上游改写(回环 → 网桥)
	proxyTo := ""
	fpmAddr := ""
	switch s.Type {
	case "proxy":
		proxyTo = P1RewriteProxyTarget(s.ProxyTo)
		if proxyTo != s.ProxyTo {
			fmt.Fprintf(w, "   反代目标已改写: %s → %s\n", s.ProxyTo, proxyTo)
			// 上游只监听 127.0.0.1 时注册端口转发(与宝塔版一致);
			// 上游监听 0.0.0.0 时转发器绑定失败属正常,可达性由验证兜底
			if m := regexp.MustCompile(`:(\d{1,5})`).FindStringSubmatch(s.ProxyTo); m != nil {
				if p, perr := strconv.Atoi(m[1]); perr == nil {
					if ferr := btmigrate.RegisterProxyForward(w, p); ferr != nil {
						return "", fmt.Errorf("反代上游端口 %d 不可用: %v", p, ferr)
					}
				}
			}
		}
	case "php":
		fpmAddr = P1FpmRewrite(s.FpmAddr)
		if s.FpmUnix {
			fmt.Fprintf(w, "   警告: PHP 运行时为 unix socket(%s),面板容器不可达,该站 PHP 请求将 502;建议在 1Panel 侧改为 TCP 监听后重迁\n", s.FpmAddr)
		} else if fpmAddr != s.FpmAddr {
			fmt.Fprintf(w, "   PHP 上游已改写: %s → %s\n", s.FpmAddr, fpmAddr)
		}
	}

	// 2) 建站(phpfpm 型保持原根目录,面板按原路径服务)
	var siteID string
	var err error
	switch s.Type {
	case "proxy":
		var created *websites.Site
		created, err = websites.Create(websites.CreateOpts{
			Domain: s.Domain, SiteType: "proxy", ProxyTo: proxyTo,
			ProxyHost: "$http_host", Port: 80,
		})
		if created != nil {
			siteID = created.ID
		}
	case "php":
		root := s.Root
		if rerr := validateP1Root(root); rerr != nil {
			root = "" // 根目录异常时交由面板自动生成(代码文件可后续手工迁移)
			fmt.Fprintf(w, "   根目录 %s 不可用(%v),改为面板根目录\n", s.Root, rerr)
		}
		var created *websites.Site
		created, err = websites.Create(websites.CreateOpts{
			Domain: s.Domain, SiteType: "phpfpm", FpmPass: fpmAddr,
			RootDir: root, Port: 80,
		})
		if created != nil {
			siteID = created.ID
		}
	default:
		// 静态站:面板容器模式会把 root 重写为 /var/www/<站点ID>(映射到面板
		// roots 目录),1Panel 原路径不生效——故用面板标准根目录,迁移后拷贝文件
		var created *websites.Site
		created, err = websites.Create(websites.CreateOpts{
			Domain: s.Domain, SiteType: "static", Port: 80,
		})
		if err != nil {
			return "", err
		}
		siteID = created.ID
		if s.Root != "" {
			if rerr := validateP1Root(s.Root); rerr == nil {
				if _, serr := os.Stat(s.Root); serr == nil {
					if out, cerr := util.Run("sh", "-c",
						fmt.Sprintf("mkdir -p %q && cp -a %q/. %q/", created.Root, s.Root, created.Root)); cerr != nil {
						fmt.Fprintf(w, "   站点文件拷贝失败(%v: %s),目录为空可手工补齐\n", cerr, tailOf(out, 120))
					} else {
						fmt.Fprintf(w, "   站点文件已拷贝:%s → %s\n", s.Root, created.Root)
					}
				}
			} else {
				fmt.Fprintf(w, "   根目录 %s 不可用(%v),目录为空\n", s.Root, rerr)
			}
		}
	}
	if err != nil {
		return "", err
	}

	// 3) 证书
	if opts.ImportCerts && s.SSL && s.CertDir != "" {
		crt := filepath.Join(s.CertDir, "fullchain.pem")
		key := filepath.Join(s.CertDir, "privkey.pem")
		if _, serr := os.Stat(crt); serr != nil {
			key = filepath.Join(s.CertDir, "private.key")
		}
		if cid, cerr := importP1Cert(s.Domain, crt, key); cerr != nil {
			fmt.Fprintf(w, "   证书导入失败(%v),站点可稍后手动绑定\n", cerr)
		} else {
			_ = websites.SetSiteSSL(siteID, cid, true)
			fmt.Fprintf(w, "   证书已导入并绑定\n")
		}
	}

	// 4) 单站配置校验:失败撤销该站全部产物
	gw, gwErr := gatewayContainer()
	if gwErr != nil {
		undoP1Site(siteID, w)
		return "", gwErr
	}
	if out, terr := dockerx.ExecIn(gw, "nginx", "-t"); terr != nil {
		fmt.Fprintf(w, "   nginx -t 未通过,撤销站点 %s 的全部产物: %s\n", s.Domain, tailOf(out, 200))
		undoP1Site(siteID, w)
		return "", fmt.Errorf("站点 %s 配置校验未通过", s.Domain)
	}
	fmt.Fprintf(w, "   站点 %s 迁移完成(nginx -t 通过)\n", s.Domain)
	return siteID, nil
}

// importP1Cert 导入 1Panel 证书(容器内读取,宿主机直接读同路径亦可——
// 1Panel 目录在宿主机明文存在)
func importP1Cert(name, crtPath, keyPath string) (string, error) {
	crt, err := os.ReadFile(crtPath)
	if err != nil {
		return "", fmt.Errorf("读取证书失败: %v", err)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("读取私钥失败: %v", err)
	}
	info, err := websites.UploadCert(name, string(crt), string(key))
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

// undoP1Site 撤销单站迁移产物(不动 1Panel 原文件)
func undoP1Site(siteID string, w io.Writer) {
	if err := websites.Delete(siteID); err != nil {
		if _, gerr := websites.GetSite(siteID); gerr != nil {
			fmt.Fprintf(w, "   站点 %s 产物已清理(重载被其他站点配置问题阻塞)\n", siteID)
			return
		}
		fmt.Fprintf(w, "   撤销站点 %s 失败: %v(请在网站页手动删除)\n", siteID, err)
		return
	}
	fmt.Fprintf(w, "   已撤销站点 %s(记录/配置)\n", siteID)
}

// migrateP1Database 从 1Panel 的 MySQL 容器导库:root 密码从容器环境变量读取,
// mysqldump 容器内执行 → 本机临时文件 → 导入面板 MySQL
func migrateP1Database(w io.Writer, name string, dbs []P1DB) error {
	var host string
	for _, d := range dbs {
		if d.Name == name {
			host = d.Host
		}
	}
	if host == "" {
		return fmt.Errorf("1Panel 中不存在数据库 %s", name)
	}
	// 找到数据库所在容器(1Panel 容器名形如 1Panel-mysql-xxxx,host 字段为简称)
	out, err := util.Run("docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return fmt.Errorf("docker ps 失败: %v", err)
	}
	var container string
	for _, n := range strings.Split(out, "\n") {
		n = strings.TrimSpace(n)
		if n != "" && strings.Contains(n, host) && !strings.Contains(n, "kun-") {
			container = n
			break
		}
	}
	if container == "" {
		return fmt.Errorf("未找到数据库容器(1Panel 记录 host=%s),容器可能未运行", host)
	}
	envOut, err := util.Run("docker", "inspect", container, "--format", "{{range .Config.Env}}{{println .}}{{end}}")
	if err != nil {
		return fmt.Errorf("读取容器环境失败: %v", err)
	}
	rootPW := ""
	for _, ln := range strings.Split(envOut, "\n") {
		if strings.HasPrefix(ln, "MYSQL_ROOT_PASSWORD=") {
			rootPW = strings.TrimPrefix(ln, "MYSQL_ROOT_PASSWORD=")
		}
	}
	if rootPW == "" {
		return fmt.Errorf("容器 %s 未设置 MYSQL_ROOT_PASSWORD 环境变量,无法导出", container)
	}
	tmp := filepath.Join(util.DataDir(), "p1mig_"+name+".sql")
	defer os.Remove(tmp)
	// 容器内 mysqldump(stdout)→ 宿主机文件
	out, err = util.RunWithTimeout(10*60*time.Second, "sh", "-c",
		fmt.Sprintf("docker exec %s sh -c \"mysqldump -uroot -p\\\"%s\\\" --single-transaction --quick %s\" > %s",
			container, strings.ReplaceAll(rootPW, "\"", "\\\""), name, tmp))
	if err != nil || out != "" {
		if b, rerr := os.ReadFile(tmp); rerr != nil || len(b) < 64 {
			os.Remove(tmp)
			return fmt.Errorf("mysqldump %s 失败: %s%s", name, tailOf(out, 150), tailOf(err.Error(), 150))
		}
	}
	fmt.Fprintf(w, "   已导出 %s(1Panel MySQL 容器)\n", name)
	if err := dbmgrCreateAndImport(name, tmp); err != nil {
		return err
	}
	fmt.Fprintf(w, "   %s 已导入面板 MySQL。注意:站点连接串需改指面板 MySQL\n", name)
	return nil
}
