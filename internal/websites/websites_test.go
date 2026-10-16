package websites

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeProxyHost(t *testing.T) {
	ok := map[string]string{
		"":            "",         // 空 = 默认
		"  ":          "",         // 空白 = 默认
		"$http_host":  "$http_host",
		"api.example.com": "api.example.com",
		"$host:8080":  "$host:8080",
		"[::1]:9000":  "[::1]:9000",
	}
	for in, want := range ok {
		if got := SanitizeProxyHost(in); got != want {
			t.Errorf("SanitizeProxyHost(%q) = %q, want %q", in, got, want)
		}
	}
	bad := []string{"a;b", "a b", "a{}b", "a\nb", "proxy_pass http://evil"}
	for _, in := range bad {
		if got := SanitizeProxyHost(in); got != "" {
			t.Errorf("SanitizeProxyHost(%q) = %q, 应整体丢弃回默认", in, got)
		}
	}
}

func TestProxyHostHeader(t *testing.T) {
	s := &Site{}
	if proxyHostHeader(s) != "$http_host" {
		t.Errorf("空 ProxyHost 应默认 $http_host, got %s", proxyHostHeader(s))
	}
	s.ProxyHost = "api.example.com"
	if proxyHostHeader(s) != "api.example.com" {
		t.Errorf("自定义 Host 应生效, got %s", proxyHostHeader(s))
	}
	s.ProxyHost = "bad;header"
	if proxyHostHeader(s) != "$http_host" {
		t.Errorf("注入字符应回退默认, got %s", proxyHostHeader(s))
	}
}

// 类型互转:static → proxy → php,状态与配置文件同步变化
func TestSetSiteType(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KP_DATA_DIR", dir)

	s, err := Create(CreateOpts{Domain: "conv.test", SiteType: "static"})
	if err != nil {
		t.Fatal(err)
	}
	// static → proxy
	if err := SetSiteType(s.ID, "proxy", "http://127.0.0.1:8080", "", ""); err != nil && !strings.Contains(err.Error(), "Nginx") {
		t.Fatalf("static→proxy: %v", err)
	}
	got, err := GetSite(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "proxy" || got.ProxyTo != "http://127.0.0.1:8080" {
		t.Errorf("转换后 type=%s proxy_to=%s", got.Type, got.ProxyTo)
	}
	conf, _ := os.ReadFile(filepath.Join(dir, "websites", "conf.d", s.ID+".conf"))
	if !strings.Contains(string(conf), "proxy_pass http://127.0.0.1:8080;") {
		t.Errorf("conf 未按 proxy 生成:\n%s", conf)
	}
	// proxy → php(无 nginx 环境 Reload 会报错,状态与配置应已落盘)
	_ = SetSiteType(s.ID, "php", "", "php82", "api.conv.test")
	got, _ = GetSite(s.ID)
	if got.Type != "php" || got.PhpKey != "php82" {
		t.Errorf("php 转换 type=%s php_key=%s", got.Type, got.PhpKey)
	}
	conf, _ = os.ReadFile(filepath.Join(dir, "websites", "conf.d", s.ID+".conf"))
	if !strings.Contains(string(conf), "proxy_set_header Host api.conv.test;") {
		t.Errorf("自定义 Host 未写入 conf:\n%s", conf)
	}
	// php → static
	_ = SetSiteType(s.ID, "static", "", "", "")
	got, _ = GetSite(s.ID)
	if got.Type != "static" {
		t.Errorf("static 转换 type=%s", got.Type)
	}
	conf, _ = os.ReadFile(filepath.Join(dir, "websites", "conf.d", s.ID+".conf"))
	if strings.Contains(string(conf), "proxy_pass") {
		t.Errorf("转回静态后不应再有 proxy_pass:\n%s", conf)
	}
	// 非法目标类型被拒
	if err := SetSiteType(s.ID, "phpfpm", "", "", ""); err == nil {
		t.Error("phpfpm 目标应被拒绝(需 fastcgi 上游,不走互转)")
	}
}
