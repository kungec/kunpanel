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
	extra, locs, err := extractExtras(conf, "php")
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
	extra, locs, err := extractExtras(conf, "proxy")
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
