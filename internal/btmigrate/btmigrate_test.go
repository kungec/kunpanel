package btmigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleBTConf = `server {
    listen 80;
    server_name upay.test;
    index index.php;
    root /www/wwwroot/upay.test;
    #SSL-START
    #SSL-STOP
    include enable-php-74.conf;
    #REWRITE-START
    include /www/server/panel/vhost/rewrite/upay.test.conf;
    #REWRITE-END
    if ($server_port !~ 443) {
        rewrite ^(.*)$ https://$host$1 permanent;
    }
    location /bepay/ {
        proxy_pass http://127.0.0.1:8080/;
        proxy_set_header X-Real-IP $remote_addr;
    }
    location ~ [^/]\.php(/|$) {
        try_files $uri =404;
        fastcgi_pass unix:/tmp/php-cgi-74.sock;
        fastcgi_index index.php;
        include fastcgi.conf;
    }
    access_log /www/wwwlogs/upay.test.log;
}
`

// 宝塔自定义配置保真提取:宝塔内部指令/标记/PHP fastcgi 块剔除,业务 location 保留且回环改写
func TestExtractExtras(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "upay.test.conf")
	if err := os.WriteFile(conf, []byte(sampleBTConf), 0o644); err != nil {
		t.Fatal(err)
	}
	extra, locs, _, err := extractExtras(conf, "php")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(extra, "server_name") || strings.Contains(extra, "access_log") {
		t.Error("面板会生成的基础指令不应出现在额外配置里")
	}
	if strings.Contains(extra, "enable-php") || strings.Contains(extra, "vhost/rewrite") {
		t.Error("宝塔内部 include 不应保留")
	}
	if strings.Contains(extra, "$server_port") {
		t.Error("宝塔 SSL 301 跳转 if 块应剔除(面板有自己的强制跳转)")
	}
	if strings.Contains(extra, "fastcgi_pass") || strings.Contains(extra, "fastcgi.conf") {
		t.Error("php 站点的宝塔 fastcgi location 应整块剔除(面板自己生成)")
	}
	if strings.Contains(extra, "#SSL-STOP") || strings.Contains(extra, "#REWRITE-START") {
		t.Error("宝塔标记注释应剔除")
	}
	if !strings.Contains(extra, "location /bepay/") {
		t.Error("业务 location 应保留")
	}
	if !strings.Contains(extra, "http://172.17.0.1:8080/") {
		t.Error("回环地址应改写为宿主机网关")
	}
	if strings.Contains(extra, "127.0.0.1") {
		t.Error("不应残留 127.0.0.1(容器内不可达宿主机)")
	}
	if locs != 1 {
		t.Errorf("保留 location 数 = %d, want 1", locs)
	}
}

// 纯反代站(ppe 形态):if/标记剔除后仍保留 2 个反代 location
func TestExtractExtrasProxySite(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "ppe.test.conf")
	confTxt := strings.Replace(sampleBTConf, "upay.test", "ppe.test", -1)
	if err := os.WriteFile(conf, []byte(confTxt), 0o644); err != nil {
		t.Fatal(err)
	}
	// proxy 类型:宝塔 fastcgi location 也应剔除(反代站用不到 PHP)
	extra, locs, _, err := extractExtras(conf, "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(extra, "fastcgi_pass") {
		t.Error("proxy 站点不应保留宝塔 fastcgi location")
	}
	if !strings.Contains(extra, "location /bepay/") {
		t.Error("业务 location 应保留")
	}
	if locs != 1 {
		t.Errorf("保留 location 数 = %d, want 1", locs)
	}
}

// 宝塔原生两行 server/{ 格式 + enable-php-00 纯静态 + ssl_session 指令
const nativeBTConf = `server
{
    listen 80;
    server_name native.test;
    index index.php;
    root /www/wwwroot/native.test;
    #SSL-START
    #SSL-STOP
    include enable-php-00.conf;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 10m;
    #PROXY-START
    location /sub-store/ {
        proxy_pass http://127.0.0.1:5000/;
        proxy_set_header Host $host;
    }
    #PROXY-END
    access_log /www/wwwlogs/native.test.log;
}
`

// 宝塔原生格式:server 与 { 分两行、enable-php-00 纯静态、ssl_session_ 过滤、反代保留
func TestExtractExtrasNativeFormat(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "native.test.conf")
	if err := os.WriteFile(conf, []byte(nativeBTConf), 0o644); err != nil {
		t.Fatal(err)
	}
	// 站点识别:enable-php-00 → 纯静态(不是 PHP 00)
	site, err := parseSiteConf(conf)
	if err != nil {
		t.Fatal(err)
	}
	if site.Type == "php" || site.PhpVer != "" {
		t.Errorf("enable-php-00 应视为纯静态,got type=%s php_ver=%s", site.Type, site.PhpVer)
	}
	extra, locs, _, err := extractExtras(conf, site.Type)
	if err != nil {
		t.Fatal(err)
	}
	// ssl_session 指令必须被剔除(否则与面板生成的重复,nginx -t 失败)
	if strings.Contains(extra, "ssl_session") {
		t.Error("ssl_session_ 指令应被剔除")
	}
	// 反代子目录保留且回环改写为宿主机网关
	if !strings.Contains(extra, "location /sub-store/") {
		t.Error("反代子目录应保留")
	}
	if !strings.Contains(extra, "172.17.0.1:5000") || strings.Contains(extra, "127.0.0.1:5000") {
		t.Error("反代上游应改写为宿主机网关")
	}
	if locs != 1 {
		t.Errorf("保留 location 数 = %d, want 1", locs)
	}
	// firstServerBlock 支持两行格式本身已在上方隐式验证(能提取即通过)
}

// ---------- 验收样例(宝塔原生格式,含 HTTP3/Alt-Svc/purge/proxy 子目录) ----------

const sampleNative443 = `server
{
    listen 443 ssl http3;
    http3 on;
    quic_retry on;
    server_name demo.example.com;
    root /www/wwwroot/demo.example.com;
    include /www/server/panel/vhost/nginx/well-known/demo.example.com.conf;
    ssl_session_cache shared:SSL:10m;
    add_header Alt-Svc 'h3=":443"';
    location ~ /purge(/.*) { proxy_cache_purge cache_one $host$1; }
    include /www/server/panel/vhost/nginx/proxy/demo.example.com/*.conf;
    include enable-php-00.conf;
}
`

const sampleProxyDirFile = `location ~ / {
    proxy_pass http://127.0.0.1:8081;
    proxy_set_header Connection $connection_upgrade;
}

location /api/ { proxy_pass http://127.0.0.1:8080; }
`

// 验收:提取端口恰为 [8081 8080];结果不含 /www/server/panel 的 include、
// proxy_cache_purge、ssl_*、http3、quic、Alt-Svc;443 不进端口表
func TestExtractExtrasNative443Sample(t *testing.T) {
	dir := t.TempDir()
	proxyDir := filepath.Join(dir, "www", "server", "panel", "vhost", "nginx", "proxy", "demo.example.com")
	if err := os.MkdirAll(proxyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxyDir, "a_demo.example.com.conf"), []byte(sampleProxyDirFile), 0o644); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "demo.example.com.conf")
	if err := os.WriteFile(conf, []byte(sampleNative443), 0o644); err != nil {
		t.Fatal(err)
	}
	// rootRemap 把宝塔 include 路径重映射到临时目录(模拟远程元数据包布局)
	extra, _, ports, err := extractExtrasAt(conf, "static", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 2 || ports[0] != 8081 || ports[1] != 8080 {
		t.Fatalf("提取端口 = %v, want [8081 8080]", ports)
	}
	for _, bad := range []string{"/www/server/panel", "proxy_cache_purge", "ssl_", "http3", "quic", "Alt-Svc", "location ~ /purge"} {
		if strings.Contains(extra, bad) {
			t.Errorf("提取结果不应包含 %q:\n%s", bad, extra)
		}
	}
	if !strings.Contains(extra, "proxy_pass http://172.17.0.1:8081") {
		t.Errorf("反代上游应改写为网关地址:\n%s", extra)
	}
	if !strings.Contains(extra, "$connection_upgrade") {
		t.Errorf("websocket 头应保留:\n%s", extra)
	}
}

// 回环上游端口识别:只认 proxy_pass/grpc_pass 指向回环地址
func TestLoopbackUpstreamPort(t *testing.T) {
	if p, ok := loopbackUpstreamPort("http://127.0.0.1:8080"); !ok || p != 8080 {
		t.Errorf("127.0.0.1:8080 → %d,%v", p, ok)
	}
	if p, ok := loopbackUpstreamPort("http://localhost:3001"); !ok || p != 3001 {
		t.Errorf("localhost:3001 → %d,%v", p, ok)
	}
	if _, ok := loopbackUpstreamPort("http://172.17.0.1:8080"); ok {
		t.Error("非回环地址不应识别为回环上游")
	}
	if _, ok := loopbackUpstreamPort("http://192.168.1.5:8080"); ok {
		t.Error("局域网地址不应识别为回环上游")
	}
}

// 转发器受保护端口:80/443 一律拒绝(转发器重启后抢占网关端口会全站宕机)
func TestProtectedPortReason(t *testing.T) {
	if protectedPortReason(80) == "" || protectedPortReason(443) == "" {
		t.Error("80/443 必须被拒绝")
	}
	if protectedPortReason(8080) != "" || protectedPortReason(9071) != "" {
		t.Error("普通端口不应被拒绝")
	}
}

// spool 行按 echo 整词匹配:md5 前缀撞车不误伤
func TestEchoMatchers(t *testing.T) {
	line := `52 19 * * *  flock -xn /www/server/cron/11448451d956f42bd7da779cd72bf6c1.lock -c /www/server/cron/11448451d956f42bd7da779cd72bf6c1  >> /www/server/cron/11448451d956f42bd7da779cd72bf6c1.log 2>&1`
	hit := echoMatchers([]string{"11448451d956f42bd7da779cd72bf6c1"})
	if len(hit) != 1 || !hit[0].MatchString(line) {
		t.Error("echo 整词应命中 spool 行(.lock/.log/空格边界)")
	}
	// 前缀撞车:另一个 md5 恰好以它为前缀,不应误匹配
	prefix := echoMatchers([]string{"11448451d956f42bd7da779cd72bf6c"})
	if prefix[0].MatchString(line) {
		t.Error("md5 前缀不应命中更长的 echo 行")
	}
	if echoMatchers([]string{"ffffffffffffffffffffffffffffffff"})[0].MatchString(line) {
		t.Error("无关 echo 不应命中")
	}
}

// 宝塔任务启用状态:11.x status=1 启用;老 schema(sType 存在)0=正常;缺省启用
func TestParseBTCronEnabled(t *testing.T) {
	c11 := parseBTCron(map[string]any{"name": "备份", "type": "day", "where_hour": "2", "where_minute": "30", "status": "1"})
	if !c11.Enabled {
		t.Error("11.x status=1 应为启用")
	}
	c11off := parseBTCron(map[string]any{"name": "备份", "type": "day", "where_hour": "2", "where_minute": "30", "status": "0"})
	if c11off.Enabled {
		t.Error("11.x status=0 应为停用")
	}
	cold := parseBTCron(map[string]any{"name": "日志切割", "sType": "day", "where_hour": "1", "where_minute": "0", "status": "0"})
	if !cold.Enabled {
		t.Error("老 schema status=0 应为启用(语义相反)")
	}
	cdef := parseBTCron(map[string]any{"name": "任务", "type": "day", "where_hour": "1", "where_minute": "0"})
	if !cdef.Enabled {
		t.Error("status 列缺失应默认启用")
	}
}

// 宝塔内部任务(sBody 引用 /www/server/panel 或 database/site/path 类型)
// 默认不可迁移;普通任务不受影响
func TestParseBTCronInternal(t *testing.T) {
	ci := parseBTCron(map[string]any{"name": "备份站点", "type": "day", "where_hour": "1", "where_minute": "0",
		"sBody": "bash /www/server/panel/script/backup_site.sh"})
	if !ci.Internal || ci.Mig {
		t.Errorf("引用 /www/server/panel 的任务应为内部任务(Mig=false),got internal=%v mig=%v", ci.Internal, ci.Mig)
	}
	cd := parseBTCron(map[string]any{"name": "数据库备份", "type": "database", "where1": "testdb"})
	if !cd.Internal || cd.Mig {
		t.Errorf("database 类型应为内部任务,got internal=%v mig=%v", cd.Internal, cd.Mig)
	}
	cn := parseBTCron(map[string]any{"name": "重启服务", "type": "day", "where_hour": "3", "where_minute": "0",
		"sBody": "systemctl restart myapp"})
	if cn.Internal || !cn.Mig || cn.Spec != "0 3 * * *" {
		t.Errorf("普通任务不应被误判内部任务: internal=%v mig=%v spec=%q", cn.Internal, cn.Mig, cn.Spec)
	}
}

// compose 端口改写:带引号/不带引号/协议后缀/幂等
func TestRewriteComposePorts(t *testing.T) {
	in := `services:
  app:
    ports:
      - "8080:80"
      - 8443:443/tcp
`
	out, o80, o443, changed := rewriteComposePorts(in)
	if !changed || o80 != 8080 || o443 != 8443 {
		t.Fatalf("changed=%v orig=%d/%d, want true 8080/8443", changed, o80, o443)
	}
	if !strings.Contains(out, `"80:80"`) || !strings.Contains(out, "443:443") {
		t.Errorf("改写结果不对:\n%s", out)
	}
	if strings.Contains(out, "8080") || strings.Contains(out, "8443") {
		t.Errorf("旧端口应被替换:\n%s", out)
	}
	// 幂等:已绑定 80/443 时再次改写无变化
	_, o80b, o443b, changed2 := rewriteComposePorts(out)
	if changed2 || o80b != 0 || o443b != 0 {
		t.Errorf("重复改写应幂等: changed=%v orig=%d/%d", changed2, o80b, o443b)
	}
}
