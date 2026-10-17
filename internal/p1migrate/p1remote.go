// p1remote —— 远程 1Panel → 坤面板 迁移(输入 IP + root 密码,SSH 自动对接)。
//
// 与宝塔远程迁移同思路:
//   - 扫描阶段:拉取远程 conf.d/*.conf 与 1Panel sqlite 库文件(小文件,秒级)
//   - 迁移阶段:按站点流式拉取文件(tar over ssh),静态/PHP 站文件直接解压到
//     面板标准根目录;数据库从远程 MySQL 容器 mysqldump 流式导入
//   - 反代/PHP 上游的回环地址改指远程机(DNS 切换前零停机回源)
package p1migrate

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

// P1RemoteCred 远程 1Panel 连接凭据
type P1RemoteCred struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	Pass        string `json:"pass"`
	Fingerprint string `json:"fingerprint,omitempty"` // TOFU:首次为空(探测回显),迁移时必须带上
}

// p1RemoteMetaRoot 远程元数据落地目录
func p1RemoteMetaRoot() string { return filepath.Join(util.DataDir(), "p1remote") }

// p1Dial SSH 连接(密码认证;指纹提供时强校验,未提供时 TOFU 接受并回显)
func p1Dial(cr P1RemoteCred) (*ssh.Client, string, error) {
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

// p1Run 远程命令(stdout+stderr 合并,带超时)
func p1Run(cl *ssh.Client, timeout time.Duration, cmd string) (string, error) {
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
			return buf.String(), fmt.Errorf("%v: %s", err, tailOf(buf.String(), 200))
		}
		return buf.String(), nil
	}
}

// p1ShQuote 远程 shell 单引号包裹
func p1ShQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// p1StreamRemoteFile 远程命令 stdout 流式落盘(tar over ssh)
func p1StreamRemoteFile(cl *ssh.Client, remoteCmd, dst string, timeout time.Duration) error {
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

// P1RemoteScan 远程扫描:拉取远程 conf.d 与 sqlite 库文件到本地元数据目录后解析
func P1RemoteScan(cr P1RemoteCred) (*P1ScanResult, string, error) {
	cl, fp, err := p1Dial(cr)
	if err != nil {
		return nil, fp, err
	}
	defer cl.Close()
	// 探测 1Panel 布局
	out, err := p1Run(cl, 20*time.Second, "ls /opt/1panel/db/ 2>/dev/null; test -d /opt/1panel/apps/openresty/openresty/conf.d && echo CONFDIR_OK")
	if err != nil || !strings.Contains(out, "CONFDIR_OK") {
		return nil, fp, fmt.Errorf("远程未检测到 1Panel(/opt/1panel)或结构不符")
	}
	dbFile := ""
	for _, cand := range []string{"1Panel.db", "core.db"} {
		if strings.Contains(out, cand) {
			dbFile = "/opt/1panel/db/" + cand
			break
		}
	}
	meta := p1RemoteMetaRoot()
	_ = os.MkdirAll(filepath.Join(meta, "conf.d"), 0o755)
	// 拉取 sqlite 库文件(小,秒级)
	if dbFile != "" {
		_ = p1StreamRemoteFile(cl, "cat "+p1ShQuote(dbFile), filepath.Join(meta, "p1panel.db"), 30*time.Second)
	}
	// 拉取 conf.d(tar over ssh)
	if err := p1StreamRemoteFile(cl,
		"cd /opt/1panel/apps/openresty/openresty && tar cf - conf.d 2>/dev/null",
		filepath.Join(meta, "confd.tar"), 60*time.Second); err == nil {
		_, _ = util.Run("sh", "-c", fmt.Sprintf("cd %s && tar xf confd.tar && rm -f confd.tar", p1ShQuote2(meta)))
	}
	res, err := scanFrom(filepath.Join(meta, "conf.d"), filepath.Join(meta, "p1panel.db"))
	if err != nil {
		return nil, fp, err
	}
	// 远程站点标记:根目录/上游的回环地址在迁移时改指远程机
	res.Found = true
	res.ConfDir = "remote:" + cr.Host
	return res, fp, nil
}

func p1ShQuote2(s string) string {
	return "'" + s + "'"
}

// P1RemoteApply 远程迁移:流式拉取站点文件/证书,数据库远程 mysqldump 导入。
// 反代/PHP 上游回环地址改指远程机(DNS 切换前零停机回源)。
func P1RemoteApply(cr P1RemoteCred, opts ApplyOptions, w io.Writer, siteDone func(domain, siteID string)) error {
	if cr.Fingerprint == "" {
		return fmt.Errorf("请先完成连接检测(TOFU 指纹回显)再迁移")
	}
	cl, actualFP, err := p1Dial(cr)
	if err != nil {
		return err
	}
	defer cl.Close()
	if actualFP != cr.Fingerprint {
		return fmt.Errorf("主机指纹与连接检测时不一致(环境可能被劫持),已中止")
	}
	remoteIP := cr.Host

	// 元数据:conf.d(解析站点结构)+ sqlite(数据库清单)
	meta := p1RemoteMetaRoot()
	_ = os.MkdirAll(filepath.Join(meta, "conf.d"), 0o755)
	_ = p1StreamRemoteFile(cl, "cat /opt/1panel/db/"+p1DbNameOn(cl), filepath.Join(meta, "p1panel.db"), 30*time.Second)
	_ = p1StreamRemoteFile(cl,
		"cd /opt/1panel/apps/openresty/openresty && tar cf - conf.d 2>/dev/null",
		filepath.Join(meta, "confd.tar"), 60*time.Second)
	_, _ = util.Run("sh", "-c", fmt.Sprintf("cd %s && tar xf confd.tar && rm -f confd.tar", p1ShQuote2(meta)))
	res, err := scanFrom(filepath.Join(meta, "conf.d"), filepath.Join(meta, "p1panel.db"))
	if err != nil {
		return err
	}
	sel := map[string]bool{}
	for _, d := range opts.Sites {
		sel[d] = true
	}
	if len(sel) == 0 && len(opts.Databases) == 0 {
		return fmt.Errorf("请选择要迁移的站点或数据库")
	}

	// 数据库(可选):远程容器 mysqldump → 本机文件 → 面板 MySQL
	for _, name := range opts.Databases {
		fmt.Fprintf(w, ">> 迁移数据库 %s(远程)...\n", name)
		if err := p1RemoteMigrateDB(cl, res.DBs, name, w); err != nil {
			fmt.Fprintf(w, "   失败: %v(继续)\n", err)
		}
	}

	migrated := 0
	for _, s := range res.Sites {
		if !sel[s.Domain] {
			continue
		}
		siteID, err := p1MigrateRemoteSite(cl, s, remoteIP, opts.ImportCerts, meta, w)
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
	fmt.Fprintf(w, ">> 远程站点迁移完成:%d 个\n", migrated)

	gw, gwErr := gatewayContainer()
	if gwErr != nil {
		return gwErr
	}
	if out, terr := dockerx.ExecIn(gw, "nginx", "-t"); terr != nil {
		return fmt.Errorf("Nginx 配置校验失败: %v: %s", terr, tailOf(out, 200))
	}
	_, _ = dockerx.ExecIn(gw, "nginx", "-s", "reload")
	fmt.Fprintf(w, ">> 1Panel 远程迁移完成。反代/PHP 上游当前回源远程机(%s),DNS 切换后可把上游改为本地面板服务\n", remoteIP)
	return nil
}

// p1DbNameOn 探测远程 1Panel 数据库文件名
func p1DbNameOn(cl *ssh.Client) string {
	out, err := p1Run(cl, 15*time.Second, "ls /opt/1panel/db/ 2>/dev/null")
	if err == nil {
		for _, cand := range []string{"1Panel.db", "core.db"} {
			if strings.Contains(out, cand) {
				return cand
			}
		}
	}
	return "1Panel.db"
}

// p1MigrateRemoteSite 单站点远程迁移
func p1MigrateRemoteSite(cl *ssh.Client, s P1Site, remoteIP string, importCerts bool, meta string, w io.Writer) (string, error) {
	fmt.Fprintf(w, ">> 迁移站点 %s(%s)...\n", s.Domain, s.Type)

	// 上游改写:回环 → 远程机(DNS 切换前零停机回源)
	proxyTo := ""
	fpmAddr := ""
	switch s.Type {
	case "proxy":
		proxyTo = reP1Loopback.ReplaceAllString(s.ProxyTo, "${1}"+remoteIP+"${3}")
		if proxyTo != s.ProxyTo {
			fmt.Fprintf(w, "   反代目标已改写: %s → %s(回源远程机)\n", s.ProxyTo, proxyTo)
		}
	case "php":
		if !s.FpmUnix {
			fpmAddr = reP1Loopback.ReplaceAllString(s.FpmAddr, "${1}"+remoteIP+"${3}")
			if fpmAddr != s.FpmAddr {
				fmt.Fprintf(w, "   PHP 上游已改写: %s → %s(回源远程机)\n", s.FpmAddr, fpmAddr)
			}
		} else {
			fmt.Fprintf(w, "   警告: PHP 运行时为 unix socket,跨机不可直连,该站 PHP 请求将 502;建议在远程 1Panel 改为 TCP 监听后重迁\n")
		}
	}

	// 建站:文件流式拉取,静态/PHP 直接解压到面板标准根目录
	var siteID string
	var err error
	var created *websites.Site
	switch s.Type {
	case "proxy":
		created, err = websites.Create(websites.CreateOpts{
			Domain: s.Domain, SiteType: "proxy", ProxyTo: proxyTo,
			ProxyHost: "$http_host", Port: 80,
		})
		if created != nil {
			siteID = created.ID
		}
	case "php":
		created, err = websites.Create(websites.CreateOpts{
			Domain: s.Domain, SiteType: "phpfpm", FpmPass: fpmAddr, Port: 80,
		})
		if created != nil {
			siteID = created.ID
			pullRemoteSiteFiles(cl, s, created.Root, meta, w)
		}
	default:
		created, err = websites.Create(websites.CreateOpts{
			Domain: s.Domain, SiteType: "static", Port: 80,
		})
		if created != nil {
			siteID = created.ID
			pullRemoteSiteFiles(cl, s, created.Root, meta, w)
		}
	}
	if err != nil {
		return "", err
	}

	// 证书(远程 www/ssl/<域名>)
	if importCerts && s.SSL {
		certDir := filepath.Join(meta, "ssl", s.Domain)
		_ = os.MkdirAll(certDir, 0o755)
		if err := p1StreamRemoteFile(cl,
			fmt.Sprintf("cd /opt/1panel/apps/openresty/openresty/www/ssl && tar cf - %s 2>/dev/null", p1ShQuote(s.Domain)),
			filepath.Join(meta, "ssl.tar"), 60*time.Second); err == nil {
			_, _ = util.Run("sh", "-c", fmt.Sprintf("cd %s && tar xf ssl.tar && rm -f ssl.tar", p1ShQuote2(certDir)))
		}
		crt := filepath.Join(certDir, s.Domain, "fullchain.pem")
		key := filepath.Join(certDir, s.Domain, "privkey.pem")
		if _, serr := os.Stat(crt); serr != nil {
			key = filepath.Join(certDir, s.Domain, "private.key")
		}
		if cid, cerr := importP1Cert(s.Domain, crt, key); cerr != nil {
			fmt.Fprintf(w, "   证书导入失败(%v),站点可稍后手动绑定\n", cerr)
		} else {
			_ = websites.SetSiteSSL(siteID, cid, true)
			fmt.Fprintf(w, "   证书已导入并绑定\n")
		}
	}

	// 单站配置校验:失败撤销该站全部产物
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

// pullRemoteSiteFiles 流式拉取站点文件到面板根目录(1Panel 目录结构:
// www/sites/<别名>/index 为站点根)
func pullRemoteSiteFiles(cl *ssh.Client, s P1Site, panelRoot, meta string, w io.Writer) {
	// 从 conf 的 root 解析远程站点目录别名:/opt/.../www/sites/<X>/index
	alias := ""
	if s.Root != "" {
		parts := strings.Split(filepath.ToSlash(s.Root), "/")
		for i, seg := range parts {
			if seg == "sites" && i+1 < len(parts) {
				alias = parts[i+1]
			}
		}
	}
	if alias == "" {
		alias = s.Domain
	}
	if err := p1StreamRemoteFile(cl,
		fmt.Sprintf("cd /opt/1panel/apps/openresty/openresty/www/sites/%s/index 2>/dev/null && tar cf - . 2>/dev/null", p1ShQuote(alias)),
		filepath.Join(meta, "site.tar"), 10*60*time.Second); err != nil {
		fmt.Fprintf(w, "   站点文件拉取失败(%v),目录为空可手工补齐\n", err)
		return
	}
	tarPath := filepath.Join(meta, "site.tar")
	if b, _ := os.Stat(tarPath); b == nil || b.Size() < 32 {
		os.Remove(tarPath)
		fmt.Fprintf(w, "   远程站点目录为空,跳过文件拉取\n")
		return
	}
	_ = os.MkdirAll(panelRoot, 0o755)
	if _, err := util.Run("sh", "-c", fmt.Sprintf("cd %s && tar xf %s && rm -f %s",
		p1ShQuote2(panelRoot), p1ShQuote2(tarPath), p1ShQuote2(tarPath))); err != nil {
		fmt.Fprintf(w, "   站点文件解压失败(%v)\n", err)
		return
	}
	fmt.Fprintf(w, "   站点文件已拉取(远程 %s → %s)\n", alias, panelRoot)
}

// p1RemoteMigrateDB 远程 MySQL 容器 mysqldump → 本机 → 面板 MySQL
func p1RemoteMigrateDB(cl *ssh.Client, dbs []P1DB, name string, w io.Writer) error {
	var host string
	for _, d := range dbs {
		if d.Name == name {
			host = d.Host
		}
	}
	if host == "" {
		return fmt.Errorf("1Panel 中不存在数据库 %s", name)
	}
	// 远程找数据库容器
	out, err := p1Run(cl, 20*time.Second, "docker ps --format '{{.Names}}'")
	if err != nil {
		return fmt.Errorf("远程 docker ps 失败: %v", err)
	}
	var container string
	for _, n := range strings.Split(out, "\n") {
		n = strings.TrimSpace(n)
		if n != "" && strings.Contains(n, host) {
			container = n
			break
		}
	}
	if container == "" {
		return fmt.Errorf("远程未找到数据库容器(host=%s)", host)
	}
	envOut, err := p1Run(cl, 20*time.Second, fmt.Sprintf("docker inspect %s --format '{{range .Config.Env}}{{println .}}{{end}}'", p1ShQuote(container)))
	if err != nil {
		return fmt.Errorf("读取容器环境失败: %v", err)
	}
	rootPW := ""
	for _, ln := range strings.Split(envOut, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "MYSQL_ROOT_PASSWORD=") {
			rootPW = strings.TrimPrefix(ln, "MYSQL_ROOT_PASSWORD=")
		}
	}
	if rootPW == "" {
		return fmt.Errorf("远程容器 %s 未设置 MYSQL_ROOT_PASSWORD,无法导出", container)
	}
	tmp := filepath.Join(util.DataDir(), "p1remote_"+name+".sql")
	defer os.Remove(tmp)
	dumpCmd := fmt.Sprintf("docker exec %s sh -c %s > %s",
		p1ShQuote(container),
		p1ShQuote(fmt.Sprintf("mysqldump -uroot -p'%s' --single-transaction --quick %s", strings.ReplaceAll(rootPW, "'", ""), name)),
		p1ShQuote(tmp))
	if err := p1StreamRemoteFile(cl, dumpCmd, tmp, 10*60*time.Second); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("远程 mysqldump 失败: %v", err)
	}
	if fi, rerr := os.Stat(tmp); rerr != nil || fi.Size() < 64 {
		os.Remove(tmp)
		return fmt.Errorf("远程 mysqldump 输出为空(out=%s)", tailOf(out, 150))
	}
	fmt.Fprintf(w, "   已导出 %s(远程 1Panel MySQL 容器)\n", name)
	if err := dbmgrCreateAndImport(name, tmp); err != nil {
		return err
	}
	fmt.Fprintf(w, "   %s 已导入面板 MySQL。注意:站点连接串需改指面板 MySQL\n", name)
	return nil
}
