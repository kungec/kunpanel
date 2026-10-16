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
	"sync"
	"strconv"
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
	btDBPath    = "/www/server/panel/data/default.db"
	btCronDBNew = "/www/server/panel/data/db/crontab.db"
	btVhostDir  = "/www/server/panel/vhost/nginx"
	btRewrite   = "/www/server/panel/vhost/rewrite"
	btCertDir   = "/www/server/panel/vhost/cert"
	btWwwRoot   = "/www/wwwroot"
	btNginxBin  = "/www/server/nginx/sbin/nginx"
	btMySQLBin  = "/www/server/mysql/bin"
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
	ID    string `json:"id,omitempty"` // 宝塔 crontab 表 id(spool 脚本目录 /www/server/cron/<id>/)
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

// scanCache Scan 结果 60s 缓存:扫描要跑 python3 读库 + docker 探测(~200-300ms),
// 网站页每次渲染都会调;迁移/切换/回滚内部用 Scan() 实时版
var (
	scanMu     sync.Mutex
	scanCache  *ScanResult
	scanCacheAt time.Time
)

// ScanCached 带缓存的扫描(API 用)
func ScanCached() (*ScanResult, error) {
	scanMu.Lock()
	if scanCache != nil && time.Since(scanCacheAt) < 60*time.Second {
		out := scanCache
		scanMu.Unlock()
		return out, nil
	}
	scanMu.Unlock()
	res, err := Scan()
	if err != nil {
		return nil, err
	}
	scanMu.Lock()
	scanCache, scanCacheAt = res, time.Now()
	scanMu.Unlock()
	return res, nil
}

// InvalidateScanCache 迁移/切换/回滚后调用
func InvalidateScanCache() {
	scanMu.Lock()
	scanCache = nil
	scanMu.Unlock()
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
			// 注意:面板 systemd 开了 PrivateTmp,看不到真实 /tmp,这里探测不到是常态;
			// 迁移用 TCP pool(172.17.0.1:907x)直连,不依赖 socket,提示改为中性说明
			res.FPMs = append(res.FPMs, BtFPM{Ver: v, Sock: fmt.Sprintf("/tmp/php-cgi-%s.sock", v),
				PoolDir: filepath.Join(btPHPDir, v, "etc", "php-fpm.d"),
				Service: "php-fpm-" + v})
		}
	}

	// 数据库清单(default.db databases 表,错误透出不再静默)
	if out, err := pythonJSON(fmt.Sprintf(
		`import sqlite3,json;c=sqlite3.connect('%s');c.row_factory=sqlite3.Row;print(json.dumps([dict(r) for r in c.execute('select name,username as user,ps from databases')]))`, cronDbPath())); err != nil {
		res.Warnings = append(res.Warnings, "读取宝塔数据库清单失败(不影响站点迁移): "+err.Error())
	} else {
		var dbs []BtDB
		if err := json.Unmarshal([]byte(out), &dbs); err != nil {
			res.Warnings = append(res.Warnings, "解析宝塔数据库清单失败: "+err.Error())
		} else {
			res.DBs = dbs
		}
	}
	if len(res.DBs) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("检测到 %d 个数据库:它们仍在宝塔的宿主机 MySQL(3306)中运行,站点连接串无需改动,本工具不做迁移", len(res.DBs)))
	}

	// 计划任务(default.db crontab 表)
	// 宝塔各版本 schema 不一致:老版本有 sType,11.x 起只有 type 字段。
	// 用 PRAGMA 先探测可用列再拼查询,查询错误必须透出(此前被吞,界面显示"迁移 0 条")
	cronSQL := func() string {
		cols := map[string]bool{}
		var colNames []string
		if raw, err := pythonJSON(fmt.Sprintf(
			`import sqlite3,json;c=sqlite3.connect('%s');print(json.dumps([r[1] for r in c.execute('PRAGMA table_info(crontab)')]))`, cronDbPath())); err == nil {
			_ = json.Unmarshal([]byte(raw), &colNames)
		}
		for _, c := range colNames {
			cols[c] = true
		}
		want := []string{"id", "name", "type", "sType", "where1", "where_hour", "where_minute", "week", "sBody", "status"}
		var sel []string
		for _, w := range want {
			if cols[w] {
				sel = append(sel, w)
			}
		}
		if !cols["name"] {
			return ""
		}
		// status 语义随宝塔版本漂移(旧 0=正常 / 11.x 1=启用),不过滤——
		// 全量列出由向导勾选,避免语义猜错漏掉正在用的任务
		return fmt.Sprintf(`import sqlite3,json;c=sqlite3.connect('%s');c.row_factory=sqlite3.Row;print(json.dumps([dict(r) for r in c.execute('select %s from crontab')]))`,
			cronDbPath(), strings.Join(sel, ","))
	}()
	if cronSQL == "" {
		res.Warnings = append(res.Warnings, "宝塔 crontab 表读取失败(无该表或 schema 异常),计划任务清单为空;站点迁移不受影响")
	}
	if cronSQL != "" {
		cronRaw, err := pythonJSON(cronSQL)
		if err != nil {
			res.Warnings = append(res.Warnings, "读取宝塔计划任务失败(不影响站点迁移): "+err.Error())
		} else {
			var crons []map[string]any
			if err := json.Unmarshal([]byte(cronRaw), &crons); err != nil {
				res.Warnings = append(res.Warnings, "解析宝塔计划任务失败: "+err.Error())
			} else {
				for _, cr := range crons {
					c := parseBTCron(cr)
					if v := strOf(cr, "id"); v != "" {
						c.ID = v
					}
					// 宝塔 11.8.1 的 crontab 表没有 sBody 列,命令存在
					// /www/server/cron/<id>/ 的脚本文件里,读不出来会全部误判"无法迁移"
					if strings.TrimSpace(c.Shell) == "" && c.ID != "" {
						if script, serr := btCronScript(c.ID); serr == nil && script != "" {
							c.Shell = script
							c.Mig = c.Spec != ""
							if c.Mig && strings.Contains(c.Cycle, "无法") {
								c.Cycle = strings.ReplaceAll(c.Cycle, "(无法自动迁移)", "")
							}
						}
					}
					res.Crons = append(res.Crons, c)
				}
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
		// enable-php-00 = 宝塔"纯静态,不启用 PHP"(00 不是版本号);
		// 此前误判为 "PHP 00 版本",找不到该版本会被随机接到别的 PHP 池
		if v := m[1]; v != "00" && v != "0" {
			site.PhpVer = v
			site.Type = "php"
		}
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
	// 证书目录存在即视为 SSL 站点(个别 conf 只听 80、靠 if 跳转,漏判会导致
	// 证书不迁移、续期不接管)
	if !site.SSL {
		name := site.CertName
		if name == "" {
			name = site.Domain
		}
		if _, err := os.Stat(filepath.Join(btCertDir, name, "fullchain.pem")); err == nil {
			site.SSL = true
			site.CertName = name
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
	Databases   []string // 可选:迁移宝塔数据库到面板 MySQL(导入后需改站点连接串)
}

// Apply 执行迁移(任务体内调用,进度写 w)。
// siteDone 回调每个站点完成时触发(域名, 面板站点ID)。
func Apply(opts ApplyOptions, w io.Writer, siteDone func(domain, siteID string)) error {
	InvalidateScanCache()
	res, err := Scan()
	if err != nil {
		return err
	}
	if !res.Found {
		return fmt.Errorf("未检测到宝塔面板(%s)", btPanelDir)
	}
	// php-fpm pool 访问控制(allowed_clients + iptables 限源,防任意容器直连 FastCGI)
	EnsurePoolSecurity(w)
	// Nginx 容器挂载宝塔站点目录(phpfpm 站点 root 保持宿主机路径)
	// websocket 变量全局定义(宝塔反代规则普遍引用 $connection_upgrade)
	ensureWSMap(w)
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
	var importedSites []ImportedSite
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
			// 面板 Nginx 跑在容器里,127.0.0.1/localhost 是容器自己,直接迁会整站 502
			proxyTo := rewriteLoopback(s.ProxyTo)
			if proxyTo != s.ProxyTo {
				fmt.Fprintf(w, "   反代目标已改写: %s → %s(容器内回环不可达宿主机)\n", s.ProxyTo, proxyTo)
				// 上游服务多半只监听 127.0.0.1,启动端口转发器让容器可达
				if m := regexp.MustCompile(`:(\d{1,5})`).FindStringSubmatch(s.ProxyTo); m != nil {
					if p, perr := strconv.Atoi(m[1]); perr == nil {
						RegisterProxyForward(w, p)
					}
				}
			}
			siteID, err = createProxySite(s.Domain, proxyTo, s.Port80())
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
		importedSites = append(importedSites, ImportedSite{Domain: s.Domain, SiteID: siteID, SSL: s.SSL && s.CertName != ""})
		// 自定义配置保真提取(反代子目录/websocket/自定义 location 等)
		extra, locs, xports, err := extractExtras(s.Conf, s.Type)
		if err == nil && strings.TrimSpace(extra) != "" {
			if err := saveExtras(siteID, extra); err == nil {
				fmt.Fprintf(w, "   自定义配置已保留(%d 个 location/指令块)\n", locs)
				// 反代子目录的所有回环上游端口一并注册端口转发
				for _, pn := range xports {
					RegisterProxyForward(w, pn)
				}
			} else {
				fmt.Fprintf(w, "   自定义配置保存失败(%v),站点基础功能不受影响\n", err)
			}
		}
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

	// 3) 计划任务(迁移成功后只注释"已迁移"的宝塔原 crontab 行,防止双跑;
	// 没迁移的宝塔任务照常运行)
	migratedCrons := 0
	var migratedCronIDs []string
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
			migratedCronIDs = append(migratedCronIDs, c.ID)
			n++
		}
		migratedCrons = n
		fmt.Fprintf(w, ">> 计划任务已迁移 %d 条\n", n)
		if n > 0 {
			disableBTCron(w, migratedCronIDs)
		}
	}

	// 3.5) 可选:迁移宝塔数据库到面板 MySQL
	for _, name := range opts.Databases {
		fmt.Fprintf(w, ">> 迁移数据库 %s ...\n", name)
		if err := MigrateBTDatabase(w, name); err != nil {
			fmt.Fprintf(w, "   失败: %v(继续)\n", err)
		}
	}

	// 4) Nginx 校验与重载(先不切换 80/443)
	if _, err := dockerx.ExecIn("kun-nginx", "nginx", "-t"); err != nil {
		return fmt.Errorf("Nginx 配置校验失败: %v", err)
	}
	_, _ = dockerx.ExecIn("kun-nginx", "nginx", "-s", "reload")
	// 记录迁移状态(供切换后证书续期接管与回滚恢复自启使用)
	st := loadState()
	st.Imported = importedSites
	st.MigratedCrons = migratedCrons
	saveState(st)
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

// cronDbPath 宝塔计划任务库路径:11.x 迁移至 data/db/crontab.db,旧版在
// data/default.db——按存在性探测,避免读错空库漏掉全部任务
func cronDbPath() string {
	if _, err := os.Stat(btCronDBNew); err == nil {
		return btCronDBNew
	}
	return btDBPath
}

// pythonJSON 用宝塔自带 pyenv python3 执行脚本并返回输出(schema 探测/读库统一入口)。
// pyenv 缺失时回退系统 python3,仍失败才报错(不再静默吞掉)
func pythonJSON(script string) (string, error) {
	var out string
	var err error
	if _, e := os.Stat(filepath.Join(btPanelDir, "pyenv", "bin", "python3")); e == nil {
		out, err = util.RunWithTimeout(15*time.Second,
			filepath.Join(btPanelDir, "pyenv", "bin", "python3"), "-c", script)
		if err == nil {
			return strings.TrimSpace(out), nil
		}
	}
	out2, err2 := util.RunWithTimeout(15*time.Second, "python3", "-c", script)
	if err2 != nil {
		return "", fmt.Errorf("pyenv 与系统 python3 均不可用: %v / %v", err, err2)
	}
	return strings.TrimSpace(out2), nil
}

func strOf(m map[string]any, k string) string {
	if v, ok := m[k]; ok && v != nil {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

// parseBTCron 把宝塔 crontab 行换算成面板计划任务。
// 类型字段兼容:sType(老版本)/ type(11.x);where1 按类型是「几号/N 分钟/URL」。
// 周类型 BT 的 week 字段 1~7 对应周一~周日,cron 里周日是 0(7%7)。
func parseBTCron(cr map[string]any) BtCron {
	name := strOf(cr, "name")
	styp := strOf(cr, "sType")
	if styp == "" {
		styp = strOf(cr, "type")
	}
	// 有些版本 type 存的是数字枚举(1=天 2=周 ...),带字母才算周期类型
	sbody := strings.TrimSpace(strOf(cr, "sBody"))
	where1 := strOf(cr, "where1")
	h := strOf(cr, "where_hour")
	m := strOf(cr, "where_minute")
	week := strOf(cr, "week")
	if h == "" {
		h = "0"
	}
	if m == "" {
		m = "0"
	}
	c := BtCron{Name: name, Shell: sbody}
	switch styp {
	case "day", "1":
		c.Cycle = "每天 " + h + ":" + m
		c.Spec = m + " " + h + " * * *"
	case "hour", "3":
		c.Cycle = "每小时第 " + m + " 分"
		c.Spec = m + " * * * *"
	case "nmonth", "month", "2":
		// 每月 where1 号执行(此前把 where1 丢了变成每天跑,已修)
		day := where1
		if day == "" {
			day = "1"
		}
		c.Cycle = "每月 " + day + " 日 " + h + ":" + m
		c.Spec = m + " " + h + " " + day + " * *"
	case "week", "4":
		dow := week
		if n, e := strconv.Atoi(week); e == nil && n >= 0 && n <= 7 {
			dow = strconv.Itoa(n % 7) // BT 1~7=周一~周日,cron 0=周日
		}
		if dow == "" {
			dow = "1"
		}
		names := map[string]string{"0": "日", "1": "一", "2": "二", "3": "三", "4": "四", "5": "五", "6": "六"}
		c.Cycle = "每周" + names[dow] + " " + h + ":" + m
		c.Spec = m + " " + h + " * * " + dow
	case "minute", "5", "n":
		// 每 N 分钟(where1=N)
		n := where1
		if n == "" {
			n = "30"
		}
		c.Cycle = "每 " + n + " 分钟"
		c.Spec = "*/" + n + " * * * *"
	case "day_n", "tz", "url":
		// 访问 URL/特殊类型:面板无法等价执行,保留展示
		c.Cycle = styp + "(宝塔专属类型,无法自动迁移)"
	default:
		c.Cycle = styp + "(无法自动迁移)"
	}
	c.Mig = c.Spec != "" && sbody != ""
	if !c.Mig && !strings.Contains(c.Cycle, "无法") {
		c.Cycle += "(无法自动迁移)"
	}
	return c
}

// ensureWSMap 确保面板 Nginx 全局存在 $connection_upgrade 的 map 定义。
// 宝塔站点反代规则普遍引用该变量(其定义在宝塔全局 0.websocket.conf),
// 面板镜像无此定义 → include 的规则迁入后 nginx -t 报未知变量。
// 文件名 k 开头,conf.d 按字母序展开时先于 site_* 站点配置;幂等
func ensureWSMap(w io.Writer) {
	dst := "/opt/kunpanel/data/websites/conf.d/kunmap.conf"
	if _, err := os.Stat(dst); err == nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	content := `# websocket 连接升级变量(由坤面板生成,供反代规则引用)
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
`
	if err := util.AtomicWriteString(dst, content, 0o644); err == nil {
		fmt.Fprintln(w, "   已生成 websocket 变量全局定义(conf.d/kunmap.conf)")
	}
}

// (此前只注册"反代类型站点的主上游",内联反代子目录的端口全部漏掉)
func registerExtraPorts(extra string, w io.Writer) {
	if strings.TrimSpace(extra) == "" {
		return
	}
	seen := map[int]bool{}
	for _, m := range regexp.MustCompile(`127\.0\.0\.1:(\d{2,5})`).FindAllStringSubmatch(extra, -1) {
		pn, err := strconv.Atoi(m[1])
		if err != nil || pn < 1 || pn > 65535 || seen[pn] {
			continue
		}
		seen[pn] = true
		RegisterProxyForward(w, pn)
	}
}

// btCronScript 宝塔 11.8.1 crontab 表无 sBody 时,从 /www/server/cron/<id>/
// 读取任务脚本,返回可执行命令("bash <脚本路径>")
func btCronScript(id string) (string, error) {
	dir := filepath.Join("/www/server/cron", id)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var script string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		script = filepath.Join(dir, e.Name())
		break
	}
	if script == "" {
		return "", fmt.Errorf("脚本不存在")
	}
	return "bash " + script, nil
}

// disableBTCron 迁移后注释宝塔原计划任务(/var/spool/cron/root 中引用
// /www/server/cron/ 的行),防止同一任务面板与宝塔各跑一遍。
// 注释保留原文,回滚(取消注释)即可恢复。
func disableBTCron(w io.Writer, migratedIDs []string) {
	// 只注释"已成功迁移"的任务:按 crontab 表 id 匹配 spool 行里的
	// /www/server/cron/<id>/;没迁移的宝塔任务照常运行——此前一刀切
	// 全部注释,把没迁移的任务也停了
	if len(migratedIDs) == 0 {
		return
	}
	for _, spool := range []string{"/var/spool/cron/root", "/var/spool/cron/crontabs/root"} {
		b, err := os.ReadFile(spool)
		if err != nil {
			continue
		}
		var lines []string
		changed := false
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.Contains(ln, "/www/server/cron/") && !strings.HasPrefix(strings.TrimSpace(ln), "#") {
				hit := false
				for _, id := range migratedIDs {
					if id != "" && strings.Contains(ln, "/www/server/cron/"+id+"/") {
						hit = true
						break
					}
				}
				if hit {
					lines = append(lines, "#kunmig# "+ln)
					changed = true
					continue
				}
			}
			lines = append(lines, ln)
		}
		if changed {
			if err := util.AtomicWrite(spool, []byte(strings.Join(lines, "\n")), 0o600); err == nil {
				fmt.Fprintf(w, "   已注释宝塔原 crontab 中的迁移任务(%s),避免双跑;回滚时去除 #kunmig# 前缀即可恢复\n", spool)
			}
		}
	}
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
	InvalidateScanCache()
	res, err := Scan()
	if err != nil {
		return err
	}
	if !res.Found {
		return fmt.Errorf("未检测到宝塔面板")
	}
	// 0) 刷新 php-fpm pool 访问控制(容器 IP 可能已变)
	EnsurePoolSecurity(w)
	// 1) 停宝塔 Nginx(systemd → init.d → 二进制逐级回退;仅停 nginx,不动 php-fpm/mysql)
	fmt.Fprintln(w, ">> 停止宝塔 Nginx(php-fpm 与 MySQL 保持运行)...")
	if err := stopBTNginx(w); err != nil {
		return err
	}
	// 1.5) 禁宝塔 Nginx 自启:服务器重启后不再与面板抢 80/443(回滚时恢复)
	st := loadState()
	if !st.AutostartOff {
		if off, via := disableBTNginxAutostart(w); off {
			st.AutostartOff, st.AutostartVia = true, via
			saveState(st)
		}
	}
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
				// 失败回滚:按原方式拉起宝塔 Nginx
				_ = startBTNginx(io.Discard)
				return fmt.Errorf("[%s] 容器重建失败,已恢复宝塔 Nginx: %s", key, tailStr(out, 300))
			}
		}
	}
	fmt.Fprintln(w, ">> 流量已切换到面板 Nginx。如需回滚,请使用「回滚到宝塔」。")
	// 记录切换状态
	st2 := loadState()
	st2.Switched = true
	saveState(st2)
	// 证书续期接管:迁移导入的宝塔证书改由面板 ACME 重签并自动续期
	RenewMigratedSites(w)
	return nil
}

// RollbackTraffic 回滚:停面板 Nginx 容器 → 恢复端口 → 启动宝塔 Nginx
func RollbackTraffic(w io.Writer) error {
	InvalidateScanCache()
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
	fmt.Fprintln(w, ">> 启动宝塔 Nginx(systemd/init.d 优先)...")
	if err := startBTNginx(w); err != nil {
		return fmt.Errorf("启动宝塔 Nginx 失败: %v", err)
	}
	// 恢复宝塔 Nginx 自启
	st := loadState()
	if st.AutostartOff {
		restoreBTNginxAutostart(w, st.AutostartVia)
		st.AutostartOff, st.AutostartVia = false, ""
	}
	st.Switched = false
	saveState(st)
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
