package server

import (
	"runtime/debug"
	"bytes"
	"compress/gzip"

	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/authx"
	"kunpanel/internal/config"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/logsx"
	"kunpanel/internal/util"
)

// Version 面板版本号,由 main 注入
var Version = "dev"

// Server 面板 HTTP 服务
type Server struct {
	mux         *http.ServeMux
	limiter     *authx.Limiter
	webFS       fs.FS
	assetVer    string // 静态资源内容哈希(自动 cache-bust,替代手工 ?v= 递增)
	cspScript   string // script-src 值(按 index.html 内联脚本动态计算哈希)
}

// Options 服务构建参数
type Options struct {
	Version string
	WebFS   fs.FS
}

// New 构建 HTTP 服务(路由 + 中间件)
func New(o Options) *http.Server {
	Version = o.Version
	cfg := config.M.Get()

	s := &Server{
		mux:         http.NewServeMux(),
		limiter:     authx.NewLimiter(),
		webFS:       o.WebFS,
	}
	s.initAssetVer()
	s.registerAPI()
	s.mux.Handle("/", s.spaHandler())

	// 限流器 janitor:定期清理过期条目,长期运行防 map 缓慢增长
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			s.limiter.Sweep()
			UserLimiter.Sweep()
		}
	}()

	return &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: s.withSecurityHeaders(s.withRecover(s.withOpLog(s.mux))),
		// 慢速攻击防护:头读取限时 + 空闲连接回收。
		// 不设 Read/WriteTimeout:终端 WS 升级后长连接与大文件下载需要不受限的读写
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// ---------- 响应辅助 ----------

type apiResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func OK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, apiResp{Code: 0, Message: "success", Data: data})
}

func Fail(w http.ResponseWriter, status int, msg string) {
	if status == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, apiResp{Code: status, Message: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	return dec.Decode(v)
}

// ---------- 中间件 ----------

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v %s\n%s", rec, r.URL.Path, debug.Stack())
				Fail(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusWriter 捕获真实 HTTP 状态码供审计日志使用
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// withOpLog 记录变更类 API 的操作日志(记录真实状态码,失败操作不再显示为成功)
func (s *Server) withOpLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead {
			ip := ClientIP(r)
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			// 日志清理动作自身不再写入操作日志,否则清空后立刻出现一条
			// "清空"记录,看起来像清理无效
			if strings.HasSuffix(r.URL.Path, "/clear") && strings.Contains(r.URL.Path, "/logs") {
				return
			}
			logsx.AppendOp(ip, r.Method, r.URL.Path, "", sw.status)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// auth API 鉴权(支持 Authorization 头与 ?token= 两种方式)
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := config.M.Get()
		token := BearerToken(r)
		if authx.VerifyToken(cfg.JWTSecret, token, cfg.Username) {
			next.ServeHTTP(w, r)
			return
		}
		Fail(w, http.StatusUnauthorized, "未登录或登录已过期")
	})
}

func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	return ""
}

func ClientIP(r *http.Request) string {
	// 仅在显式声明处于可信反代之后(KP_TRUST_PROXY=1)时才采信转发头,
	// 否则以 TCP 对端为准,防止伪造 X-Forwarded-For 绕过登录锁定。
	if os.Getenv("KP_TRUST_PROXY") == "1" {
		if v := r.Header.Get("X-Real-IP"); v != "" {
			return v
		}
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			return strings.TrimSpace(strings.Split(v, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// withSecurityHeaders 附加基础安全响应头。
// script-src 用 index.html 内联脚本的动态哈希替代 'unsafe-inline':
// 前端无任何内联事件处理器(全 JS 绑定),哈希收紧后功能不变而 XSS 面大幅缩小。
// style-src 保留 'unsafe-inline':前端大量使用 style="" 行内样式。
func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-XSS-Protection", "0")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// 构建版本号:前端比对自身加载的 ?v=,面板升级后提示刷新,
		// 避免长驻标签页一直跑旧版前端(用户误报"功能缺失")
		h.Set("X-Panel-Build", s.assetVer)
		h.Set("Content-Security-Policy", s.cspScript+
			" style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self' ws: wss:; "+
			"font-src 'self' data:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		// 标记面板活跃:Docker 后台刷新据此自适应降频(空闲 30s,活跃 3s)
		dockerx.MarkActive()
		next.ServeHTTP(w, r)
	})
}

// safeID 路径参数白名单校验(用于拼文件路径 / 命令参数的 id,key 等)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func safeID(s string) (string, bool) {
	return s, idPattern.MatchString(s)
}

// ---------- 静态资源 / SPA(含安全入口门禁) ----------

func (s *Server) spaHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		cfg := config.M.Get()

		// 安全入口:未通过门禁的请求一律 404("-" 表示已在设置中关闭)
		if entranceEnabled(cfg.Entrance) {
			if r.URL.Path == "/"+cfg.Entrance {
				http.SetCookie(w, &http.Cookie{
					Name: "kp_entrance", Value: entranceCookieValue(cfg.JWTSecret), Path: "/",
					HttpOnly: true, MaxAge: 7 * 86400, SameSite: http.SameSiteLaxMode,
				})
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			if !s.hasAccess(r) {
				http.NotFound(w, r)
				return
			}
		}

		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if st, err := fs.Stat(s.webFS, p); err == nil && !st.IsDir() {
			// 入口 HTML:no-store(几 KB,必须每次拉新,杜绝旧版本号资源被缓存引用)
			// 静态资源:no-cache + ETag(变化即失效,未变化 304 复验)
			b, rerr := fs.ReadFile(s.webFS, p)
			if rerr != nil {
				http.Error(w, "read error", http.StatusInternalServerError)
				return
			}
			if p == "index.html" {
				b = s.injectAssetVer(b)
			}
			etag := staticETag(p, b)
			w.Header().Set("ETag", `"`+etag+`"`)
			if p == "index.html" {
				w.Header().Set("Cache-Control", "no-store")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			if etagMatches(r.Header.Get("If-None-Match"), etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			serveStaticBytes(w, r, p, b)
			return
		}
		// SPA fallback:其余路径回退到 index.html
		index, err := fs.ReadFile(s.webFS, "index.html")
		if err != nil {
			http.Error(w, "前端资源缺失,请重新安装面板", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(s.injectAssetVer(index))
	})
}

// entranceEnabled 入口是否启用("-" 为显式关闭)
func entranceEnabled(e string) bool {
	return e != "" && e != "-"
}

// staticETag 内容哈希(同一路径只算一次,嵌入文件运行期不变)
var etagCache sync.Map

func staticETag(path string, b []byte) string {
	if v, ok := etagCache.Load(path); ok {
		return v.(string)
	}
	sum := sha1.Sum(b)
	etag := hex.EncodeToString(sum[:8]) + "-" + strconv.Itoa(len(b))
	etagCache.Store(path, etag)
	return etag
}

// etagMatches 宽松匹配 If-None-Match(支持多值与 W/ 前缀)
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		part = strings.Trim(part, `"`)
		if part == etag || part == "*" {
			return true
		}
	}
	return false
}

// serveStaticBytes 按扩展名 gzip 压缩输出(慢速链路下体积减约 70%)
func serveStaticBytes(w http.ResponseWriter, r *http.Request, path string, b []byte) {
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	if ct == "text/html" {
		ct = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	ext := strings.ToLower(filepath.Ext(path))
	compressible := false
	switch ext {
	case ".js", ".css", ".html", ".json", ".svg", ".txt", ".xml":
		compressible = true
	}
	if compressible && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write(b)
		_ = gz.Close()
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// entranceCookieValue 门禁 Cookie 的值:基于 JWT 密钥的 HMAC,
// 攻击者无法伪造(第二轮渗透修复:旧版静态值 kp_entrance=1 可被直接伪造绕过入口)
func entranceCookieValue(secret string) string {
	return util.HMAC(secret, "kp-entrance-gate")
}

func (s *Server) hasAccess(r *http.Request) bool {
	cfg := config.M.Get()
	if ck, err := r.Cookie("kp_entrance"); err == nil && ck.Value != "" {
		if util.ConstantTimeEq(entranceCookieValue(cfg.JWTSecret), ck.Value) {
			return true
		}
	}
	return authx.VerifyToken(cfg.JWTSecret, BearerToken(r), cfg.Username)
}

// initAssetVer 启动时对内嵌静态资源整体计算内容哈希,
// 作为前端 ?v= 版本号注入 index.html —— 彻底替代手工递增版本号(历史上多次因忘 bump 导致缓存不刷新)
func (s *Server) initAssetVer() {
	h := sha256.New()
	var index []byte
	err := fs.WalkDir(s.webFS, ".", func(path string, d fs.DirEntry, err error) error {
		if path == "index.html" {
			if b, e := fs.ReadFile(s.webFS, path); e == nil {
				index = b
			}
		}
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(s.webFS, path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write(b)
		return nil
	})
	if err != nil {
		s.assetVer = fmt.Sprintf("%x", time.Now().Unix())
		s.cspScript = "default-src 'self'; script-src 'self';"
		return
	}
	s.assetVer = fmt.Sprintf("%x", h.Sum(nil))[:10]

	// 内联 <script> 逐块计算 sha256 → CSP 白名单(带 src= 的外链脚本跳过)。
	// 注意 Go RE2 不支持负向先行断言,外链判断在循环内做
	hashes := ""
	re := regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)
	for _, m := range re.FindAllSubmatch(index, -1) {
		if bytes.Contains(m[1], []byte("src=")) {
			continue
		}
		sum := sha256.Sum256(m[2])
		hashes += fmt.Sprintf(" 'sha256-%s'", base64.StdEncoding.EncodeToString(sum[:]))
	}
	if hashes == "" {
		hashes = " 'unsafe-inline'"
	}
	s.cspScript = fmt.Sprintf("default-src 'self'; script-src 'self'%s;", hashes)
}

// injectAssetVer 把 index.html 里的 __ASSET_VER__ 占位符替换为资源哈希
func (s *Server) injectAssetVer(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("__ASSET_VER__"), []byte(s.assetVer))
}
