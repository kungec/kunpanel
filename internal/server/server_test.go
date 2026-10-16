package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// 回归(2026-10-15 崩溃事故):initAssetVer 的内联脚本 CSP 正则必须兼容 RE2
// (Go regexp 无负向先行断言),且启动时 panic 会让整个面板无法启动。
func TestInitAssetVerAndCSP(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<!DOCTYPE html><html><head><script>try{if(localStorage.getItem('kp_theme')==='dark')document.documentElement.setAttribute('data-theme','dark')}catch(e){}</script><script src="/static/app.js?v=__ASSET_VER__"></script></head><body></body></html>`)},
		"app.js":     &fstest.MapFile{Data: []byte("console.log(1)")},
		"app.css":    &fstest.MapFile{Data: []byte("body{}")},
	}
	s := &Server{webFS: fsys, limiter: nil}
	s.initAssetVer() // 曾在此 panic:regexp.MustCompile 负向断言
	if s.assetVer == "" {
		t.Fatal("assetVer 未计算")
	}
	if s.cspScript == "" || !contains(s.cspScript, "'sha256-") {
		t.Fatalf("CSP 未包含内联脚本哈希: %s", s.cspScript)
	}
	// 注入验证
	out := string(s.injectAssetVer([]byte("app.js?v=__ASSET_VER__")))
	if contains(out, "__ASSET_VER__") || !contains(out, "v="+s.assetVer) {
		t.Fatalf("版本注入失败: %s", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// CSP 中间件冒烟:脚本哈希进入响应头
func TestCSPHeaderContainsHash(t *testing.T) {
	fsys := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html><script>var a=1;</script></html>")}}
	s := &Server{webFS: fsys}
	s.initAssetVer()
	rec := httptest.NewRecorder()
	s.withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if csp := rec.Header().Get("Content-Security-Policy"); !contains(csp, "'sha256-") {
		t.Fatalf("CSP 缺少脚本哈希: %s", csp)
	}
}
