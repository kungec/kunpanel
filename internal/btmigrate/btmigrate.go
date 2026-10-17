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
	"errors"
	"fmt"
	"io"
	"net"
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
	"kunpanel/internal/cronx"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
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
	Domain   string `json:"domain"`  // 主域名(server_name 第一个)
	Aliases  string `json:"aliases"` // 完整 server_name
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
	ID    string `json:"id,omitempty"`   // 宝塔 crontab 表 id
	Echo  string `json:"echo,omitempty"` // crontab 表 md5 列;11.x 脚本文件即 /www/server/cron/<echo>
	Name  string `json:"name"`
	Shell string `json:"shell"`
	Cycle string `json:"cycle"`
	Spec  string `json:"spec,omitempty"` // 标准 5 位 cron(可迁移时生成)
	Mig   bool   `json:"mig"`            // true=可自动迁移
	// Enabled 宝塔侧启用状态(11.x crontab 表 status=1 为启用;老 schema
	// sType 存在时 status 语义相反,0=正常)。迁移后面板任务保持原状态
	Enabled bool `json:"enabled"`
	// Internal 宝塔内部任务:依赖宝塔自身(/www/server/panel 脚本、database/
	// site/path 专属类型),迁移出去大概率不可用,默认不勾选迁移
	Internal bool `json:"internal"`
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
	scanMu      sync.Mutex
	scanCache   *ScanResult
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

	// 数据库清单:11.x 在 data/db/database.db,default.db 里是同名空表——
	// btTableDB 按库探测,错误透出不静默
	dbsDB, dbDBErr := btTableDB("databases")
	if dbDBErr != nil {
		res.Warnings = append(res.Warnings, "读取宝塔数据库清单失败(不影响站点迁移): "+dbDBErr.Error())
	} else if out, err := pythonJSON(fmt.Sprintf(
		`import sqlite3,json;c=sqlite3.connect('%s');c.row_factory=sqlite3.Row;print(json.dumps([dict(r) for r in c.execute('select name,username as user,ps from databases')]))`, dbsDB)); err != nil {
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

	// 计划任务:11.x 在 data/db/crontab.db,default.db 里同名表是空的——
	// btTableDB 探测。宝塔各版本 schema 不一致:老版本有 sType,11.x 起只有
	// type 字段;echo 列是脚本文件名(md5)。用 PRAGMA 先探测可用列再拼查询,
	// 查询错误必须透出(此前被吞,界面显示"迁移 0 条")
	cronDB, cronDBErr := btTableDB("crontab")
	cronSQL := ""
	if cronDBErr != nil {
		res.Warnings = append(res.Warnings, "宝塔 crontab 表读取失败(无该表或 schema 异常),计划任务清单为空;站点迁移不受影响: "+cronDBErr.Error())
	} else {
		cronSQL = func() string {
			cols := map[string]bool{}
			var colNames []string
			if raw, err := pythonJSON(fmt.Sprintf(
				`import sqlite3,json;c=sqlite3.connect('%s');print(json.dumps([r[1] for r in c.execute('PRAGMA table_info(crontab)')]))`, cronDB)); err == nil {
				_ = json.Unmarshal([]byte(raw), &colNames)
			}
			for _, c := range colNames {
				cols[c] = true
			}
			want := []string{"id", "name", "type", "sType", "where1", "where_hour", "where_minute", "week", "sBody", "status", "echo"}
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
				cronDB, strings.Join(sel, ","))
		}()
		if cronSQL == "" {
			res.Warnings = append(res.Warnings, "宝塔 crontab 表读取失败(无该表或 schema 异常),计划任务清单为空;站点迁移不受影响")
		}
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
					c.Echo = strOf(cr, "echo")
					// 宝塔 11.8.1 的 crontab 表没有 sBody 列,命令存在单文件
					// /www/server/cron/<echo> 里,读不出来会全部误判"无法迁移"
					if strings.TrimSpace(c.Shell) == "" && c.Echo != "" {
						script, serr := btCronScript(c.Echo)
						if serr != nil {
							res.Warnings = append(res.Warnings, "读取计划任务["+c.Name+"]脚本失败("+serr.Error()+"),标记为仅展示")
						} else if script != "" {
							c.Shell = script
							// 脚本内容引用宝塔内部路径同样视为内部任务
							if strings.Contains(script, "/www/server/panel/") {
								c.Internal = true
							}
							c.Mig = c.Spec != "" && !c.Internal
							if c.Mig && strings.Contains(c.Cycle, "无法") {
								c.Cycle = strings.ReplaceAll(c.Cycle, "(无法自动迁移)", "")
								c.Cycle = strings.ReplaceAll(c.Cycle, "(宝塔内部任务,默认不迁移)", "")
							}
						}
					}
					if c.Internal && !strings.Contains(c.Cycle, "默认不迁移") && !strings.Contains(c.Cycle, "无法") {
						c.Cycle += "(宝塔内部任务,默认不迁移)"
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
	Crons       []string // 要迁移的计划任务(echo 列,空回退任务名);空列表=迁移全部可迁移任务(兼容旧调用)
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
			// pool 可达性校验:以网关容器视角为准——fpm 的 allowed_clients 与
			// KUNFPM 链只放行容器 IP,宿主机侧拨号会被拒,不能作为校验依据;
			// 宝塔 php-fpm 未运行/reload 失败时容器内也连不上,该站 PHP 将 502
			if err := waitPoolReachable(plan.Port, 8*time.Second); err != nil {
				fmt.Fprintf(w, "   警告: PHP %s 进程池 %s:%d 不可达(%v),相关站点 PHP 请求将 502;请在宝塔启动 php-fpm 后重试迁移\n", ver, poolGateway, plan.Port, err)
			} else {
				fmt.Fprintf(w, "   PHP %s 进程池已就绪(%s:%d,容器内可达)\n", ver, poolGateway, plan.Port)
			}
			reloaded[ver] = true
		}
	}

	// 2) 逐站点迁移:先验证反代上游可达,再建站,每站迁完即跑 nginx -t,
	//    失败撤销该站全部产物后继续下一个(不留残骸拖垮整批)
	var importedSites []ImportedSite
	for _, s := range res.Sites {
		if !sel[s.Domain] {
			continue
		}
		siteID, err := migrateOneSite(s, res.FPMs, opts, pools, w)
		if err != nil {
			if strings.Contains(err.Error(), "站点已存在") {
				fmt.Fprintf(w, "   %s 已在面板中(跳过,不覆盖)\n", s.Domain)
				continue
			}
			fmt.Fprintf(w, "   站点 %s 迁移中止: %v(继续迁移其余站点)\n", s.Domain, err)
			continue
		}
		importedSites = append(importedSites, ImportedSite{
			Domain: s.Domain, SiteID: siteID, SSL: s.SSL && s.CertName != "",
			// 记录 location 前缀路径,切换流量的逐路径验证据此覆盖反代子路径
			Paths: probePathsFromConf(s.Conf),
		})
		if siteDone != nil {
			siteDone(s.Domain, siteID)
		}
	}
	_ = importedSites

	// 3) 计划任务(迁移成功后只注释"已迁移"的宝塔原 crontab 行,防止双跑;
	// 没迁移的宝塔任务照常运行)。opts.Crons 为空时迁全部可迁移任务(兼容旧调用)
	migratedCrons := 0
	var migratedCronEchoes []string
	if opts.MigrateCron {
		n := 0
		for _, c := range res.Crons {
			if len(opts.Crons) > 0 && !cronSelected(opts.Crons, c) {
				continue
			}
			if !c.Mig || strings.TrimSpace(c.Shell) == "" || strings.TrimSpace(c.Spec) == "" {
				if len(opts.Crons) > 0 {
					fmt.Fprintf(w, "   计划任务[%s]无法自动迁移(%s),已跳过\n", c.Name, c.Cycle)
				}
				continue
			}
			if _, err := addCron(c); err != nil {
				fmt.Fprintf(w, "   计划任务[%s]迁移失败: %v\n", c.Name, err)
				continue
			}
			migratedCronEchoes = append(migratedCronEchoes, c.Echo)
			n++
		}
		migratedCrons = n
		fmt.Fprintf(w, ">> 计划任务已迁移 %d 条\n", n)
		if n > 0 {
			disableBTCron(w, migratedCronEchoes)
		}
	}

	// 3.5) 可选:迁移宝塔数据库到面板 MySQL
	for _, name := range opts.Databases {
		fmt.Fprintf(w, ">> 迁移数据库 %s ...\n", name)
		if err := MigrateBTDatabase(w, name); err != nil {
			fmt.Fprintf(w, "   失败: %v(继续)\n", err)
		}
	}

	// 4) Nginx 校验与重载(先不切换 80/443)。
	// 网关容器名不写死:只装 OpenResty 时 kun-nginx 不存在,写死必失败
	gw, gwErr := panelGatewayContainer()
	if gwErr != nil {
		return gwErr
	}
	if out, err := dockerx.ExecIn(gw, "nginx", "-t"); err != nil {
		return fmt.Errorf("Nginx 配置校验失败: %v: %s", err, tailStr(out, 200))
	}
	if out, err := dockerx.ExecIn(gw, "nginx", "-s", "reload"); err != nil {
		return fmt.Errorf("Nginx 重载失败: %v: %s", err, tailStr(out, 200))
	}
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

// waitPoolListening 等待 fpm pool 端口在本机出现监听(reload 后 fpm 需要短暂
// 时间;仅宿主机网关模式使用——容器网关场景请用 waitPoolReachable)
func waitPoolListening(port int, timeout time.Duration) error {
	addr := net.JoinHostPort(poolGateway, strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	for {
		if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 %s 监听超时(fpm 未运行或 reload 失败)", addr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// waitPoolReachable 以网关容器视角等待 pool 端口可连(KUNFPM 链与 fpm
// allowed_clients 只放行容器 IP,宿主机侧拨号必然被拒,不能作为校验依据)。
// 纯 TCP 探测即可:fpm 说的是 FastCGI,curl 首选失败后自然落到 bash 兜底。
func waitPoolReachable(port int, timeout time.Duration) error {
	mode := websites.DetectNginx()
	if mode.Mode != "container" || mode.Target == "" {
		return waitPoolListening(port, timeout) // 宿主机网关:本机直拨即为消费者视角
	}
	deadline := time.Now().Add(timeout)
	probe := fmt.Sprintf(`timeout 2 bash -c "exec 3<>/dev/tcp/%s/%d" 2>/dev/null && echo POOL_OK`, poolGateway, port)
	for {
		if out, err := dockerx.ExecIn(mode.Target, "sh", "-c", probe); err == nil && strings.Contains(out, "POOL_OK") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("网关容器内无法连通 %s:%d(php-fpm 未运行/reload 失败/allowed_clients 未含容器 IP)", poolGateway, port)
		}
		time.Sleep(700 * time.Millisecond)
	}
}

// cronSelected 判断任务是否被勾选迁移(按 echo 匹配;无 echo 的老库回退任务名)
func cronSelected(sel []string, c BtCron) bool {
	for _, want := range sel {
		if want == "" {
			continue
		}
		if (c.Echo != "" && want == c.Echo) || (c.Echo == "" && want == c.Name) {
			return true
		}
	}
	return false
}

// panelGatewayContainer 面板网关容器名(DetectNginx 判定)。
// 此前写死 kun-nginx,只装 OpenResty 时迁移必然失败。
func panelGatewayContainer() (string, error) {
	mode := websites.DetectNginx()
	if mode.Mode == "container" && mode.Target != "" {
		return mode.Target, nil
	}
	if mode.Mode == "host" {
		return "", fmt.Errorf("面板 Nginx 运行在宿主机(非容器),宝塔迁移需要容器网关承载宝塔站点;请先在应用商店安装 Nginx/OpenResty")
	}
	return "", fmt.Errorf("未检测到面板网关容器,请先在应用商店安装并启动 Nginx/OpenResty")
}

// migrateOneSite 迁移单个站点:提取自定义配置 → 验证全部反代上游可达(不
// 可达不产生任何产物)→ 建站 → 落 extras/伪静态/证书 → 单站 nginx -t
// (失败撤销该站全部产物)。返回错误时调用方跳到下一站。
func migrateOneSite(s BtSite, fpms []BtFPM, opts ApplyOptions, pools map[string]FpmPoolPlan, w io.Writer) (string, error) {
	fmt.Fprintf(w, ">> 迁移站点 %s(%s)...\n", s.Domain, s.Type)

	// 0) 先提取并验证,失败不产生半成品
	extra, locs, xports, xerr := extractExtras(s.Conf, s.Type)
	if xerr != nil {
		fmt.Fprintf(w, "   自定义配置提取失败(%v),仅迁移基础配置\n", xerr)
	}
	upPorts := xports
	if s.Type == "proxy" {
		if p, ok := loopbackUpstreamPort(s.ProxyTo); ok {
			upPorts = append([]int{p}, upPorts...)
		}
	}
	for _, p := range upPorts {
		if err := RegisterProxyForward(w, p); err != nil {
			return "", fmt.Errorf("反代上游端口 %d 不可用: %v", p, err)
		}
	}

	// 1) 建站
	var siteID string
	var err error
	switch s.Type {
	case "php":
		plan := pools[s.PhpVer]
		var fpmAddr string
		if plan.Port > 0 {
			fpmAddr = fmt.Sprintf("%s:%d", poolGateway, plan.Port)
		} else if f := findFPM(fpms, s.PhpVer); f != nil {
			fpmAddr = "unix:" + f.Sock // 无 pool 时原 socket(容器内不可达,仅占位)
		} else {
			fpmAddr = fmt.Sprintf("%s:%d", poolGateway, poolBase+1)
		}
		siteID, err = createFpmSite(s.Domain, s.Root, fpmAddr, s.PhpVer)
	case "proxy":
		// 面板 Nginx 跑在容器里,127.0.0.1/localhost 是容器自己,直接迁会整站 502
		proxyTo := rewriteLoopback(s.ProxyTo)
		if proxyTo != s.ProxyTo {
			fmt.Fprintf(w, "   反代目标已改写: %s → %s(容器内回环不可达宿主机)\n", s.ProxyTo, proxyTo)
		}
		siteID, err = createProxySite(s.Domain, proxyTo, s.Port80())
	default:
		siteID, err = createStaticSite(s.Domain, s.Root)
	}
	if err != nil {
		return "", err
	}

	// 2) 自定义配置落盘。SetSiteRawExtra 只写 extras 文件、不重新生成站点
	//    conf——此前从未调用 RefreshSiteConf,extras 一直没进站点配置:
	//    nginx -t 测的是不含 extras 的空壳(坏指令漏检),迁移的规则也不生效
	if xerr == nil && strings.TrimSpace(extra) != "" {
		if serr := saveExtras(siteID, extra); serr != nil {
			fmt.Fprintf(w, "   自定义配置保存失败(%v),站点基础功能不受影响\n", serr)
		} else if rerr := websites.RefreshSiteConf(siteID); rerr != nil {
			fmt.Fprintf(w, "   自定义配置注入站点 conf 失败(%v)\n", rerr)
		} else {
			fmt.Fprintf(w, "   自定义配置已保留并生效(%d 个 location/指令块)\n", locs)
		}
	}
	// 3) 伪静态
	if s.Rewrite != "" {
		if rw, rerr := os.ReadFile(s.Rewrite); rerr == nil && len(strings.TrimSpace(string(rw))) > 0 {
			if werr := util.AtomicWrite(filepath.Join("/opt/kunpanel/data/websites/rewrite", siteID+".conf"), rw, 0o644); werr != nil {
				fmt.Fprintf(w, "   伪静态写入失败(%v)\n", werr)
			} else {
				if rerr := websites.RefreshSiteConf(siteID); rerr != nil {
					fmt.Fprintf(w, "   伪静态注入站点 conf 失败(%v)\n", rerr)
				} else {
					fmt.Fprintf(w, "   伪静态已迁移(%s)\n", filepath.Base(s.Rewrite))
				}
			}
		}
	}
	// 4) 证书
	if opts.ImportCerts && s.SSL && s.CertName != "" {
		crt := filepath.Join(btCertDir, s.CertName, "fullchain.pem")
		key := filepath.Join(btCertDir, s.CertName, "privkey.pem")
		if cid, cerr := importCert(s.CertName, crt, key); cerr != nil {
			fmt.Fprintf(w, "   证书导入失败(%v),站点可稍后手动绑定\n", cerr)
		} else {
			bindSiteSSL(siteID, cid)
			fmt.Fprintf(w, "   证书已导入并绑定\n")
		}
	}
	// 5) 单站配置校验:此前整批迁完才跑一次 nginx -t,一个坏站拖垮整批且
	//    失败后留下全部半成品;现在每站即测即撤
	gw, gwErr := panelGatewayContainer()
	if gwErr != nil {
		undoSiteProducts(siteID, w)
		return "", gwErr
	}
	if out, terr := dockerx.ExecIn(gw, "nginx", "-t"); terr != nil {
		fmt.Fprintf(w, "   nginx -t 未通过,撤销站点 %s 的全部产物: %s\n", s.Domain, tailStr(out, 200))
		undoSiteProducts(siteID, w)
		return "", fmt.Errorf("站点 %s 配置校验未通过", s.Domain)
	}
	fmt.Fprintf(w, "   站点 %s 迁移完成(nginx -t 通过)\n", s.Domain)
	return siteID, nil
}

// undoSiteProducts 撤销单站点迁移产物:站点记录与 conf、额外配置、伪静态
// (websites.Delete 一并清理,不动站点根目录——宝塔原文件绝不能碰)。
// 证书保留在证书库(无站点引用无副作用)。
// 注意:Delete 在清完产物后才 Reload;重载被"另一个站点的坏配置"(正是本
// 流程要修的场景)阻塞时,产物其实已经清理干净——查实后按成功处理。
func undoSiteProducts(siteID string, w io.Writer) {
	err := websites.Delete(siteID)
	if err != nil {
		if _, gerr := websites.GetSite(siteID); gerr != nil {
			fmt.Fprintf(w, "   站点 %s 产物已清理(重载被其他站点配置问题阻塞,最终校验会暴露)\n", siteID)
			return
		}
		fmt.Fprintf(w, "   撤销站点 %s 失败: %v(请在网站页手动删除)\n", siteID, err)
		return
	}
	fmt.Fprintf(w, "   已撤销站点 %s(记录/配置/伪静态)\n", siteID)
}

// cronDBPathDeprecated 已由 btTableDB 取代(dbmig.go):11.x 数据库按表拆分,
// 必须逐表探测所在库,不能再按固定路径猜。

// btTableDB 宝塔 11.x 起数据库拆分:crontab→data/db/crontab.db、databases→
// data/db/database.db、config→data/db/panel.db;default.db 里的同名表是空的
// 旧库。按「data/db/<对应库>.db → data/default.db」顺序探测,返回第一个
// 表存在且非空的库。此前固定读 default.db:数据库清单恒为空、MySQL root
// 密码丢失、计划任务清单恒空,全部同根因。
func btTableDB(table string) (string, error) {
	safe := sanitizeIdent(table)
	fileNames := map[string]string{"crontab": "crontab.db", "databases": "database.db", "config": "panel.db"}
	fname := fileNames[table]
	if fname == "" {
		fname = safe + ".db"
	}
	var tried []string
	for _, db := range []string{filepath.Join(btPanelDir, "data", "db", fname), btDBPath} {
		if _, err := os.Stat(db); err != nil {
			continue
		}
		out, err := pythonJSON(fmt.Sprintf(`import sqlite3,json
c=sqlite3.connect('%s')
try:
    n=list(c.execute('select count(*) from %s'))[0][0]
except Exception:
    n=-1
print(json.dumps(n))`, db, safe))
		if err != nil {
			tried = append(tried, db+"("+err.Error()+")")
			continue
		}
		var n int
		if json.Unmarshal([]byte(strings.TrimSpace(out)), &n) != nil {
			tried = append(tried, db+"(行数解析失败)")
			continue
		}
		if n > 0 {
			return db, nil
		}
		tried = append(tried, fmt.Sprintf("%s(表空或不存在)", db))
	}
	return "", fmt.Errorf("没有找到含数据的 %s 表,已尝试: %s", table, strings.Join(tried, "; "))
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
	// 启用状态:11.x(无 sType 列)status=1 为启用;老 schema(sType 存在)
	// status 语义相反,0=正常。列缺失时默认启用
	if st, ok := cr["status"]; ok && st != nil {
		sts := strings.TrimSpace(fmt.Sprint(st))
		if _, oldSchema := cr["sType"]; oldSchema {
			c.Enabled = sts == "0"
		} else {
			c.Enabled = sts == "1"
		}
	} else {
		c.Enabled = true
	}
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
	// 宝塔内部任务:依赖宝塔自身(面板脚本/备份等),迁移出去大概率不可用,
	// 默认不迁移、界面说明;database/site/path 类型同理
	c.Internal = styp == "database" || styp == "site" || styp == "path" ||
		strings.Contains(sbody, "/www/server/panel/")
	if c.Internal {
		c.Mig = false
	}
	if !c.Mig && !strings.Contains(c.Cycle, "无法") && !c.Internal {
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

// (端口转发注册统一走 RegisterProxyForward / extras 提取的回环上游端口,
// 旧 registerExtraPorts 对整段文本扫 :数字 会把 Alt-Svc 的 :443 也当端口,已删)

// btCronScript 宝塔 11.x 计划任务脚本是单文件 /www/server/cron/<echo>
// (echo 为 crontab 表 md5 列;老版本才是 /www/server/cron/<id>/ 目录)。
// 返回脚本内容作为面板任务命令(自包含,不依赖宝塔目录后续存在)。
func btCronScript(echo string) (string, error) {
	if echo == "" || strings.ContainsAny(echo, "/\\") || strings.Contains(echo, "..") {
		return "", fmt.Errorf("非法 echo 文件名")
	}
	b, err := os.ReadFile(filepath.Join("/www/server/cron", echo))
	if err != nil {
		return "", err
	}
	script := strings.TrimSpace(string(b))
	if script == "" {
		return "", fmt.Errorf("脚本文件为空")
	}
	return script, nil
}

// echoMatchers 构造 spool 行匹配器:/www/server/cron/<echo> 后必须跟非字母
// 数字(整词),防止 md5 前缀撞车误注释别人的行
func echoMatchers(echos []string) []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(echos))
	for _, echo := range echos {
		if echo == "" {
			continue
		}
		patterns = append(patterns, regexp.MustCompile(`/www/server/cron/`+regexp.QuoteMeta(echo)+`([^0-9a-zA-Z]|$)`))
	}
	return patterns
}

// disableBTCron 迁移后注释宝塔原计划任务(/var/spool/cron/root 中引用
// /www/server/cron/ 的行),防止同一任务面板与宝塔各跑一遍。
// 按 /www/server/cron/<echo> 整词匹配(11.x 布局:echo 是 md5 文件名,
// 行内同时出现 <echo>.lock 与 <echo>.log;按表 id 匹配对不上会漏注释,
// 已迁移任务双跑)。注释保留原文,回滚(取消注释)即可恢复。
func disableBTCron(w io.Writer, migratedEchoes []string) {
	if len(migratedEchoes) == 0 {
		return
	}
	patterns := echoMatchers(migratedEchoes)
	if len(patterns) == 0 {
		return
	}
	for _, spool := range []string{"/var/spool/cron/root", "/var/spool/cron/crontabs/root"} {
		b, err := os.ReadFile(spool)
		if err != nil {
			continue
		}
		var lines []string
		changed := false
		commented := 0
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.Contains(ln, "/www/server/cron/") && !strings.HasPrefix(strings.TrimSpace(ln), "#") {
				hit := false
				for _, re := range patterns {
					if re.MatchString(ln) {
						hit = true
						break
					}
				}
				if hit {
					lines = append(lines, "#kunmig# "+ln)
					changed = true
					commented++
					continue
				}
			}
			lines = append(lines, ln)
		}
		if changed {
			if err := util.AtomicWrite(spool, []byte(strings.Join(lines, "\n")), 0o600); err == nil {
				fmt.Fprintf(w, "   已注释宝塔原 crontab 中的 %d 条已迁移任务(%s),避免双跑;回滚时去除 #kunmig# 前缀即可恢复\n", commented, spool)
			} else {
				fmt.Fprintf(w, "   警告: 注释宝塔 crontab 失败(%v),已迁移任务可能与宝塔双跑,请手动注释\n", err)
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

// addCron 迁移计划任务为面板 shell 任务(spec 由 BT 周期换算),
// 保留宝塔侧的启用/停用状态(此前写死启用,停用任务迁完立即开跑)。
func addCron(c BtCron) (string, error) {
	job, err := cronx.Add(cronx.Job{
		Name:      "BT-" + c.Name,
		Type:      "shell",
		Spec:      c.Spec,
		Command:   c.Shell,
		Enabled:   true,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	})
	if err != nil {
		return "", err
	}
	if !c.Enabled {
		if terr := cronx.Toggle(job.ID, false); terr != nil {
			return job.ID, fmt.Errorf("任务已迁入但恢复停用状态失败: %v", terr)
		}
	}
	return job.ID, nil
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

// soleGatewayKey 面板网关容器必须唯一:两个网关同时改绑 80/443 会互相抢占,
// 先起的一个占住端口、另一个启动失败,面板网站随机 502。
func soleGatewayKey() (string, error) {
	var running []string
	for _, name := range []string{"kun-nginx", "kun-openresty"} {
		out, err := exec.Command("docker", "ps", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}").CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			running = append(running, name)
		}
	}
	switch len(running) {
	case 1:
		return running[0], nil
	case 0:
		return "", fmt.Errorf("未检测到运行中的面板网关容器(kun-nginx/kun-openresty),请先在应用商店安装并启动")
	default:
		return "", fmt.Errorf("检测到多个运行中的网关容器(%s):会互相抢占 80/443,请停止多余的只保留一个", strings.Join(running, "、"))
	}
}

// containerPublishedPorts 读容器实际发布的宿主端口(以 docker 运行态为准,
// 不再靠正则猜 compose 里 4~5 位的旧端口)。
func containerPublishedPorts(name string) (p80, p443 int, err error) {
	out, err := exec.Command("docker", "ps", "--filter", "name=^/"+name+"$", "--format", "{{.Ports}}").CombinedOutput()
	if err != nil {
		return 0, 0, err
	}
	for _, m := range regexp.MustCompile(`(?:0\.0\.0\.0|\[::\]):(\d+)->(\d+)`).FindAllStringSubmatch(string(out), -1) {
		host, _ := strconv.Atoi(m[1])
		cont, _ := strconv.Atoi(m[2])
		if cont == 80 {
			p80 = host
		}
		if cont == 443 {
			p443 = host
		}
	}
	return p80, p443, nil
}

var (
	reComposePort80  = regexp.MustCompile(`(?m)^(\s*-\s*"?)([0-9]{1,5})(:80(/[a-z]+)?"?)\s*$`)
	reComposePort443 = regexp.MustCompile(`(?m)^(\s*-\s*"?)([0-9]{1,5})(:443(/[a-z]+)?"?)\s*$`)
	reComposeBind80  = regexp.MustCompile(`(?m)^(\s*-\s*"?)(80)(:80(/[a-z]+)?"?)\s*$`)
	reComposeBind443 = regexp.MustCompile(`(?m)^(\s*-\s*"?)(443)(:443(/[a-z]+)?"?)\s*$`)
)

// rewriteComposePorts 把 compose 里 80/443 的宿主端口(任意位数、带不带引号、
// 带不带协议后缀)改绑为 80/443,返回新内容与原端口。幂等:已是 80/443 的行
// 跳过,重复执行 changed=false。
func rewriteComposePorts(content string) (string, int, int, bool) {
	orig80, orig443 := 0, 0
	out := reComposePort80.ReplaceAllStringFunc(content, func(m0 string) string {
		m := reComposePort80.FindStringSubmatch(m0)
		if m == nil {
			return m0
		}
		if n, e := strconv.Atoi(m[2]); e == nil && n != 80 {
			if orig80 == 0 {
				orig80 = n
			}
			return m[1] + "80" + m[3]
		}
		return m0
	})
	out = reComposePort443.ReplaceAllStringFunc(out, func(m0 string) string {
		m := reComposePort443.FindStringSubmatch(m0)
		if m == nil {
			return m0
		}
		if n, e := strconv.Atoi(m[2]); e == nil && n != 443 {
			if orig443 == 0 {
				orig443 = n
			}
			return m[1] + "443" + m[3]
		}
		return m0
	})
	return out, orig80, orig443, orig80 != 0 || orig443 != 0
}

// restoreComposePorts 把 compose 的宿主端口改回切换前记录的原值(只改文件)。
func restoreComposePorts(key, orig string, w io.Writer) error {
	parts := strings.SplitN(orig, "/", 2)
	o80, _ := strconv.Atoi(parts[0])
	o443 := 443
	if len(parts) > 1 {
		o443, _ = strconv.Atoi(parts[1])
	}
	dir := appComposeDir(key)
	b, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		return err
	}
	content := string(b)
	if o80 > 0 && o80 != 80 {
		content = reComposeBind80.ReplaceAllString(content, "${1}"+strconv.Itoa(o80)+"${3}")
	}
	if o443 > 0 && o443 != 443 {
		content = reComposeBind443.ReplaceAllString(content, "${1}"+strconv.Itoa(o443)+"${3}")
	}
	if err := util.AtomicWriteString(filepath.Join(dir, "docker-compose.yml"), content, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "   [%s] compose 端口已还原为 %s\n", key, orig)
	return nil
}

// appComposeDir 网关应用的 compose 目录:容器名(kun-nginx)与安装 key(nginx)
// 差一个前缀,且新版装在 /opt/kunpanel/apps、旧版在数据目录下——组合探测,
// 按 compose 文件存在性返回第一个命中,老机器切换/回滚才不会落空
func appComposeDir(container string) string {
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

// httpProbe 用 --resolve 把域名指到本机探测状态码(不依赖 DNS 已切换;
// SSL 站点走 https -k,与切换前后两侧的服务形态对齐)。
func httpProbe(domain, path string, https bool) string {
	scheme, port := "http", "80"
	if https {
		scheme, port = "https", "443"
	}
	out, err := util.Run("curl", "-sk", "-o", "/dev/null", "-w", "%{http_code}",
		"--max-time", "8", "--resolve", net.JoinHostPort(domain, port)+":127.0.0.1",
		scheme+"://"+domain+path)
	if err != nil {
		return "000"
	}
	code := strings.TrimSpace(out)
	if len(code) != 3 {
		return "000"
	}
	return code
}

// probeItem 一条探测项:域名 × 路径 × 协议 → 切换前状态码基线
type probeItem struct {
	domain string
	path   string
	https  bool
	code   string
}

// captureBaseline 切换前逐域名×逐路径记录状态码(宝塔正在服务),切换后逐一比对。
// 只比对宝塔认识的域名(btSites):面板原生站点(multi82 这类自建站)宝塔
// 从未用同名 vhost 服务过,其"基线"是宝塔默认服务器的响应——部分迁移场景
// 下与面板必然不一致,会把切换误判失败触发回滚。
// 路径来自迁移时记录的 location 前缀(imported paths),反代子路径同样覆盖,
// 不只测首页
func captureBaseline(w io.Writer, btSites []BtSite, imported []ImportedSite) []probeItem {
	pathsByDomain := map[string][]string{}
	for _, im := range imported {
		if len(im.Paths) > 0 {
			pathsByDomain[im.Domain] = im.Paths
		}
	}
	btDomains := map[string]BtSite{}
	for _, s := range btSites {
		btDomains[s.Domain] = s
	}
	var base []probeItem
	all, err := websites.List()
	if err != nil {
		fmt.Fprintf(w, "   警告: 读取面板站点失败(%v),切换后将无法做逐域名比对\n", err)
		return base
	}
	for _, s := range all {
		if !s.Enabled || s.Domain == "" {
			continue
		}
		d := strings.Fields(s.Domain)[0]
		bt, known := btDomains[d]
		if !known {
			continue
		}
		https := bt.SSL // 以宝塔侧是否 SSL 站决定探测协议,与切换前服务形态对齐
		paths := pathsByDomain[d]
		if len(paths) == 0 {
			paths = []string{"/"}
		}
		for _, p := range paths {
			base = append(base, probeItem{domain: d, path: p, https: https, code: httpProbe(d, p, https)})
		}
	}
	fmt.Fprintf(w, ">> 已采集 %d 个域名×路径的切换前状态码基线\n", len(base))
	return base
}

// verifySwitched 切换后验证:80/443 必须由面板网关容器监听、宿主机不能残留
// nginx 进程占用、逐域名状态码与切换前一致。任何一项失败返回错误(自动回滚)。
func verifySwitched(w io.Writer, gwKey string, baseline []probeItem) error {
	p80, p443, err := containerPublishedPorts(gwKey)
	if err != nil {
		return fmt.Errorf("读取 %s 端口映射失败: %v", gwKey, err)
	}
	if p80 != 80 {
		return fmt.Errorf("切换后 %s 未监听宿主 80(当前映射 %v)", gwKey, p80)
	}
	if p443 != 443 {
		return fmt.Errorf("切换后 %s 未监听宿主 443(当前映射 %v)", gwKey, p443)
	}
	// 宿主机 80/443 只能由 docker-proxy 持有;宝塔 nginx 必须已退场
	if out, err := exec.Command("sh", "-c", "ss -tlnp 2>/dev/null | grep -E ':(80|443) '").CombinedOutput(); err == nil {
		for _, ln := range strings.Split(string(out), "\n") {
			l := strings.ToLower(ln)
			if (strings.Contains(l, ":80 ") || strings.Contains(l, ":443 ")) && strings.Contains(l, "nginx") {
				return fmt.Errorf("宿主机 80/443 仍被 nginx 进程持有: %s", tailStr(ln, 150))
			}
		}
	}
	// 逐域名×逐路径比对(基线取不到的项跳过——切换前就不通的无从比对)
	var mism []string
	for _, want := range baseline {
		if want.code == "" || want.code == "000" {
			continue
		}
		got := httpProbe(want.domain, want.path, want.https)
		if got != want.code {
			mism = append(mism, fmt.Sprintf("%s%s:切换前 %s → 切换后 %s", want.domain, want.path, want.code, got))
		}
	}
	if len(mism) > 0 {
		return fmt.Errorf("%d 个域名状态码与切换前不一致: %s", len(mism), strings.Join(mism, "; "))
	}
	fmt.Fprintf(w, ">> 验证通过:80/443 已由 %s 监听,各域名×路径状态码与切换前一致\n", gwKey)
	return nil
}

// rollbackAfterFailedSwitch 切换失败回滚:恢复端口映射并重建面板网关(保持
// 面板自身可用)、拉起宝塔 Nginx、恢复其自启,最后再次确认 80/443 归属。
func rollbackAfterFailedSwitch(gwKey string, st *MigrateState, w io.Writer) error {
	var errs []string
	orig, hasOrig := st.OrigPorts[gwKey]
	if hasOrig {
		if err := restoreComposePorts(gwKey, orig, w); err != nil {
			errs = append(errs, "恢复端口映射失败: "+err.Error())
		}
	}
	dir := appComposeDir(gwKey)
	out, upErr := dockerx.Compose(dir, "up", "-d")
	if upErr != nil {
		errs = append(errs, "面板网关重建失败: "+tailStr(out, 120))
		// 拉不起来就不能让它留着占 80/443
		if sout, serr := dockerx.Compose(dir, "stop"); serr != nil {
			errs = append(errs, "停止面板网关也失败: "+tailStr(sout, 100))
		} else {
			fmt.Fprintf(w, "   已停止面板网关容器 %s(端口未恢复,避免继续抢占)\n", gwKey)
		}
	} else if hasOrig {
		// 端口已还原:从状态中清除记录(重复回滚幂等)
		delete(st.OrigPorts, gwKey)
		saveState(st)
	}
	if err := startBTNginx(w); err != nil {
		errs = append(errs, "启动宝塔 Nginx 失败: "+err.Error())
	}
	if st.AutostartOff {
		restoreBTNginxAutostart(w, st.AutostartVia)
		st.AutostartOff, st.AutostartVia = false, ""
		saveState(st)
	}
	st2 := loadState()
	st2.Switched = false
	saveState(st2)
	time.Sleep(2 * time.Second)
	// 回滚后再次确认:80/443 必须回到宝塔 nginx 手里
	if out, err := exec.Command("sh", "-c", "ss -tlnp 2>/dev/null | grep -E ':(80|443) '").CombinedOutput(); err != nil || !strings.Contains(strings.ToLower(string(out)), "nginx") {
		errs = append(errs, "回滚后未检测到 nginx 监听 80/443: "+tailStr(string(out), 120))
	} else {
		fmt.Fprintln(w, ">> 回滚确认:80/443 已由宝塔 Nginx 重新监听")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// SwitchTraffic 流量切换:预检(唯一网关容器)→ 采集基线 → 停宝塔 Nginx →
// 面板网关改绑 80/443 → 重建 → 验证(端口归属+逐域名状态码一致)。
// 任何一步失败自动回滚并再次确认 80/443 回到宝塔。
func SwitchTraffic(w io.Writer) error {
	InvalidateScanCache()
	res, err := Scan()
	if err != nil {
		return err
	}
	if !res.Found {
		return fmt.Errorf("未检测到宝塔面板")
	}
	// 0) 预检:有且仅有一个运行中的面板网关容器
	gwKey, err := soleGatewayKey()
	if err != nil {
		return err
	}
	// 0.5) 基线:切换前逐域名状态码(宝塔正在服务;只比宝塔认识的域名)
	baseline := captureBaseline(w, res.Sites, loadState().Imported)
	// 刷新 php-fpm pool 访问控制(容器 IP 可能已变)
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

	// 2) 面板网关改绑 80/443(以容器实际端口为准判断是否需要改写)
	var switchErr error
	cur80, cur443, perr := containerPublishedPorts(gwKey)
	if perr != nil {
		switchErr = fmt.Errorf("读取 %s 当前端口映射失败: %v", gwKey, perr)
	} else if cur80 == 80 && cur443 == 443 {
		fmt.Fprintf(w, "   [%s] 已绑定 80/443,无需改写\n", gwKey)
	} else {
		fmt.Fprintf(w, ">> 面板网关 %s 改绑 80/443(当前 %v/%v)...\n", gwKey, cur80, cur443)
		dir := appComposeDir(gwKey)
		cf := filepath.Join(dir, "docker-compose.yml")
		if b, rerr := os.ReadFile(cf); rerr != nil {
			switchErr = fmt.Errorf("读取 %s 失败: %v", cf, rerr)
		} else {
			newContent, orig80, orig443, changed := rewriteComposePorts(string(b))
			if !changed {
				switchErr = fmt.Errorf("[%s] compose 中找不到可改写的 80/443 端口映射,请检查 docker-compose.yml", gwKey)
			} else {
				if st.OrigPorts == nil {
					st.OrigPorts = map[string]string{}
				}
				if _, has := st.OrigPorts[gwKey]; !has {
					st.OrigPorts[gwKey] = fmt.Sprintf("%d/%d", orig80, orig443)
					saveState(st)
				}
				if werr := util.AtomicWriteString(cf, newContent, 0o644); werr != nil {
					switchErr = werr
				} else {
					out, uerr := dockerx.Compose(dir, "up", "-d")
					fmt.Fprintln(w, out)
					if uerr != nil {
						switchErr = fmt.Errorf("[%s] 容器重建失败: %s", gwKey, tailStr(out, 300))
					}
					time.Sleep(2 * time.Second)
				}
			}
		}
	}
	// 3) 验证;任一步失败自动回滚,回滚后再次确认 80/443 归属
	if switchErr == nil {
		switchErr = verifySwitched(w, gwKey, baseline)
	}
	if switchErr != nil {
		fmt.Fprintf(w, ">> 切换失败: %v\n>> 自动回滚...\n", switchErr)
		if rbErr := rollbackAfterFailedSwitch(gwKey, st, w); rbErr != nil {
			return fmt.Errorf("%v;且自动回滚存在问题: %v(请立即检查 80/443 端口占用!)", switchErr, rbErr)
		}
		return fmt.Errorf("切换失败已自动回滚: %v", switchErr)
	}

	fmt.Fprintln(w, ">> 流量已切换到面板网关。如需回滚,请使用「回滚到宝塔」。")
	st2 := loadState()
	st2.Switched = true
	saveState(st2)
	// 证书续期接管:迁移导入的宝塔证书改由面板 ACME 重签并自动续期
	RenewMigratedSites(w)
	return nil
}

// RollbackTraffic 回滚:还原面板网关端口映射(没有记录时仅停止容器)、
// 启动宝塔 Nginx、恢复自启,并确认 80/443 已回到宝塔。
func RollbackTraffic(w io.Writer) error {
	InvalidateScanCache()
	st := loadState()
	gwKey, gwErr := soleGatewayKey()
	if gwErr != nil {
		// 网关容器可能已停(半切换状态):继续回滚,但要还原 compose 端口,
		// 否则下次容器启动又会抢 80/443
		fmt.Fprintf(w, ">> %v(继续回滚流程)\n", gwErr)
	}
	// 还原端口映射:按状态记录逐个处理(与当前是否运行无关,文件必须改回)
	for _, key := range []string{"kun-nginx", "kun-openresty"} {
		orig, has := st.OrigPorts[key]
		if !has {
			continue
		}
		if err := restoreComposePorts(key, orig, w); err != nil {
			fmt.Fprintf(w, "   [%s] 端口还原失败: %v\n", key, err)
			continue
		}
		delete(st.OrigPorts, key)
	}
	saveState(st)
	if gwKey != "" {
		dir := appComposeDir(gwKey)
		// 端口已还原则带新端口拉起(面板网站保持可用);没还原成功则停掉
		if _, has := loadState().OrigPorts[gwKey]; !has {
			out, err := dockerx.Compose(dir, "up", "-d")
			fmt.Fprintln(w, out)
			if err != nil {
				fmt.Fprintf(w, "   面板网关重建失败(%v),改为停止以避免抢占 80/443\n", err)
				sout, _ := dockerx.Compose(dir, "stop")
				fmt.Fprintln(w, sout)
			}
		} else {
			out, _ := dockerx.Compose(dir, "stop")
			fmt.Fprintf(w, "   已停止面板网关容器(端口未还原): %s\n", tailStr(out, 100))
		}
	}
	fmt.Fprintln(w, ">> 启动宝塔 Nginx(systemd/init.d 优先)...")
	if err := startBTNginx(w); err != nil {
		return fmt.Errorf("启动宝塔 Nginx 失败: %v", err)
	}
	// 恢复宝塔 Nginx 自启
	if st.AutostartOff {
		restoreBTNginxAutostart(w, st.AutostartVia)
		st.AutostartOff, st.AutostartVia = false, ""
		saveState(st)
	}
	st.Switched = false
	saveState(st)
	time.Sleep(2 * time.Second)
	// 回滚后确认 80/443 归属
	if out, err := exec.Command("sh", "-c", "ss -tlnp 2>/dev/null | grep -E ':(80|443) '").CombinedOutput(); err != nil || !strings.Contains(strings.ToLower(string(out)), "nginx") {
		return fmt.Errorf("回滚后未检测到宝塔 Nginx 监听 80/443: %s", tailStr(string(out), 150))
	}
	fmt.Fprintln(w, ">> 已回滚到宝塔:80/443 由宝塔重新监听,原站点配置未改动,应立即恢复访问。")
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

// ---------- 供 1Panel 迁移复用的切换原语(p1migrate) ----------

// RewriteComposePortsE 把 compose 里 80/443 的宿主端口改绑为 80/443,
// 返回新内容与原端口;已是 80:80 的行跳过(幂等)
func RewriteComposePortsE(content string) (string, int, int, bool) {
	return rewriteComposePorts(content)
}

// RestoreComposePortsE 把 compose 里已绑定的 80/443 行还原为指定原端口(幂等)
func RestoreComposePortsE(content string, o80, o443 int) string {
	if o80 > 0 && o80 != 80 {
		content = reComposeBind80.ReplaceAllString(content, "${1}"+itoa(o80)+"${3}")
	}
	if o443 > 0 && o443 != 443 {
		content = reComposeBind443.ReplaceAllString(content, "${1}"+itoa(o443)+"${3}")
	}
	return content
}

// ContainerPublishedPortsE 读容器实际发布的宿主端口
func ContainerPublishedPortsE(name string) (int, int, error) {
	return containerPublishedPorts(name)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
