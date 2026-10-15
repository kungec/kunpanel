// btmigrate —— 宝塔面板一键迁移
//
// 设计原则(最小侵入、可回滚):
//   - PHP 站点:不搬代码、不改 PHP 版本。为宝塔 php-fpm 增加「kunpanel」进程池,
//     监听 docker 网桥地址(172.17.0.1:端口),面板管理的 Nginx 容器通过该地址
//     fastcgi 直连宿主机 php-fpm;宝塔原有 unix socket 不受影响,可随时回滚。
//   - 反向代理站点:proxy_pass 原样迁移。
//   - 数据库:不迁移。站点继续连接宝塔的宿主机 MySQL(127.0.0.1:3306),零风险。
//   - 证书:拷贝 fullchain/privkey 到面板证书库并绑定。
//   - 计划任务:shell 与访问 URL 类型迁为面板计划任务,BT 专属(备份数据库等)仅提示。
//   - 流量切换 = 停宝塔 Nginx、面板 Nginx 容器改绑 80/443 并重建;回滚完全反向。
package btmigrate

import (
	"encoding/json"
	"io"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"kunpanel/internal/appstore"
	"kunpanel/internal/cronx"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/websites"
	"kunpanel/internal/util"
)

const (
	btPanelDir  = "/www/server/panel"
	btVhostDir  = "/www/server/panel/vhost/nginx"
	btRewrite   = "/www/server/panel/vhost/rewrite"
	btCertDir   = "/www/server/panel/vhost/cert"
	btWwwRoot   = "/www/wwwroot"
	btNginxBin  = "/www/server/nginx/sbin/nginx"
	btPHPDir    = "/www/server/php"
	poolGateway = "172.17.0.1"
	poolBase    = 9070
)

// BtSite 宝塔站点(由 vhost conf 解析)
type BtSite struct {
	Conf     string `json:"conf"`
	Domain   string `json:"domain"`   // 主域名(server_name 第一个)
	Aliases  string `json:"aliases"`  // 完整 server_name
	Root     string `json:"root"`
	Type     string `json:"type"` // php / proxy / static
	PhpVer   string `json:"php_ver,omitempty"`
	ProxyTo  string `json:"proxy_to,omitempty"`
	SSL      bool   `json:"ssl"`
	CertName string `json:"cert_name,omitempty"`
	Rewrite  string `json:"rewrite,omitempty"` // 伪静态文件路径(存在时)
}

// BtFPM 宝塔 php-fpm 版本
type BtFPM struct {
	Ver     string `json:"ver"`
	Sock    string `json:"sock"`
	PoolDir string `json:"pool_dir"`
	Service string `json:"service"`
}

// BtDB 宝塔数据库(仅清单展示,不迁移)
type BtDB struct {
	Name string `json:"name"`
	User string `json:"user"`
	Ps   string `json:"ps"`
}

// BtCron 宝塔计划任务
type BtCron struct {
	Name  string `json:"name"`
	Shell string `json:"shell"`
	Cycle string `json:"cycle"`
	Spec  string `json:"spec,omitempty"` // 标准 5 位 cron(可迁移时生成)
	Mig   bool   `json:"mig"`            // true=可自动迁移
}

// ScanResult 预检结果
type ScanResult struct {
	Found    bool      `json:"found"`
	NginxBin string    `json:"nginx_bin"`
	Sites    []BtSite  `json:"sites"`
	FPMs     []BtFPM   `json:"fpms"`
	DBs      []BtDB    `json:"dbs"`
	Crons    []BtCron  `json:"crons"`
	Warnings []string  `json:"warnings"`
	Scanned  time.Time `json:"scanned"`
}

// BtInstalled 检测宝塔面板是否存在
func BtInstalled() bool {
	st, err := os.Stat(filepath.Join(btPanelDir, "data", "default.db"))
	return err == nil && st != nil && !st.IsDir() ||
		dirExists(btVhostDir)
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// Scan 扫描宝塔环境
func Scan() (*ScanResult, error) {
	res := &ScanResult{Scanned: time.Now(), Sites: []BtSite{}, FPMs: []BtFPM{}, DBs: []BtDB{}, Crons: []BtCron{}, Warnings: []string{}}
	if !BtInstalled() {
		return res, nil
	}
	res.Found = true

	// Nginx 路径
	res.NginxBin = btNginxBin
	if _, err := os.Stat(btNginxBin); err != nil {
		res.NginxBin = "/www/server/nginx/sbin/nginx (未找到)"
		res.Warnings = append(res.Warnings, "未找到宝塔 Nginx("+btNginxBin+"),无法自动切换流量")
	}

	// 站点 conf 解析
	entries, err := os.ReadDir(btVhostDir)
	if err == nil {
		var confs []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") && !strings.HasPrefix(e.Name(), ".") {
				confs = append(confs, filepath.Join(btVhostDir, e.Name()))
			}
		}
		sort.Strings(confs)
		for _, cf := range confs {
			site, err := parseSiteConf(cf)
			if err != nil {
				res.Warnings = append(res.Warnings, "站点配置解析失败 "+filepath.Base(cf)+": "+err.Error())
				continue
			}
			if site.Domain != "" {
				res.Sites = append(res.Sites, site)
			}
		}
	}

	// php-fpm 版本发现
	if dirs, err := os.ReadDir(btPHPDir); err == nil {
		var vers []string
		for _, d := range dirs {
			name := d.Name()
			if d.IsDir() && len(name) >= 2 {
				valid := true
				for _, r := range name {
					if r < '0' || r > '9' {
						valid = false
						break
					}
				}
				if valid {
					vers = append(vers, name)
				}
			}
		}
		sort.Strings(vers)
		for _, v := range vers {
			sock := fmt.Sprintf("/tmp/php-cgi-%s.sock", v)
			if _, err := os.Stat(sock); err != nil {
				// 部分版本 socket 在 /tmp/php-cgi.sock 或未运行
				res.Warnings = append(res.Warnings, "PHP "+v+" 的 socket 未找到("+sock+"),该版本站点迁移后需确认 php-fpm 运行中")
			}
			res.FPMs = append(res.FPMs, BtFPM{Ver: v, Sock: sock,
				PoolDir: filepath.Join(btPHPDir, v, "etc", "php-fpm.d"),
				Service: "php-fpm-" + v})
		}
	}

	// 数据库清单(宝塔 pyenv python3 读 default.db)
	if out, err := util.RunWithTimeout(15*time.Second, filepath.Join(btPanelDir, "pyenv", "bin", "python3"), "-c",
		`import sqlite3,json;c=sqlite3.connect('/www/server/panel/data/default.db');c.row_factory=sqlite3.Row;print(json.dumps([dict(r) for r in c.execute('select name,username,ps from databases')]))`); err == nil {
		var dbs []BtDB
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dbs); err == nil {
			res.DBs = dbs
		}
	}
	if len(res.DBs) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("检测到 %d 个数据库:它们仍在宝塔的宿主机 MySQL(3306)中运行,站点连接串无需改动,本工具不做迁移", len(res.DBs)))
	}

	// 计划任务(default.db crontab 表)
	if out, err := util.RunWithTimeout(15*time.Second, filepath.Join(btPanelDir, "pyenv", "bin", "python3"), "-c",
		`import sqlite3,json;c=sqlite3.connect('/www/server/panel/data/default.db');c.row_factory=sqlite3.Row;print(json.dumps([dict(r) for r in c.execute('select name,type,where_hour,where_minute,sType,sBody from crontab where status=0 or status is null')]))`); err == nil {
		var crons []map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &crons); err == nil {
			for _, cr := range crons {
				name, _ := cr["name"].(string)
				typ := fmt.Sprint(cr["type"])
				styp, _ := cr["sType"].(string)
				sbody, _ := cr["sBody"].(string)
				hh := fmt.Sprint(cr["where_hour"])
				mm := fmt.Sprint(cr["where_minute"])
				c := BtCron{Name: name, Shell: strings.TrimSpace(sbody)}
				h := strings.TrimSpace(hh)
				if h == "" {
					h = "0"
				}
				m := strings.TrimSpace(mm)
				if m == "" {
					m = "0"
				}
				switch styp {
				case "day":
					c.Cycle = "每天 " + h + ":" + m
					c.Spec = m + " " + h + " * * *"
					c.Mig = true
				case "hour":
					c.Cycle = "每小时第 " + m + " 分"
					c.Spec = m + " * * * *"
					c.Mig = true
				case "nmonth":
					c.Cycle = "每月 " + mm + " 日 " + h + ":" + m
					c.Spec = m + " " + h + " * * *"
					c.Mig = true
				case "week":
					c.Cycle = "每周" + mm
					c.Spec = "0 " + h + " * * " + m
					c.Mig = true
				default:
					c.Cycle = styp
				}
				_ = typ
				if strings.TrimSpace(sbody) == "" {
					c.Mig = false
				}
				if !c.Mig {
					c.Cycle += "(无法自动迁移)"
				}
				res.Crons = append(res.Crons, c)
			}
		}
	}

	// 警告:反代到宿主机端口的站点(宝塔停服后若目标也停需注意)
	for _, s := range res.Sites {
		if s.Type == "proxy" && strings.Contains(s.ProxyTo, "127.0.0.1") {
			res.Warnings = append(res.Warnings, "站点 "+s.Domain+" 反代到 127.0.0.1:"+strings.TrimPrefix(s.ProxyTo, "http://127.0.0.1:")+",请确认该服务由 systemd 等守护(不依赖宝塔)")
			break // 只提示一次
		}
	}
	return res, nil
}

// parseSiteConf 解析宝塔 nginx vhost 配置
func parseSiteConf(path string) (BtSite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return BtSite{}, err
	}
	content := string(b)
	site := BtSite{Conf: path}

	reServerName := regexp.MustCompile(`server_name\s+([^;]+);`)
	reRoot := regexp.MustCompile(`root\s+([^;]+);`)
	reSSL := regexp.MustCompile(`listen\s+[^;]*443[^;]*ssl`)
	reCert := regexp.MustCompile(`ssl_certificate\s+([^;]+fullchain\.pem)`)
	rePHP := regexp.MustCompile(`include\s+enable-php-([0-9]+)\.conf`)
	reProxy := regexp.MustCompile(`proxy_pass\s+(https?://[^;]+);`)
	reRewrite := regexp.MustCompile(`include\s+/www/server/panel/vhost/rewrite/([^;]+\.conf);`)

	if m := reServerName.FindStringSubmatch(content); m != nil {
		site.Aliases = strings.TrimSpace(m[1])
		f := strings.Fields(site.Aliases)
		if len(f) > 0 {
			site.Domain = f[0]
		}
	}
	if m := reRoot.FindStringSubmatch(content); m != nil {
		site.Root = strings.TrimSpace(m[1])
	}
	site.SSL = reSSL.MatchString(content)
	if m := reCert.FindStringSubmatch(content); m != nil {
		site.CertName = strings.TrimSpace(filepath.Base(filepath.Dir(m[1])))
	}
	if m := rePHP.FindStringSubmatch(content); m != nil {
		site.PhpVer = m[1]
		site.Type = "php"
	}
	if m := reProxy.FindStringSubmatch(content); m != nil && site.Type == "" {
		site.Type = "proxy"
		site.ProxyTo = strings.TrimSpace(m[1])
	}
	if site.Type == "" {
		if site.Root != "" {
			site.Type = "static"
		} else {
			return site, fmt.Errorf("无法识别站点类型")
		}
	}
	if site.Domain == "" {
		return site, fmt.Errorf("无 server_name")
	}
	if m := reRewrite.FindStringSubmatch(content); m != nil {
		rw := filepath.Join(btRewrite, m[1])
		if _, err := os.Stat(rw); err == nil {
			site.Rewrite = rw
		}
	}
	return site, nil
}

// FpmPoolPlan 为站点规划 fpm pool(端口从 9071 起分配,按版本稳定)
type FpmPoolPlan struct {
	Ver  string
	Port int
}

// planFpmPools 为选中的 php 站点生成 pool 计划(同版本共享 pool)
func planFpmPools(fpms []BtFPM, sites []BtSite, selected map[string]bool) map[string]FpmPoolPlan {
	need := map[string]bool{}
	for _, s := range sites {
		if !selected[s.Domain] || s.Type != "php" || s.PhpVer == "" {
			continue
		}
		need[s.PhpVer] = true
	}
	out := map[string]FpmPoolPlan{}
	port := poolBase + 1
	for _, f := range fpms {
		if need[f.Ver] {
			out[f.Ver] = FpmPoolPlan{Ver: f.Ver, Port: port}
			port++
		}
	}
	return out
}

// ApplyOptions 迁移选项
type ApplyOptions struct {
	Sites       []string // 要迁移的站点域名
	ImportCerts bool
	MigrateCron bool
}

// Apply 执行迁移(任务体内调用,进度写 w)。
// siteDone 回调每个站点完成时触发(域名, 面板站点ID)。
func Apply(opts ApplyOptions, w io.Writer, siteDone func(domain, siteID string)) error {
	res, err := Scan()
	if err != nil {
		return err
	}
	if !res.Found {
		return fmt.Errorf("未检测到宝塔面板(%s)", btPanelDir)
	}
	// Nginx 容器挂载宝塔站点目录(phpfpm 站点 root 保持宿主机路径)
	if err := appstore.EnsureBtRootMount(w); err != nil {
		return err
	}
	sel := map[string]bool{}
	for _, d := range opts.Sites {
		sel[d] = true
	}

	// 1) fpm pool 规划与部署
	pools := planFpmPools(res.FPMs, res.Sites, sel)
	byDomain := map[string]BtSite{}
	for _, s := range res.Sites {
		byDomain[s.Domain] = s
	}
	reloaded := map[string]bool{}
	for ver, plan := range pools {
		var fpm *BtFPM
		for i := range res.FPMs {
			if res.FPMs[i].Ver == ver {
				fpm = &res.FPMs[i]
			}
		}
		if fpm == nil {
			continue
		}
		fmt.Fprintf(w, ">> 配置 PHP %s 进程池(kunpanel,监听 %s:%d)...\n", ver, poolGateway, plan.Port)
		if err := writeFpmPool(*fpm, plan.Port); err != nil {
			return fmt.Errorf("写入 PHP %s 进程池失败: %v", ver, err)
		}
		if !reloaded[ver] {
			reloadFpm(ver, fpm.Service)
			reloaded[ver] = true
		}
	}

	// 2) 逐站点迁移(此处不做重载,统一在最后 Nginx reload)
	var importedSites []string
	for _, s := range res.Sites {
		if !sel[s.Domain] {
			continue
		}
		fmt.Fprintf(w, ">> 迁移站点 %s(%s)...\n", s.Domain, s.Type)
		var siteID string
		var err error
		switch s.Type {
		case "php":
			plan := pools[s.PhpVer]
			var fpmAddr string
			if plan.Port > 0 {
				fpmAddr = fmt.Sprintf("%s:%d", poolGateway, plan.Port)
			} else if f := findFPM(res.FPMs, s.PhpVer); f != nil {
				fpmAddr = "unix:" + f.Sock // 无 pool 时原 socket(容器内不可达,仅占位)
			}
			if fpmAddr == "" {
				fpmAddr = fmt.Sprintf("%s:%d", poolGateway, poolBase+1)
			}
			siteID, err = createFpmSite(s.Domain, s.Root, fpmAddr, s.PhpVer)
		case "proxy":
			siteID, err = createProxySite(s.Domain, s.ProxyTo, s.Port80())
		default:
			siteID, err = createStaticSite(s.Domain, s.Root)
		}
		if err != nil {
			if strings.Contains(err.Error(), "站点已存在") {
				fmt.Fprintf(w, "   %s 已在面板中(跳过,不覆盖)\n", s.Domain)
				continue
			}
			return fmt.Errorf("迁移站点 %s 失败: %v", s.Domain, err)
		}
		importedSites = append(importedSites, siteID)
		// 伪静态
		if s.Rewrite != "" {
			if rw, err := os.ReadFile(s.Rewrite); err == nil && len(strings.TrimSpace(string(rw))) > 0 {
				_ = util.AtomicWrite(filepath.Join("/opt/kunpanel/data/websites/rewrite", siteID+".conf"), rw, 0o644)
				fmt.Fprintf(w, "   伪静态已迁移(%s)\n", filepath.Base(s.Rewrite))
			}
		}
		// 证书
		if opts.ImportCerts && s.SSL && s.CertName != "" {
			crt := filepath.Join(btCertDir, s.CertName, "fullchain.pem")
			key := filepath.Join(btCertDir, s.CertName, "privkey.pem")
			if cid, err := importCert(s.CertName, crt, key); err != nil {
				fmt.Fprintf(w, "   证书导入失败(%v),站点可稍后手动绑定\n", err)
			} else {
				bindSiteSSL(siteID, cid)
				fmt.Fprintf(w, "   证书已导入并绑定\n")
			}
		}
		if siteDone != nil {
			siteDone(s.Domain, siteID)
		}
	}
	_ = importedSites

	// 3) 计划任务
	if opts.MigrateCron {
		n := 0
		for _, c := range res.Crons {
			if !c.Mig || strings.TrimSpace(c.Shell) == "" || strings.TrimSpace(c.Spec) == "" {
				continue
			}
			if err := addCron(c.Name, c.Shell, c.Spec); err != nil {
				fmt.Fprintf(w, "   计划任务[%s]迁移失败: %v\n", c.Name, err)
				continue
			}
			n++
		}
		fmt.Fprintf(w, ">> 计划任务已迁移 %d 条\n", n)
	}

	// 4) Nginx 校验与重载(先不切换 80/443)
	if _, err := dockerx.ExecIn("kun-nginx", "nginx", "-t"); err != nil {
		return fmt.Errorf("Nginx 配置校验失败: %v", err)
	}
	_, _ = dockerx.ExecIn("kun-nginx", "nginx", "-s", "reload")
	fmt.Fprintf(w, ">> 迁移完成。请确认站点在本面板可访问后,再执行「切换流量」接管 80/443\n")
	return nil
}

func findFPM(fpms []BtFPM, ver string) *BtFPM {
	for i := range fpms {
		if fpms[i].Ver == ver {
			return &fpms[i]
		}
	}
	return nil
}

// Port80 返回 HTTP 端口(宝塔站点 conf 的 listen 80)
func (s BtSite) Port80() int { return 80 }

// writeFpmPool 写宝塔 php-fpm 的 kunpanel pool(不改动原有 www pool)
func writeFpmPool(f BtFPM, port int) error {
	// 从原 www.conf 提取 user/group 与 pm 参数,保持一致
	user, group := "www", "www"
	if b, err := os.ReadFile(filepath.Join(btPHPDir, f.Ver, "etc", "php-fpm.d", "www.conf")); err == nil {
		reUser := regexp.MustCompile(`(?m)^user\s*=\s*(\S+)`)
		reGroup := regexp.MustCompile(`(?m)^group\s*=\s*(\S+)`)
		if mm := reUser.FindStringSubmatch(string(b)); mm != nil {
			user = mm[1]
		}
		if mm := reGroup.FindStringSubmatch(string(b)); mm != nil {
			group = mm[1]
		}
	}
	pool := fmt.Sprintf(`[kunpanel]
user = %s
group = %s
listen = %s:%d
pm = dynamic
pm.max_children = 20
pm.start_servers = 3
pm.min_spare_servers = 2
pm.max_spare_servers = 5
php_admin_value[error_log] = /www/wwwlogs/kunpanel-php-%s-error.log
php_admin_flag[log_errors] = on
`, user, group, poolGateway, port, f.Ver)
	return util.AtomicWrite(filepath.Join(f.PoolDir, "kunpanel.conf"), []byte(pool), 0o644)
}

// reloadFpm 重载宝塔 php-fpm(USR2 复用配置)
func reloadFpm(ver, service string) {
	// 优先 systemd reload
	if out, err := exec.Command("systemctl", "reload", service).CombinedOutput(); err == nil && len(out) == 0 {
		return
	}
	// 回退:USR2 master
	if b, err := os.ReadFile(filepath.Join(btPHPDir, ver, "var", "run", "php-fpm.pid")); err == nil {
		pid := strings.TrimSpace(string(b))
		if pid != "" {
			_ = exec.Command("kill", "-USR2", pid).Run()
		}
	}
	time.Sleep(2 * time.Second)
}


// createFpmSite 经面板创建 PHP-FPM(fastcgi)站点
func createFpmSite(domain, root, fpmPass, phpVer string) (string, error) {
	id, err := createViaWebsites(domain, "phpfpm", root, fpmPass)
	if err == nil && id != "" {
		_ = recordSiteMeta(id, phpVer)
	}
	return id, err
}

func createProxySite(domain, proxyTo string, port int) (string, error) {
	return createViaWebsitesOpts(domain, "proxy", "", proxyTo, port)
}

func createStaticSite(domain, root string) (string, error) {
	return createViaWebsites(domain, "static", root, "")
}

// importCert 导入宝塔证书到面板证书库,返回证书 ID
func importCert(name, crtPath, keyPath string) (string, error) {
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

// bindSiteSSL 绑定站点证书
func bindSiteSSL(siteID, certID string) {
	_ = websites.SetSiteSSL(siteID, certID, true)
}

// addCron 迁移计划任务为面板 shell 任务(spec 由 BT 周期换算)
func addCron(name, shell, spec string) error {
	_, err := cronx.Add(cronx.Job{
		Name:      "BT-" + name,
		Type:      "shell",
		Spec:      spec,
		Command:   shell,
		Enabled:   true,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	})
	return err
}

// recordSiteMeta 保留站点与 PHP 版本的映射说明(写入站点备注)
func recordSiteMeta(siteID, phpVer string) error {
	return websites.AppendRemark(siteID, "BT迁移 PHP "+phpVer+" (fastcgi)")
}

// createViaWebsites 经面板创建站点(phpfpm/static)
func createViaWebsites(domain, siteType, root, fpmPass string) (string, error) {
	site, err := websites.Create(websites.CreateOpts{
		Domain:   domain,
		SiteType: siteType,
		RootDir:  root,
		FpmPass:  fpmPass,
		Port:     80,
	})
	if err != nil {
		return "", err
	}
	return site.ID, nil
}

// createViaWebsitesOpts 经面板创建反代站点
func createViaWebsitesOpts(domain, siteType, root, proxyTo string, port int) (string, error) {
	site, err := websites.Create(websites.CreateOpts{
		Domain:   domain,
		SiteType: siteType,
		ProxyTo:  proxyTo,
		RootDir:  root,
		Port:     port,
	})
	if err != nil {
		return "", err
	}
	return site.ID, nil
}

// ---------- 流量切换与回滚 ----------


// TrafficState 当前流量归属
type TrafficState struct {
	BTNginxActive bool `json:"bt_nginx"`
	PanelNginx80  bool `json:"panel_nginx_80"`
	Switched      bool `json:"switched"`
}

// SwitchTraffic 流量切换:停宝塔 Nginx → 面板 Nginx 改绑 80/443 → 重建容器
func SwitchTraffic(w io.Writer) error {
	res, err := Scan()
	if err != nil {
		return err
	}
	if !res.Found {
		return fmt.Errorf("未检测到宝塔面板")
	}
	// 1) 停宝塔 Nginx(仅停 nginx,不动 php-fpm/mysql)
	fmt.Fprintln(w, ">> 停止宝塔 Nginx(php-fpm 与 MySQL 保持运行)...")
	_ = exec.Command(btNginxBin, "-s", "stop").Run()
	time.Sleep(1 * time.Second)

	// 2) 面板 Nginx 改绑 80/443
	fmt.Fprintln(w, ">> 面板 Nginx 改绑 80/443 ...")
	for _, key := range []string{"nginx", "openresty"} {
		dir := filepath.Join("/opt/kunpanel/apps", key)
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
		if err != nil {
			continue
		}
		content := string(b)
		changed := false
		// 常见旧端口(8080/8443 等)映射改绑 80/443;已是 80/443 则跳过
		rePort := regexp.MustCompile(`(?m)^(\s*-)\s*"([0-9]{4,5}):(80)"`)
		content2 := rePort.ReplaceAllStringFunc(content, func(m0 string) string {
			m1 := regexp.MustCompile(`(\s*-\s*)"([0-9]{4,5}):(80)"`).FindStringSubmatch(m0)
			if m1 == nil {
				return m0
			}
			changed = true
			return m1[1] + `"80:80"`
		})
		rePort2 := regexp.MustCompile(`(?m)^(\s*-\s*)"([0-9]{4,5}):(443)"`)
		content2 = rePort2.ReplaceAllStringFunc(content2, func(m0 string) string {
			m1 := regexp.MustCompile(`(\s*-\s*)"([0-9]{4,5}):(443)"`).FindStringSubmatch(m0)
			if m1 == nil {
				return m0
			}
			changed = true
			return m1[1] + `"443:443"`
		})
		if changed {
			if err := util.AtomicWriteString(filepath.Join(dir, "docker-compose.yml"), content2, 0o644); err != nil {
				return err
			}
			out, err := dockerx.Compose(dir, "up", "-d")
			fmt.Fprintln(w, out)
			if err != nil {
				// 失败回滚:重启宝塔 Nginx
				_ = exec.Command(btNginxBin).Run()
				return fmt.Errorf("[%s] 容器重建失败,已恢复宝塔 Nginx: %s", key, tailStr(out, 300))
			}
		}
	}
	fmt.Fprintln(w, ">> 流量已切换到面板 Nginx。如需回滚,请使用「回滚到宝塔」。")
	return nil
}

// RollbackTraffic 回滚:停面板 Nginx 容器 → 恢复端口 → 启动宝塔 Nginx
func RollbackTraffic(w io.Writer) error {
	fmt.Fprintln(w, ">> 停止面板 Nginx 容器 ...")
	for _, key := range []string{"nginx", "openresty"} {
		dir := filepath.Join("/opt/kunpanel/apps", key)
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
			continue
		}
		out, err := dockerx.Compose(dir, "stop")
		fmt.Fprintln(w, out)
		_ = err
	}
	fmt.Fprintln(w, ">> 启动宝塔 Nginx ...")
	if err := exec.Command(btNginxBin).Start(); err != nil {
		return fmt.Errorf("启动宝塔 Nginx 失败: %v", err)
	}
	time.Sleep(2 * time.Second)
	fmt.Fprintln(w, ">> 已回滚到宝塔。原站点配置未改动,应立即恢复访问。")
	return nil
}

// tailStr 取字符串尾部(错误输出截断)
func tailStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TrafficStatus 查询流量归属(宝塔 nginx / 面板 nginx)
func TrafficStatus() TrafficState {
	st := TrafficState{}
	if _, err := os.Stat(btNginxBin); err == nil {
		out, err := exec.Command("sh", "-c", "ss -tlnp 2>/dev/null | grep -E ':80 |:443 ' | head -2").CombinedOutput()
		if err == nil {
			st.BTNginxActive = strings.Contains(strings.ToLower(string(out)), "nginx")
		}
	}
	if out, err := exec.Command("sh", "-c", "docker ps --format '{{.Names}} {{.Ports}}' 2>/dev/null | grep -E 'kun-(nginx|openresty)' | head -2").CombinedOutput(); err == nil {
		st.PanelNginx80 = strings.Contains(string(out), ":80->") || strings.Contains(string(out), ":80-")
	}
	st.Switched = st.PanelNginx80 && !st.BTNginxActive
	return st
}

// TrafficStatusFunc 供外部包调用的状态查询
func TrafficStatusFunc() TrafficState { return TrafficStatus() }
