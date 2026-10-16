// remote.go —— 远程宝塔迁移:输入服务器 IP/端口/密码,SSH 自动对接搬站。
//
// 与同机迁移的本质差异:
//   - 网站文件走 SSH 流式传输(tar over ssh),落到本面板标准根目录
//   - PHP 站点不能 FastCGI 直连远程 php-fpm(跨机不可达)——按远程 PHP 版本
//     自动安装本面板对应版本的 PHP 运行时容器,站点转为容器服务(版本不变)
//   - 反向代理站点的上游(远程机 127.0.0.1:port)改写为 远程IP:port,
//     DNS 切换前继续由旧服务器提供服务(零停机)
//   - 数据库(可选)远程 mysqldump 流式导入面板 MySQL,并按宝塔记录的
//     账号密码重建用户授权;面板 MySQL 自动增加 172.17.0.1:3306 发布,
//     站点连接串 host 改为 172.17.0.1
//   - 证书随元数据打包导入并绑定;计划任务迁移时自动改写站点路径
//   - 安全:SSH 主机指纹 TOFU——首次连接记录指纹并回显给用户,迁移时强制校验
package btmigrate

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"kunpanel/internal/appstore"
	"kunpanel/internal/dbmgr"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
	"kunpanel/internal/taskx"
	"kunpanel/internal/websites"
)

const btRemoteRoot = "/www/server"

// RemoteCred 远程连接凭据
type RemoteCred struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	Pass        string `json:"pass"`
	Fingerprint string `json:"fingerprint,omitempty"` // TOFU:首次为空(探测回显),迁移时必须带上
}

// remoteDial SSH 连接(密码认证;指纹提供时强校验,未提供时 TOFU 接受并回显)
func remoteDial(cr RemoteCred) (*ssh.Client, string, error) {
	var actual string
	cb := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		actual = ssh.FingerprintSHA256(key)
		if cr.Fingerprint != "" && actual != cr.Fingerprint {
			return fmt.Errorf("主机指纹不匹配: 期望 %s,实际 %s(连接可能被劫持,已中止)", cr.Fingerprint, actual)
		}
		return nil
	}
	port := cr.Port
	if port <= 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{
		User:            cr.User,
		Auth:            []ssh.AuthMethod{ssh.Password(cr.Pass)},
		HostKeyCallback: cb,
		Timeout:         15 * time.Second,
	}
	cl, err := ssh.Dial("tcp", net.JoinHostPort(cr.Host, strconv.Itoa(port)), cfg)
	if err != nil {
		if actual != "" {
			return nil, actual, err
		}
		return nil, "", err
	}
	return cl, actual, nil
}

// runRemote 执行远程命令(stdout+stderr 合并,带超时)
func runRemote(cl *ssh.Client, timeout time.Duration, cmd string) (string, error) {
	sess, err := cl.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var buf strings.Builder
	sess.Stdout = &buf
	sess.Stderr = &buf
	done := make(chan error, 1)
	if err := sess.Start(cmd); err != nil {
		return "", err
	}
	go func() { done <- sess.Wait() }()
	select {
	case <-time.After(timeout):
		_ = sess.Signal(ssh.SIGKILL)
		<-done
		return buf.String(), fmt.Errorf("远程命令超时")
	case err := <-done:
		if err != nil {
			return buf.String(), fmt.Errorf("%v: %s", err, tailStr(buf.String(), 200))
		}
		return buf.String(), nil
	}
}

// shQuote 远程 shell 单引号包裹
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RemoteSite 远程宝塔站点
type RemoteSite struct {
	Domain  string `json:"domain"`
	Type    string `json:"type"` // php / proxy / static
	PhpVer  string `json:"php_ver,omitempty"`
	SSL     bool   `json:"ssl"`
	Root    string `json:"root"`     // 远程绝对路径
	DirName string `json:"dir_name"` // 相对根(/www 去前缀),tar 流式传输用
	ProxyTo string `json:"proxy_to,omitempty"`
	ConfRel string `json:"conf_rel"` // 元数据包内的相对路径
}

// RemoteScanResult 远程扫描结果(不含密码等敏感信息)
type RemoteScanResult struct {
	Fingerprint string       `json:"fingerprint"`
	LocalIP     string       `json:"local_ip"` // 本面板服务器出口 IP(供用户加入远程白名单)
	Found       bool         `json:"found"`
	Sites       []RemoteSite `json:"sites"`
	Crons       []BtCron     `json:"crons"`
	DBs         []BtDB       `json:"dbs"`
	HasMySQL    bool         `json:"has_mysql"`
	PHPVersions []string     `json:"php_versions"`
	Warnings    []string     `json:"warnings"`
}

// RemoteOptions 远程迁移选项
type RemoteOptions struct {
	Cred      RemoteCred `json:"cred"`
	Sites     []string   `json:"sites"`
	Databases []string   `json:"databases"`
}

var (
	reRemotePHPInclude = regexp.MustCompile(`include\s+enable-php-([0-9]+)\.conf`)
	reRemoteProxyPass  = regexp.MustCompile(`proxy_pass\s+(https?://[^;\s]+)`)
	reRemoteLoopback   = regexp.MustCompile(`(https?://)127\.0\.0\.1(:\d+)?`)
)

// remoteMetaRoot 元数据解压根目录
func remoteMetaRoot() string { return filepath.Join(util.DataDir(), "btremote") }

// fetchRemoteMeta SSH 拉取宝塔元数据包(vhost/rewrite/证书/default.db)并解压,
// 返回解压根(= 远程根路径在包内的镜像)与真实指纹
func fetchRemoteMeta(cr RemoteCred, w io.Writer) (metaRoot string, fp string, err error) {
	cl, actualFP, err := remoteDial(cr)
	if err != nil {
		if actualFP != "" {
			return "", actualFP, fmt.Errorf("SSH 连接失败(服务器指纹 %s): %v", actualFP, err)
		}
		return "", "", fmt.Errorf("SSH 连接失败: %v", err)
	}
	defer cl.Close()
	tmp := remoteMetaRoot()
	_ = os.RemoveAll(tmp)
	_ = os.MkdirAll(tmp, 0o755)
	meta := "tar czf - -C / " +
		shQuote(strings.TrimPrefix(btRemoteRoot+"/panel/vhost/nginx", "/")) + " " +
		shQuote(strings.TrimPrefix(btRemoteRoot+"/panel/vhost/rewrite", "/")) + " " +
		shQuote(strings.TrimPrefix(btRemoteRoot+"/panel/vhost/cert", "/")) + " " +
		shQuote(strings.TrimPrefix(btDBPath, "/")) + " 2>/dev/null || true"
	tgz := filepath.Join(tmp, "meta.tgz")
	if err := streamRemoteFile(cl, meta, tgz, 120*time.Second); err != nil {
		return "", actualFP, fmt.Errorf("拉取元数据失败: %v", err)
	}
	if out, err := util.RunWithTimeout(60*time.Second, "tar", "xzf", tgz, "-C", tmp); err != nil {
		return "", actualFP, fmt.Errorf("元数据解压失败: %v %s", err, tailStr(out, 150))
	}
	_ = os.Remove(tgz)
	return filepath.Join(tmp, strings.TrimPrefix(btRemoteRoot, "/")), actualFP, nil
}

// RemoteScan SSH 连接远程宝塔,解析站点/计划任务/数据库清单
func RemoteScan(cr RemoteCred) (*RemoteScanResult, error) {
	res := &RemoteScanResult{Sites: []RemoteSite{}, Crons: []BtCron{}, DBs: []BtDB{}, PHPVersions: []string{}, Warnings: []string{}}
	metaRoot, actualFP, err := fetchRemoteMeta(cr, io.Discard)
	if err != nil {
		return nil, err
	}
	res.Fingerprint = actualFP

	// 是否宝塔(default.db 已随包拉回,存在即视为宝塔)
	dbLocal := filepath.Join(metaRoot, "panel/data/default.db")
	if _, err := os.Stat(dbLocal); err != nil {
		res.Warnings = append(res.Warnings, "远程服务器未检测到宝塔面板("+btRemoteRoot+"),无法迁移")
		return res, nil
	}
	res.Found = true

	// 站点解析
	confs, _ := filepath.Glob(filepath.Join(metaRoot, "panel/vhost/nginx/*.conf"))
	sort.Strings(confs)
	for _, cf := range confs {
		b, err := os.ReadFile(cf)
		if err != nil {
			continue
		}
		content := string(b)
		site, err := parseSiteConf(cf)
		if err != nil || site.Domain == "" {
			continue
		}
		rs := RemoteSite{
			Domain: site.Domain, SSL: site.SSL, Root: site.Root,
			ConfRel: strings.TrimPrefix(strings.TrimPrefix(cf, metaRoot), "/"),
		}
		rs.DirName = strings.TrimPrefix(rs.Root, "/")
		// 类型:enable-php include → php;否则若 main conf 或宝塔反代子目录
		// include 中存在 proxy_pass → proxy;再否则 static
		// enable-php-00 = 宝塔"纯静态不启用 PHP",不是 PHP 00 版本
		if m := reRemotePHPInclude.FindStringSubmatch(content); m != nil {
			if v := m[1]; v != "00" && v != "0" {
				rs.Type = "php"
				rs.PhpVer = v
			}
		}
		// 反代子目录 include 文件里找上游
		proxyInclude := regexp.MustCompile(`include\s+([^;]*vhost/nginx/proxy/[^;]+);`)
		var bodyText strings.Builder
		bodyText.WriteString(content)
		for _, inc := range proxyInclude.FindAllStringSubmatch(content, -1) {
			pat := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(inc[1]), "include")), ";")
			matches, _ := filepath.Glob(filepath.Join(metaRoot, strings.TrimPrefix(pat, "/")))
			for _, mf := range matches {
				if mb, err := os.ReadFile(mf); err == nil {
					bodyText.WriteString("\n")
					bodyText.Write(mb)
				}
			}
		}
		body := bodyText.String()
		if rs.Type == "" {
			if m := reRemoteProxyPass.FindStringSubmatch(body); m != nil {
				rs.Type = "proxy"
				rs.ProxyTo = m[1]
			} else if rs.Root != "" {
				rs.Type = "static"
			} else {
				continue
			}
		}
		res.Sites = append(res.Sites, rs)
	}
	if len(res.Sites) == 0 {
		res.Warnings = append(res.Warnings, "远程宝塔未解析出站点")
	}

	// 计划任务(PRAGMA 探测可用列:宝塔 11.x 无 sType)+ 数据库
	cronSQL := func() string {
		var colNames []string
		var colObjs []map[string]any
		raw, err := localJSON(dbLocal, "select name from pragma_table_info('crontab')")
		if err != nil {
			res.Warnings = append(res.Warnings, "读取远程计划任务列失败: "+err.Error())
		} else if json.Unmarshal([]byte(raw), &colObjs) != nil {
			res.Warnings = append(res.Warnings, "解析远程计划任务列失败")
		}
		for _, c := range colObjs {
			if n, ok := c["name"].(string); ok {
				colNames = append(colNames, n)
			}
		}
		cols := map[string]bool{}
		for _, c := range colNames {
			cols[c] = true
		}
		want := []string{"name", "type", "sType", "where1", "where_hour", "where_minute", "week", "sBody", "status"}
		var sel []string
		for _, wnt := range want {
			if cols[wnt] {
				sel = append(sel, wnt)
			}
		}
		if !cols["name"] {
			return ""
		}
		statusFilter := ""
		if cols["status"] {
			statusFilter = " where status=0 or status is null"
		}
		return fmt.Sprintf(`select %s from crontab%s`, strings.Join(sel, ","), statusFilter)
	}()
	if cronSQL != "" {
		raw, err := localJSON(dbLocal, cronSQL)
		if err != nil {
			res.Warnings = append(res.Warnings, "读取远程计划任务失败: "+err.Error())
		} else {
			var rows []map[string]any
			if jerr := json.Unmarshal([]byte(raw), &rows); jerr != nil {
				res.Warnings = append(res.Warnings, "解析远程计划任务失败: "+jerr.Error())
			} else {
				for _, r := range rows {
					res.Crons = append(res.Crons, parseBTCron(r))
				}
			}
		}
	} else {
		res.Warnings = append(res.Warnings, "远程 crontab 表无 name 列或 PRAGMA 探测失败")
	}
	if raw, err := localJSON(dbLocal, "select name,username as user,ps from databases"); err == nil {
		var dbs []BtDB
		if json.Unmarshal([]byte(raw), &dbs) == nil {
			res.DBs = dbs
		}
	}
	if raw, err := localJSON(dbLocal, "select count(*) as n from config where mysql_root is not null and mysql_root != ''"); err == nil {
		res.HasMySQL = strings.Contains(raw, "1")
	}
	// 远程已装的 PHP 版本(从站点 enable-php include 推导,去重排序)
	phpSet := map[string]bool{}
	for _, s := range res.Sites {
		if s.PhpVer != "" {
			phpSet[phpDirToVersion(s.PhpVer)] = true
		}
	}
	for v := range phpSet {
		res.PHPVersions = append(res.PHPVersions, v)
	}
	sort.Strings(res.PHPVersions)
	return res, nil
}

// localJSON 用本机 python3 读 sqlite(远程拉回的 default.db)
func localJSON(dbPath, sqlText string) (string, error) {
	script := fmt.Sprintf(`import sqlite3,json;c=sqlite3.connect(%q);c.row_factory=sqlite3.Row;print(json.dumps([dict(r) for r in c.execute(%q)]))`, dbPath, sqlText)
	out, err := util.RunWithTimeout(15*time.Second, "python3", "-c", script)
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, tailStr(out, 150))
	}
	return strings.TrimSpace(out), nil
}

// phpDirToVersion 宝塔 PHP 目录名(74)→ 版本号(7.4)
func phpDirToVersion(dir string) string {
	if len(dir) == 2 {
		return dir[:1] + "." + dir[1:]
	}
	return dir
}

// streamRemoteFile 远程命令输出流式写到本地文件
func streamRemoteFile(cl *ssh.Client, remoteCmd, dst string, timeout time.Duration) error {
	sess, err := cl.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	pipe, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := sess.Start(remoteCmd); err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		_, e := io.Copy(f, pipe)
		errCh <- e
	}()
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case <-time.After(timeout):
		_ = sess.Signal(ssh.SIGKILL)
		<-done
		return fmt.Errorf("远程传输超时")
	case e := <-errCh:
		if e != nil {
			return e
		}
		<-done
	case err := <-done:
		if err != nil {
			return fmt.Errorf("远程命令失败: %v", err)
		}
	}
	return f.Sync()
}

// localPublicIP 本机公网出口 IP(UDP 探测不发包)
func localPublicIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return "本服务器IP"
	}
	defer conn.Close()
	return strings.Split(conn.LocalAddr().String(), ":")[0]
}

// ---------- 远程迁移执行 ----------

// RemoteApply 执行远程迁移(任务体内调用)
func RemoteApply(opts RemoteOptions, w io.Writer) error {
	cr := opts.Cred
	if cr.Fingerprint == "" {
		return fmt.Errorf("缺少服务器指纹(请先执行连接检测)")
	}
	sel := map[string]bool{}
	for _, d := range opts.Sites {
		sel[d] = true
	}
	pw := taskx.P(w)
	pw.Set(1, 6, "连接远程服务器并拉取元数据")
	fmt.Fprintln(w, ">> 连接远程服务器并拉取宝塔元数据 ...")
	metaRoot, actualFP, err := fetchRemoteMeta(cr, w)
	if err != nil {
		return err
	}
	if actualFP != cr.Fingerprint {
		return fmt.Errorf("服务器指纹与检测时不一致,已中止(可能被劫持)")
	}
	cl, _, err := remoteDial(cr)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %v", err)
	}
	defer cl.Close()
	dbLocal := filepath.Join(metaRoot, "panel/data/default.db")
	if _, err := os.Stat(dbLocal); err != nil {
		return fmt.Errorf("远程服务器未检测到宝塔面板")
	}

	// 1) 按需安装对应版本的 PHP 运行时(跨机无法直连远程 php-fpm)
	needPHP := map[string]bool{}
	confs, _ := filepath.Glob(filepath.Join(metaRoot, "panel/vhost/nginx/*.conf"))
	confByDomain := map[string]string{}
	for _, cf := range confs {
		if site, err := parseSiteConf(cf); err == nil && site.Domain != "" {
			confByDomain[site.Domain] = cf
			if sel[site.Domain] && site.Type == "php" && site.PhpVer != "" {
				needPHP[phpDirToVersion(site.PhpVer)] = true
			}
		}
	}
	phpKeyByVer := map[string]string{}
	phpVers := sortedKeys(needPHP)
	total := 3 + len(phpVers) + len(sel) + len(opts.Databases)
	pw.Set(2, total, "安装 PHP 运行时")
	for i, ver := range phpVers {
		fmt.Fprintf(w, ">> 确保面板存在 PHP %s 运行时(远程站点改由本面板容器服务,版本不变)...\n", ver)
		pw.Set(2+i, total, "安装 PHP "+ver+" 运行时")
		key, err := appstore.EnsurePHPVersion(w, ver)
		if err != nil {
			return fmt.Errorf("安装 PHP %s 运行时失败: %v", ver, err)
		}
		phpKeyByVer[ver] = key
	}

	// 2) 选了数据库:确保面板 MySQL 对容器网络可达(172.17.0.1:3306)
	if len(opts.Databases) > 0 {
		fmt.Fprintln(w, ">> 检查面板 MySQL 容器网络发布(站点将用 172.17.0.1:3306 连库)...")
		if err := ensureMySQLBridge(w); err != nil {
			fmt.Fprintf(w, "   警告: %v(迁移继续,站点连库需自行处理)\n", err)
		}
	}

	// 3) 逐站点迁移
	migrated := 0
	cronPathMap := map[string]string{}
	siteBase := 2 + len(phpVers)
	i := 0
	for _, domain := range sortedKeys(sel) {
		cf, ok := confByDomain[domain]
		if !ok {
			fmt.Fprintf(w, ">> %s 不在远程站点列表中(跳过)\n", domain)
			continue
		}
		site, err := parseSiteConf(cf)
		if err != nil || site.Domain == "" {
			continue
		}
		i++
		pw.Set(siteBase+i, total, "迁移站点 "+site.Domain)
		fmt.Fprintf(w, ">> 迁移站点 %s(%s)...\n", site.Domain, site.Type)
		// 反代:从 main conf 与宝塔反代子目录 include 中找上游(远程机 127.0.0.1 → 远程IP)
		remoteTarget := ""
		if site.Type == "proxy" {
			content, _ := os.ReadFile(cf)
			body := string(content)
			for _, inc := range regexp.MustCompile(`include\s+[^;]*vhost/nginx/proxy/[^;]+;`).FindAllStringSubmatch(body, -1) {
				pat := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(inc[1]), "include")), ";")
				for _, mf := range mustGlob(filepath.Join(metaRoot, strings.TrimPrefix(pat, "/"))) {
					if mb, rerr := os.ReadFile(mf); rerr == nil {
						body += "\n" + string(mb)
					}
				}
			}
			if m := reRemoteProxyPass.FindStringSubmatch(body); m != nil {
				remoteTarget = reRemoteLoopback.ReplaceAllString(m[1], "${1}"+cr.Host+"${2}")
			}
		}
		siteID, err := remoteCreateSite(cl, site, cr.Host, phpKeyByVer, remoteTarget, w)
		if err != nil {
			if strings.Contains(err.Error(), "站点已存在") {
				fmt.Fprintf(w, "   已在面板中(跳过,不覆盖)\n")
				continue
			}
			fmt.Fprintf(w, "   失败: %v(继续下一个)\n", err)
			continue
		}
		migrated++
		// 伪静态(元数据包内与 conf 同名)
		rwPath := filepath.Join(metaRoot, "panel/vhost/rewrite", filepath.Base(cf))
		if rw, rerr := os.ReadFile(rwPath); rerr == nil && strings.TrimSpace(string(rw)) != "" {
			_ = util.AtomicWrite(websites.RewritePath(siteID), rw, 0o644)
			fmt.Fprintf(w, "   伪静态已迁移\n")
		}
		// 保真提取(include 重映射到元数据包;回环上游改指远程机)
		if extra, locs, err := extractExtrasRemote(cf, site.Type, metaRoot, cr.Host); err == nil && strings.TrimSpace(extra) != "" {
			_ = websites.SetSiteRawExtra(siteID, extra)
			fmt.Fprintf(w, "   自定义配置已保留(%d 个 location/指令块)\n", locs)
		}
		// 证书导入绑定 + 异步重签接管续期
		if site.SSL {
			name := site.CertName
			if name == "" {
				name = site.Domain
			}
			crt := filepath.Join(metaRoot, "panel/vhost/cert", name, "fullchain.pem")
			key := filepath.Join(metaRoot, "panel/vhost/cert", name, "privkey.pem")
			if cid, cerr := importCert(name, crt, key); cerr != nil {
				fmt.Fprintf(w, "   证书导入失败(%v)\n", cerr)
			} else {
				bindSiteSSL(siteID, cid)
				fmt.Fprintf(w, "   证书已导入绑定,稍后自动重签接管续期 ...\n")
				go autoIssueRemote(siteID)
			}
		}
		// 计划任务里该站点路径的改写映射(远程路径 → 本面板路径)
		if site.Root != "" {
			cronPathMap[site.Root] = "/opt/kunpanel/data/websites/roots/" + siteID
		}
	}
	fmt.Fprintf(w, ">> 站点迁移完成:%d 个\n", migrated)

	// 4) 计划任务(命令中的远程站点路径改写为本面板路径;PRAGMA 兼容无 sType schema)
	cronBase := siteBase + len(sel)
	pw.Set(cronBase, total, "迁移计划任务")
	n := 0
	if raw, err := localJSON(dbLocal, "select * from crontab"); err == nil {
		var rows []map[string]any
		if json.Unmarshal([]byte(raw), &rows) == nil {
			for _, r := range rows {
				cn := parseBTCron(r)
				if !cn.Mig || strings.TrimSpace(cn.Shell) == "" || strings.TrimSpace(cn.Spec) == "" {
					continue
				}
				cmdText := cn.Shell
				for oldPath, newPath := range cronPathMap {
					cmdText = strings.ReplaceAll(cmdText, oldPath, newPath)
				}
				if err := addCron(cn.Name, cmdText, cn.Spec); err != nil {
					fmt.Fprintf(w, "   计划任务[%s]迁移失败: %v\n", cn.Name, err)
					continue
				}
				n++
			}
		}
	}
	fmt.Fprintf(w, ">> 计划任务已迁移 %d 条(远程机 crontab 未改动,域名切换后请在旧服务器停用宝塔定时)\n", n)

	// 5) 数据库迁移(可选)
	for di, name := range opts.Databases {
		pw.Set(cronBase+1+di, total, "迁移数据库 "+name)
		fmt.Fprintf(w, ">> 迁移数据库 %s(远程 mysqldump → 面板 MySQL)...\n", name)
		if err := migrateRemoteDatabase(cl, dbLocal, name, w); err != nil {
			fmt.Fprintf(w, "   失败: %v(继续)\n", err)
		}
	}

	// 6) 重载网关
	pw.Set(total, total, "重载 Nginx 并收尾")
	if err := websites.Reload(); err != nil {
		return fmt.Errorf("Nginx 配置校验失败: %v", err)
	}
	fmt.Fprintf(w, ">> 远程迁移完成!最后一步需要手动切换:把域名 DNS 的 A 记录指向本服务器(%s)。\n", localPublicIP())
	fmt.Fprintf(w, "   DNS 生效前网站仍由旧服务器服务(反代站点直接回源旧机,零停机);切换后请在旧服务器停用宝塔 Nginx。\n")
	return nil
}

// remoteCreateSite 创建面板站点 + 流式搬运站点文件
func remoteCreateSite(cl *ssh.Client, s BtSite, remoteHost string, phpKeyByVer map[string]string, remoteTarget string, w io.Writer) (string, error) {
	var siteID string
	var err error
	switch s.Type {
	case "php":
		ver := phpDirToVersion(s.PhpVer)
		key := phpKeyByVer[ver]
		if key == "" {
			key = "php"
		}
		site, cerr := websites.Create(websites.CreateOpts{Domain: s.Domain, SiteType: "php", PhpKey: key, Port: 80})
		if cerr != nil {
			return "", cerr
		}
		siteID = site.ID
	case "proxy":
		// 远程机的 127.0.0.1:port 改指 远程IP:port,DNS 切换前零停机回源旧机
		siteID, err = createViaWebsitesOpts(s.Domain, "proxy", "", remoteTarget, 80)
		if err != nil {
			return "", err
		}
	default:
		siteID, err = createViaWebsites(s.Domain, "static", "", "")
		if err != nil {
			return "", err
		}
	}
	// 流式搬运站点文件(有根目录的站点);远程根路径去 / 前缀即 tar 相对根
	dirName := strings.TrimPrefix(s.Root, "/")
	if dirName != "" && dirName != "." {
		dest := "/opt/kunpanel/data/websites/roots/" + siteID
		_ = os.MkdirAll(dest, 0o755)
		fmt.Fprintf(w, "   流式传输站点文件 %s ...\n", s.Root)
		tmp := filepath.Join(os.TempDir(), "btremote_site.tgz")
		if err := streamRemoteFile(cl, "tar czf - -C / "+shQuote(dirName), tmp, 30*time.Minute); err != nil {
			return siteID, fmt.Errorf("文件传输失败: %v", err)
		}
		depth := len(strings.Split(strings.Trim(dirName, "/"), "/"))
		if out, terr := util.RunWithTimeout(10*time.Minute, "tar", "xzf", tmp, "-C", dest, "--strip-components", strconv.Itoa(depth)); terr != nil {
			return siteID, fmt.Errorf("解压站点文件失败: %v %s", terr, tailStr(out, 150))
		}
		_ = os.Remove(tmp)
		// 移除面板在新建站点时生成的默认首页,避免盖住用户的 index.html
		_ = os.Remove(filepath.Join(dest, "index.html"))
	}
	return siteID, nil
}

// ensureMySQLBridge 确保面板 MySQL compose 额外发布 172.17.0.1:3306
// (迁移站点的 PHP 容器用该地址连库;仅宿主机网桥可达,不暴露公网)
func ensureMySQLBridge(w io.Writer) error {
	dir := "/opt/kunpanel/apps/mysql"
	cf := filepath.Join(dir, "docker-compose.yml")
	b, err := os.ReadFile(cf)
	if err != nil {
		return fmt.Errorf("未找到面板 MySQL 的 compose(可能未安装)")
	}
	content := string(b)
	if strings.Contains(content, "172.17.0.1:3306") {
		return nil
	}
	re := regexp.MustCompile(`(?m)^(\s*-\s*)"127\.0\.0\.1:3306:3306"`)
	if !re.MatchString(content) {
		return fmt.Errorf("MySQL compose 端口格式与预期不符,请手动为 MySQL 增加容器网络发布")
	}
	content2 := re.ReplaceAllString(content, `$1"127.0.0.1:3306:3306"
$1"172.17.0.1:3306:3306"`)
	if err := util.AtomicWriteString(cf, content2, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(w, "   MySQL 将增加 172.17.0.1:3306 发布,容器需重建一次(短暂重启)...")
	if out, err := dockerx.Compose(dir, "up", "-d"); err != nil {
		return fmt.Errorf("MySQL 重建失败: %s", tailStr(out, 150))
	}
	time.Sleep(3 * time.Second)
	return nil
}

// migrateRemoteDatabase 远程导出一个库并导入面板 MySQL,按宝塔记录重建账号授权
func migrateRemoteDatabase(cl *ssh.Client, metaDbLocal, name string, w io.Writer) error {
	if !dbmgr.ValidIdentifier(name) {
		return fmt.Errorf("库名不合法: %s", name)
	}
	// 凭据:宝塔 databases 表(该库自己的账号)→ root 兜底
	var user, pass string
	if raw, err := localJSON(metaDbLocal, fmt.Sprintf("select username,password from databases where name='%s'", strings.ReplaceAll(name, "'", ""))); err == nil {
		var rows []map[string]any
		if json.Unmarshal([]byte(raw), &rows) == nil && len(rows) > 0 {
			user, _ = rows[0]["username"].(string)
			pass, _ = rows[0]["password"].(string)
		}
	}
	if user == "" || pass == "" {
		if raw, err := localJSON(metaDbLocal, "select mysql_root from config limit 1"); err == nil {
			var rows []map[string]any
			if json.Unmarshal([]byte(raw), &rows) == nil && len(rows) > 0 {
				user, pass = "root", fmt.Sprint(rows[0]["mysql_root"])
			}
		}
	}
	if user == "" || pass == "" {
		return fmt.Errorf("拿不到库 %s 的远程连接凭据", name)
	}
	// 远程导出(凭据经环境变量传递,不进命令行/不落日志)
	dumpCmd := fmt.Sprintf(`MYSQL_PWD=%s mysqldump -h127.0.0.1 -P3306 -u%s --single-transaction --quick %s | gzip`,
		shQuote(pass), shQuote(user), shQuote(name))
	gz := filepath.Join(util.DataDir(), "btremote_"+name+".sql.gz")
	fmt.Fprintf(w, "   远程导出中 ...\n")
	if err := streamRemoteFile(cl, dumpCmd, gz, 30*time.Minute); err != nil {
		return fmt.Errorf("导出失败: %v", err)
	}
	defer os.Remove(gz)
	if fi, _ := os.Stat(gz); fi == nil || fi.Size() == 0 {
		return fmt.Errorf("导出内容为空(账号 %s 可能无权限或库为空)", user)
	}
	// 面板建库 + 导入
	if err := dbmgr.CreateDatabase("mysql", name); err != nil && !strings.Contains(err.Error(), "exist") {
		return fmt.Errorf("面板建库失败: %v", err)
	}
	fmt.Fprintf(w, "   导入面板 MySQL ...\n")
	if _, err := dbmgr.Import("mysql", name, gz); err != nil {
		return fmt.Errorf("导入失败: %v", err)
	}
	// 重建原账号授权(站点程序凭据不变,连接地址改 172.17.0.1)
	if user != "root" {
		u := strings.ReplaceAll(user, "'", "''")
		p := strings.ReplaceAll(pass, "'", "''")
		_, _ = dbmgr.Query("mysql", "", fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s'; GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%'; FLUSH PRIVILEGES;", u, p, name, u))
	}
	fmt.Fprintf(w, "   %s 已导入面板 MySQL;站点连接串 host 请改为 172.17.0.1(账号密码不变)\n", name)
	return nil
}

// autoIssueRemote 异步为远程迁移站点重签正式证书(导入的是宝塔旧证书)
func autoIssueRemote(siteID string) {
	_, _ = websites.IssueCertForSite(siteID, websites.AcmeIssueOpts{Method: "http"}, io.Discard)
}

// sortedKeys map 键排序(迁移顺序稳定)
func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mustGlob 忽略错误的 Glob
func mustGlob(pat string) []string {
	m, _ := filepath.Glob(pat)
	return m
}
