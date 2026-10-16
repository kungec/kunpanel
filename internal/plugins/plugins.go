// Package plugins 面板插件系统:插件内置在面板二进制中,按需安装/卸载。
// 当前插件:waf —— 基于 OpenResty + Lua 的 Nginx 防火墙(CC/黑白名单/注入检测)。
package plugins

import (
	"net"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/appstore"
	"kunpanel/internal/config"
	"kunpanel/internal/websites"
	"kunpanel/internal/util"
)

//go:embed wafassets/waf.lua wafassets/kunwaf.conf
var assets embed.FS

// Info 插件描述(对外)
type Info struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Desc         string   `json:"desc"`
	Status       string   `json:"status"` // not_installed / installed
	Active       bool     `json:"active"`
	Requires     []string `json:"requires"`      // 依赖的应用 key
	RequiresNote string   `json:"requires_note"` // 依赖说明(未满足时展示)
}

var wafDir = func() string { return filepath.Join(config.DataDir(), "plugins", "waf") }

func wafSettingsPath() string { return filepath.Join(wafDir(), "config.json") }
func wafLogPath() string      { return filepath.Join(wafDir(), "logs", "waf.log") }
func wafConfSnippet() string {
	return filepath.Join(config.DataDir(), "websites", "conf.d", "kunwaf.conf")
}
func wafInstalled() bool { return exists(wafConfSnippet()) }

// WafActive WAF 是否生效(已安装且总开关开启)——供网站模块生成配置时调用
func WafActive() bool {
	return wafInstalled() && GetWafSettings().Enabled
}

// Init 注入网站模块钩子
func Init() {
	websites.WafEnabled = WafActive
	_ = loadWafConfig() // 触发旧版规则迁移(?i 内联标志清理)
	syncWafLua()
}

// syncWafLua 升级同步:面板二进制内嵌的 waf.lua 比磁盘新(或内容不同)时自动覆盖,
// 保证 WAF 引擎随面板升级(旧部署的 waf.lua 不会停留在旧版本)。
func syncWafLua() {
	dst := filepath.Join(wafDir(), "waf.lua")
	b, err := assets.ReadFile("wafassets/waf.lua")
	if err != nil {
		return
	}
	if old, e := os.ReadFile(dst); e == nil && string(old) == string(b) {
		return
	}
	_ = os.MkdirAll(wafDir(), 0o755)
	_ = os.WriteFile(dst, b, 0o644)
}

// List 插件列表
func List() []Info {
	gwOK := gatewayContainer() != ""
	status := "not_installed"
	active := false
	if wafInstalled() {
		status = "installed"
		active = WafActive() && gwOK
	}
	return []Info{{
		Key:          "waf",
		Name:         "Nginx 防火墙 (WAF)",
		Version:      "1.1.0",
		Desc:         "基于 nginx + Lua 的网站防火墙:支持官方 Nginx 一键开启(自动装配 Lua 引擎版 nginx)或 OpenResty;CC 攻击防护、IP/UA 黑白名单、SQL 注入 / XSS / 文件包含 / 命令执行 / 扫描器拦截,带拦截日志与统计看板",
		Status:       status,
		Active:       active,
		Requires:     []string{"nginx"},
		RequiresNote: "需要先在应用商店安装 Nginx 或 OpenResty(网站模块依赖);对 Nginx 开启 WAF 时,面板会自动编译并替换为带 Lua 引擎的 nginx 镜像(首次约 5-8 分钟,站点配置不受影响)",
	}}
}

// ---------- WAF 设置 ----------

// WafSettings 面板侧可配置项
type WafSettings struct {
	Enabled        bool            `json:"enabled"`
	Mode           string          `json:"mode"` // block / alert / monitor
	CcEnable       bool            `json:"cc_enable"`
	CcLimit        int             `json:"cc_limit"`
	CcInterval     int             `json:"cc_interval"`
	CcMode         string          `json:"cc_mode"`      // rate(默认限流403) / jump(5秒跳转) / js(JS无感) / cf(CF风格Verifying) / click(点击) / captcha(验证码)
	CcSecret       string          `json:"cc_secret"`    // 挑战令牌签名密钥(自动生成)
	CcClearTTL     int             `json:"cc_clear_ttl"` // 验证通过后的放行时长(秒)
	IpBanEnable    bool            `json:"ip_ban_enable"`
	IpBanThreshold int             `json:"ip_ban_threshold"`
	IpBanDuration  int             `json:"ip_ban_duration"`
	IpWhite        []string        `json:"ip_white"`
	IpBlack        []string        `json:"ip_black"`
	UrlWhite       []string        `json:"url_white"`
	UaBlack        []string        `json:"ua_black"`
	RuleGroups     map[string]bool `json:"rule_groups"`
	// 蜘蛛白名单:启用的搜索引擎爬虫完全跳过检测(CC 验证也不会拦)
	SpiderWhiteEnable bool      `json:"spider_white_enable"`
	SpiderWhite       []string  `json:"spider_white"`
	// SpiderVerifyDNS 严格模式:UA 命中蜘蛛白名单后,再做反向 DNS + 正向确认,
	// 防止攻击者伪造 Googlebot UA 整体绕过 WAF(第三轮审计遗留建议)。默认关闭。
	SpiderVerifyDNS bool `json:"spider_verify_dns"`
	// 危险 HTTP 方法拦截
	MethodBlockEnable bool     `json:"method_block_enable"`
	MethodBlockList   []string `json:"method_block_list"`
	// 自定义检测规则(用户在面板增删,保存即热更新;命中走 拦截/告警/监控 全局模式)
	CustomRules []CustomRule `json:"custom_rules"`
	// 增强防护:空 UA 拦截(常见脚本工具特征)/ HTTP/1.0 请求拦截(扫描器常用)
	EmptyUaBlock bool `json:"empty_ua_block"`
	Http10Block  bool `json:"http10_block"`
	// 站点级 WAF 开关:这些 host 不启用 WAF(纯 API/内网站点)
	SiteOff []string `json:"site_off"`
	// 自动封禁:24 小时内违规次数达阈值的 IP 自动加入持久黑名单(区别于手工维护的 ip_black)
	AutoBanEnable    bool     `json:"auto_ban_enable"`
	AutoBanThreshold int      `json:"auto_ban_threshold"` // 24h 违规次数阈值
	AutoBlackList    []string `json:"auto_black_list"`    // 自动封禁的 IP(由面板维护,UI 只读)
	// 蜜罐诱捕:访问 honeypot_paths 中路径(如假后台)的 IP 立即长期封禁
	HoneypotEnable bool     `json:"honeypot_enable"`
	HoneypotPaths  []string `json:"honeypot_paths"`
	// 敏感路径限速:对指定 URL 前缀做独立 IP 频率限制(防暴力破解登录/API)
	URLRateRules []URLRateRule `json:"url_rate_rules"`
	// 目录扫描防御:窗口内不同 URI 数量超过阈值自动封禁
	DirscanEnable    bool `json:"dirscan_enable"`
	DirscanThreshold int  `json:"dirscan_threshold"` // 窗口内不同 URI 数量阈值
	DirscanWindow    int  `json:"dirscan_window"`    // 窗口秒数
	// HTTP 方法白名单:仅允许列表内方法(比黑名单更安全)
	MethodWlEnable bool     `json:"method_whitelist_enable"`
	MethodWlList   []string `json:"method_whitelist"`
}

// DefaultRuleGroups 默认规则组(全部启用)
func DefaultRuleGroups() map[string]bool {
	return map[string]bool{"sql": true, "xss": true, "lfi": true, "cmd": true, "scan": true, "webshell": true}
}

// URLRateRule 敏感路径限速规则
type URLRateRule struct {
	Path   string `json:"path"`   // URL 前缀,如 /login
	Limit  int    `json:"limit"`  // 窗口内最大请求次数
	Window int    `json:"window"` // 窗口秒数
}

// CustomRule 用户自定义检测规则
type CustomRule struct {
	Pattern string `json:"pattern"` // PCRE 正则(对 URI/参数/UA/Referer/POST 体扫描)
	Label   string `json:"label"`   // 命中后日志显示的标签(可选)
}

type wafConfigFile struct {
	WafSettings
	Rules map[string][]string `json:"rules"`
}

// defaultRules 内置检测规则(PCRE,大小写不敏感)
func defaultRules() map[string][]string {
	return map[string][]string{
		"sql": {
			`union\s+(all\s+)?select`,
			`select\s+[\w*,\s]+\s+from`,
			`insert\s+into`,
			`drop\s+(table|database)`,
			`update\s+\w+\s+set\s`,
			`delete\s+from`,
			`('|%27)\s*(or|and)\s*['\d=]`,
			`(or|and)\s+\d+\s*=\s*\d+`,
			`information_schema`,
			`/\*!`,                    // MySQL 内联注释(/*!50000union*/ 类绕过)
			`\(\s*select`,             // 子查询注入
			`(or|and)\s+(substring|ascii|ord|mid|left|right|hex|if|elt)\s*\(`, // 盲注提取函数
			`sleep\s*\(`,
			`waitfor\s+delay`,
			`benchmark\s*\(`,
			`load_file\s*\(`,
			`(char|chr|hex|unhex|conv)\s*\(`,
			`into\s+(out|dump)file`,
		},
		"webshell": {
			`eval\s*\(\s*\$(_POST|_GET|_REQUEST|GLOBALS)`,
			`assert\s*\(\s*\$(_POST|_GET|_REQUEST)`,
			`(include|require)(\s|\()?\s*\$(_POST|_GET|_REQUEST)`,
			`base64_decode\s*\(\s*(gzinflate|str_rot13|eval|\$)`,
			`preg_replace\s*\(\s*["'].*\/e`,
			`(system|shell_exec|passthru|proc_open|popen)\s*\(`,
			`\$\{(?=.*JQR)`,
			`\b(gzuncompress|str_rot13|array_map|call_user_func|array_filter)\s*\(\s*(base64_decode|\$_(GET|POST|REQUEST|COOKIE))`,
			`<\?php[\s\S]{0,200}(eval|assert|system)\s*\(`,
			`request\.getParameter.*?(Runtime|exec|getRuntime)`,
			`ProcessBuilder|java\.lang\.Runtime`,
			`(Invoke-Expression|IEX\s+\(|DownloadString)`,
		},
		"xss": {
			`<\s*script`,
			`</\s*script`,
			`javascript\s*:`,
			`on(error|load|click|mouse\w*|focus|blur)\s*=`,
			`<\s*(iframe|embed|object|svg\s+on\w+)`,
			`document\s*\.\s*cookie`,
			`alert\s*\(`,
			`eval\s*\(`,
			`expression\s*\(`,
			`String\.fromCharCode`,
		},
		"lfi": {
			`(\.\./){2,}`,
			`/etc/(passwd|shadow|hosts)`,
			`/proc/self`,
			`boot\.ini`,
			`win\.ini`,
			`php://(input|filter|data)`,
			`data://text`,
			`%2e%2e%2f(%2e%2e%2f)+`,
		},
		"cmd": {
			`;\s*(cat|ls|id|whoami|uname|wget|curl|nc|bash|sh)\s`,
			`\|\s*(nc|bash|sh|cat|id|whoami)\s`,
			`&&\s*(cat|id|whoami|ls)\s`,
			`\$\((cat|id|whoami|uname)\)`,
			`\bwget\s+https?://`,
			`/bin/(ba)?sh\s`,
		},
		"scan": {
			`(sqlmap|nikto|nmap|nessus|acunetix|awvs|havij|dirbuster|dirb|wpscan|whatweb|masscan|zgrab|nuclei)`,
			`(sqlmap|nikto)/[\d.]+`,
		},
		"sensitive": {
			`(^|/|\.)(git|svn|hg)(/|\?|$)`,
			`(^|/)\.env($|\?|\.)`,
			`\.(bak|sql|swp|save|orig|old|dist)($|\?)`,
			`(phpinfo|adminer\.php)`,
			`(^|/)wp-config(\.php)?$`,
			`(^|/)(composer\.(json|lock)|package-lock\.json)($|\?)`,
			`(^|/)\.DS_Store($|\?)`,
		},
	}
}

func defaultWafConfig() *wafConfigFile {
	return &wafConfigFile{
		WafSettings: WafSettings{
			Enabled:           true,
			Mode:              "block",
			CcEnable:          true,
			CcLimit:           120,
			CcInterval:        10,
			// 蜘蛛白名单默认关闭:伪造 Googlebot UA 可豁免 CC;确需开启请在
			// 设置里同时打开「反向 DNS 验证」(规则扫描与封禁不受白名单豁免)
			SpiderWhiteEnable: false,
			SpiderWhite:       []string{"(Googlebot|Baiduspider|bingbot|360Spider|Sogou|YandexBot|Bytespider|DuckDuckBot)"},
			MethodBlockEnable: true,
			MethodBlockList:   []string{"TRACE", "TRACK", "CONNECT"},
			RuleGroups:        DefaultRuleGroups(),
		},
		Rules: defaultRules(),
	}
}

func loadWafConfig() *wafConfigFile {
	b, err := os.ReadFile(wafSettingsPath())
	if err == nil {
		var c wafConfigFile
		if json.Unmarshal(b, &c) == nil {
			if c.Rules == nil {
				c.Rules = defaultRules()
			}
			// 旧版规则迁移:移除内联 (?i) 标志(外部统一已带大小写不敏感,
			// 内联重复会导致 PCRE 编译冲突、整组规则静默失效 —— 第二轮渗透修复)
			migrated := false
			cleaned := make(map[string][]string, len(c.Rules))
			for g, pats := range c.Rules {
				var np []string
				for _, p := range pats {
					if strings.Contains(p, "(?i)") {
						p = strings.ReplaceAll(p, "(?i)", "")
						migrated = true
					}
					np = append(np, p)
				}
				cleaned[g] = np
			}
			c.Rules = cleaned
			// 新功能迁移:旧配置缺失的规则组/白名单/方法列表补默认值
			// (nil 表示配置里没有该键;显式空数组 [] 保留用户意图不覆盖)
			def := defaultWafConfig()
			if _, ok := c.Rules["sensitive"]; !ok {
				c.Rules["sensitive"] = def.Rules["sensitive"]
				migrated = true
			}
			// 规则迁移(第五轮):webshell 木马检测组,存量配置补默认
			if _, ok := c.Rules["webshell"]; !ok {
				c.Rules["webshell"] = def.Rules["webshell"]
				migrated = true
			}
			// 规则迁移(第三轮渗透修复):sql 组补内联注释/子查询/盲注函数检测,存量配置追加缺失项
			{
				want := []string{`/\*!`, `\(\s*select`, `(or|and)\s+(substring|ascii|ord|mid|left|right|hex|if|elt)\s*\(`}
				have := map[string]bool{}
				for _, p := range c.Rules["sql"] {
					have[p] = true
				}
				for _, p := range want {
					if !have[p] {
						c.Rules["sql"] = append(c.Rules["sql"], p)
						migrated = true
					}
				}
			}
			if c.CcMode == "" {
				// Lua 里空串为真值,'' or "rate" 会返回 '',CC 模式判断全部错位
				c.CcMode = "rate"
				migrated = true
			}
			// 数值缺省迁移:旧配置缺失数值字段时补默认值。
			// 否则 GET 返回 0,POST 回写被校验拒绝,设置页保存永久失败(第四轮修复)
			if c.CcLimit <= 0 {
				c.CcLimit = 120
				migrated = true
			}
			if c.CcInterval <= 0 {
				c.CcInterval = 60
				migrated = true
			}
			if c.IpBanThreshold <= 0 {
				c.IpBanThreshold = 10
				migrated = true
			}
			if c.IpBanDuration <= 0 {
				c.IpBanDuration = 600
				migrated = true
			}
			if c.SpiderWhite == nil {
				c.SpiderWhite = def.SpiderWhite
				c.SpiderWhiteEnable = true
				migrated = true
			}
		if c.MethodBlockList == nil {
			c.MethodBlockList = def.MethodBlockList
			c.MethodBlockEnable = true
			migrated = true
		}
		// 数值缺省迁移(第六轮):目录扫描/自动封禁阈值。
		// lua 端 tonumber(0) 为 0 而非 nil,`or 默认值` 不会触发:
		// threshold=0 会让目录扫描判定恒真(首个 URI 即封)、封禁阈值恒真
		if c.DirscanThreshold <= 0 {
			c.DirscanThreshold = 30
			migrated = true
		}
		if c.DirscanWindow <= 0 {
			c.DirscanWindow = 60
			migrated = true
		}
		if c.AutoBanThreshold <= 0 {
			c.AutoBanThreshold = 50
			migrated = true
		}
		if c.CcClearTTL <= 0 {
			c.CcClearTTL = 3600
			migrated = true
		}
			if migrated {
				_ = saveWafConfig(&c)
			}
			return &c
		}
	}
	return defaultWafConfig()
}

func saveWafConfig(c *wafConfigFile) error {
	// nil 切片归一为空切片:JSON null 会让 Lua 端拿到 truthy userdata
	if c.IpWhite == nil {
		c.IpWhite = []string{}
	}
	if c.IpBlack == nil {
		c.IpBlack = []string{}
	}
	if c.UrlWhite == nil {
		c.UrlWhite = []string{}
	}
	if c.UaBlack == nil {
		c.UaBlack = []string{}
	}
	if c.SpiderWhite == nil {
		c.SpiderWhite = []string{}
	}
	if c.MethodBlockList == nil {
		c.MethodBlockList = []string{}
	}
	if c.CustomRules == nil {
		c.CustomRules = []CustomRule{}
	}
	if c.URLRateRules == nil {
		c.URLRateRules = []URLRateRule{}
	}
	if c.MethodWlList == nil {
		c.MethodWlList = []string{}
	}
	if c.SiteOff == nil {
		c.SiteOff = []string{}
	}
	if c.AutoBlackList == nil {
		c.AutoBlackList = []string{}
	}
	if c.RuleGroups == nil {
		c.RuleGroups = DefaultRuleGroups()
	}
	_ = os.MkdirAll(wafDir(), 0o755)
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(wafSettingsPath(), b, 0o644)
}

// GetWafSettings 读取设置(不含规则)
func GetWafSettings() WafSettings {
	return loadWafConfig().WafSettings
}

var listEntryRe = regexp.MustCompile(`[\r\n\t\x00-\x1f]`)

// methodNameRe 合法 HTTP 方法名(用于黑/白名单条目校验)
var methodNameRe = regexp.MustCompile(`^[A-Z]{3,10}$`)

// nestedQuantRe 拦截嵌套量词(如 (a+)+ ):RE2 可编译但 PCRE 回溯存在风险
var nestedQuantRe = regexp.MustCompile(`[+*]\)[+{*]`)

// validateRuleRegex 单条规则校验:长度/控制字符/正则语法/嵌套量词启发式
func validateRuleRegex(e string) error {
	if len(e) > 200 {
		return fmt.Errorf("单条规则过长(>200 字符)")
	}
	if nestedQuantRe.MatchString(e) {
		return fmt.Errorf("规则含嵌套量词(如 (a+)+),存在回溯风险: %q", e)
	}
	if _, err := regexp.Compile(e); err != nil {
		return fmt.Errorf("规则 %q 不是合法正则: %v", e, err)
	}
	return nil
}

// SaveWafSettings 保存设置(规则保持不变),写入后 Lua 5 秒内自动热加载
// wafSettingsMu 读改写互斥:多个 API 都是"读完整设置→改一个字段→整体写回",
// 并发时后写覆盖前写;调用方在整段序列外层持锁(SaveWafSettings 内部也持锁兜底)
var wafSettingsMu sync.Mutex

// WafLock 返回 WAF 设置读写锁,用于"读-改-写"整体序列的互斥
func WafLock() *sync.Mutex { return &wafSettingsMu }

func SaveWafSettings(s WafSettings) error {
	wafSettingsMu.Lock()
	defer wafSettingsMu.Unlock()
	if s.CcLimit < 5 || s.CcLimit > 100000 {
		return fmt.Errorf("CC 阈值应在 5-100000 之间")
	}
	if s.CcInterval < 1 || s.CcInterval > 3600 {
		return fmt.Errorf("CC 统计窗口应在 1-3600 秒之间")
	}
	switch s.CcMode {
	case "", "rate", "jump", "js", "cf", "slider", "click", "captcha":
	default:
		return fmt.Errorf("未知 CC 防御模式")
	}
	if s.CcMode == "" {
		s.CcMode = "rate"
	}
	if s.CcClearTTL <= 0 || s.CcClearTTL > 86400 {
		s.CcClearTTL = 3600
	}
	if s.CcSecret == "" || len(s.CcSecret) < 32 {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("生成 CC 签名密钥失败")
		}
		s.CcSecret = hex.EncodeToString(b)
	}
	clean := func(list []string, max int) ([]string, error) {
		if len(list) > max {
			return nil, fmt.Errorf("列表最多 %d 条", max)
		}
		out := make([]string, 0, len(list))
		for _, e := range list {
			e = strings.TrimSpace(listEntryRe.ReplaceAllString(e, ""))
			if e == "" {
				continue
			}
			if err := validateRuleRegex(e); err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		return out, nil
	}
	var err error
	if s.IpWhite, err = clean(s.IpWhite, 500); err != nil {
		return fmt.Errorf("IP 白名单: %w", err)
	}
	if s.IpBlack, err = clean(s.IpBlack, 2000); err != nil {
		return fmt.Errorf("IP 黑名单: %w", err)
	}
	if s.UrlWhite, err = clean(s.UrlWhite, 500); err != nil {
		return fmt.Errorf("URL 白名单: %w", err)
	}
	if s.UaBlack, err = clean(s.UaBlack, 500); err != nil {
		return fmt.Errorf("UA 黑名单: %w", err)
	}
	if s.SpiderWhite, err = clean(s.SpiderWhite, 100); err != nil {
		return fmt.Errorf("蜘蛛白名单: %w", err)
	}
	// 危险方法:仅允许大写字母方法名
	methods := make([]string, 0, len(s.MethodBlockList))
	for _, m := range s.MethodBlockList {
		m = strings.ToUpper(strings.TrimSpace(listEntryRe.ReplaceAllString(m, "")))
		if m == "" {
			continue
		}
		if !methodNameRe.MatchString(m) {
			return fmt.Errorf("HTTP 方法不合法: %q", m)
		}
		methods = append(methods, m)
	}
	if len(methods) > 20 {
		return fmt.Errorf("方法列表最多 20 条")
	}
	s.MethodBlockList = methods

	// 方法白名单:同样仅允许大写字母方法名;列表为空时自动关闭开关
	// (lua 端空列表直接放行,开着开关只会造成"已启用却没生效"的误解)
	wl := make([]string, 0, len(s.MethodWlList))
	for _, m := range s.MethodWlList {
		m = strings.ToUpper(strings.TrimSpace(listEntryRe.ReplaceAllString(m, "")))
		if m == "" {
			continue
		}
		if !methodNameRe.MatchString(m) {
			return fmt.Errorf("白名单方法不合法: %q", m)
		}
		wl = append(wl, m)
	}
	if len(wl) > 20 {
		return fmt.Errorf("方法白名单最多 20 条")
	}
	s.MethodWlList = wl
	if len(wl) == 0 {
		s.MethodWlEnable = false
	}

	// 自动封禁阈值:归一到安全区间(0/负值会让封禁判定恒真,全量误封)
	if s.AutoBanThreshold < 1 || s.AutoBanThreshold > 100000 {
		s.AutoBanThreshold = 50
	}
	// 目录扫描防御参数归一(阈值过小会拦截正常浏览,窗口非法会破坏计数桶)
	if s.DirscanThreshold < 2 || s.DirscanThreshold > 100000 {
		s.DirscanThreshold = 30
	}
	if s.DirscanWindow < 10 || s.DirscanWindow > 86400 {
		s.DirscanWindow = 60
	}
	// 敏感路径限速:条数上限 + 参数归一(path 进 lua 前缀匹配,清理控制字符)
	if len(s.URLRateRules) > 50 {
		return fmt.Errorf("敏感路径限速最多 50 条")
	}
	rules := make([]URLRateRule, 0, len(s.URLRateRules))
	for _, r := range s.URLRateRules {
		r.Path = strings.TrimSpace(listEntryRe.ReplaceAllString(r.Path, ""))
		if r.Path == "" {
			continue
		}
		if len(r.Path) > 200 {
			return fmt.Errorf("限速路径过长: %q", r.Path)
		}
		if r.Limit < 1 || r.Limit > 100000 {
			r.Limit = 10
		}
		if r.Window < 1 || r.Window > 86400 {
			r.Window = 60
		}
		rules = append(rules, r)
	}
	s.URLRateRules = rules

	// 蜜罐路径:前缀匹配用,须以 / 开头;空列表自动关闭
	if len(s.HoneypotPaths) > 50 {
		return fmt.Errorf("蜜罐路径最多 50 条")
	}
	paths := make([]string, 0, len(s.HoneypotPaths))
	for _, p := range s.HoneypotPaths {
		p = strings.TrimSpace(listEntryRe.ReplaceAllString(p, ""))
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("蜜罐路径须以 / 开头: %q", p)
		}
		if len(p) > 200 {
			return fmt.Errorf("蜜罐路径过长: %q", p)
		}
		paths = append(paths, p)
	}
	s.HoneypotPaths = paths
	if len(paths) == 0 {
		s.HoneypotEnable = false
	}

	if s.Mode != "block" && s.Mode != "alert" && s.Mode != "monitor" {
		s.Mode = "block"
	}
	c := loadWafConfig()
	c.WafSettings = s
	if c.RuleGroups == nil {
		c.RuleGroups = DefaultRuleGroups()
	}
	if err := saveWafConfig(c); err != nil {
		return err
	}
	// Apache(ModSecurity)防护已启用时,同步最新规则
	ApplyApacheWafRules()
	return nil
}

// ---------- 安装 / 卸载 ----------

// wafOpMu 串行化 WAF 安装/卸载,防止并发交错破坏站点配置
var wafOpMu sync.Mutex

// InstallWaf 安装 WAF 插件:解包 Lua 引擎 → 写 http 级指令 → 重写站点配置 → 重载 OpenResty
func InstallWaf(w io.Writer) error {
	wafOpMu.Lock()
	defer wafOpMu.Unlock()
	gw := gatewayContainer()
	if gw == "" {
		return fmt.Errorf("未检测到运行中的 Nginx / OpenResty,请先在应用商店安装(WAF 依赖 Lua 规则引擎)")
	}
	// 官方 nginx 镜像不含 Lua 引擎:原位升级为面板自编译的 nginx+Lua 镜像,
	// 容器名/端口/站点配置全部保留(不再把 nginx 替换成 OpenResty)
	if gw == "kun-nginx" {
		if err := UpgradeNginxForWaf(w); err != nil {
			return err
		}
	}
	fmt.Fprintln(w, "[1/4] 释放 WAF 引擎文件 ...")
	_ = os.MkdirAll(filepath.Join(wafDir(), "logs"), 0o755)
	// OpenResty worker 以 nobody 运行,日志目录需对其可写
	_ = os.Chmod(filepath.Join(wafDir(), "logs"), 0o777)
	for _, name := range []string{"waf.lua", "kunwaf.conf"} {
		b, err := assets.ReadFile("wafassets/" + name)
		if err != nil {
			return fmt.Errorf("内置资源缺失: %w", err)
		}
		dst := filepath.Join(wafDir(), name)
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	// http 级指令(shared dict)注入 conf.d
	if err := copyAsset("kunwaf.conf", wafConfSnippet()); err != nil {
		return err
	}
	// 默认配置(已存在则保留用户设置)
	if _, err := os.Stat(wafSettingsPath()); os.IsNotExist(err) {
		if err := saveWafConfig(defaultWafConfig()); err != nil {
			return err
		}
	}

	fmt.Fprintln(w, "[2/4] 确保网关容器挂载 WAF 目录 ...")
	mountKey := "openresty"
	if gw == "kun-nginx" {
		mountKey = "nginx"
	}
	if err := appstore.EnsureNginxMounts(mountKey, w); err != nil {
		return err
	}

	fmt.Fprintln(w, "[3/4] 为全部站点注入防护规则 ...")
	if err := websites.RewriteAllConfs(); err != nil {
		return err
	}

	fmt.Fprintln(w, "[4/4] 重载网关 ...")
	if out, err := ReloadGateway(); err != nil {
		return fmt.Errorf("重载失败: %v: %s", err, tail(out))
	} else if strings.TrimSpace(out) != "" {
		fmt.Fprintln(w, out)
	}
	fmt.Fprintln(w, "✅ WAF 防火墙已安装并生效,可在本页面调整防护策略")
	return nil
}

// UninstallWaf 卸载:移除注入 → 重写站点配置 → 重载 → 删除插件数据(含拦截日志)
func UninstallWaf(w io.Writer) error {
	wafOpMu.Lock()
	defer wafOpMu.Unlock()
	fmt.Fprintln(w, "[1/3] 移除 WAF 注入配置 ...")
	_ = os.Remove(wafConfSnippet())
	if err := websites.RewriteAllConfs(); err != nil {
		return err
	}
	fmt.Fprintln(w, "[2/3] 重载网关 ...")
	if gw := gatewayContainer(); gw != "" {
		out, err := ReloadGateway()
		if w != nil && out != "" {
			fmt.Fprintln(w, out)
		}
		if err != nil {
			return fmt.Errorf("重载失败: %v", err)
		}
	}
	fmt.Fprintln(w, "[3/3] 删除插件数据(含拦截日志)...")
	_ = os.RemoveAll(wafDir())
	fmt.Fprintln(w, "✅ WAF 防火墙已卸载")
	return nil
}

// ensureWafMount 老版本 OpenResty 的 compose 可能没有 waf 目录挂载,补上并重建容器
// EnsureNginxMounts 确保面板管理的 nginx/openresty 容器挂载了 WAF 与 SSL 目录,
// 缺失时(旧版本安装的实例)自动补挂载并重建容器

func copyAsset(name, dst string) error {
	b, err := assets.ReadFile("wafassets/" + name)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// ---------- 拦截日志 / 统计 ----------

// WafLogEntry 一条拦截记录(与 waf.lua 输出的 JSON 字段对应)
type WafLogEntry struct {
	Time   string `json:"time"`
	IP     string `json:"ip"`
	Host   string `json:"host"`
	Method string `json:"method"`
	URI    string `json:"uri"`
	UA     string `json:"ua"`
	Rule   string `json:"rule"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// WafLogs 读取最近 n 条拦截日志(倒序)
func WafLogs(n int) ([]WafLogEntry, error) {
	if n <= 0 || n > 5000 {
		n = 200
	}
	lines := tailLines(wafLogPath(), n*2)
	out := make([]WafLogEntry, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		var e WafLogEntry
		if json.Unmarshal([]byte(lines[i]), &e) == nil && e.Rule != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

// ClearWafLogs 清空拦截日志
func ClearWafLogs() error {
	if err := os.Remove(wafLogPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(wafLogPath() + ".old"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// WafStats 拦截统计:总量 / 今日 / 规则 Top / IP Top / 近 24 小时分布
func WafStats() map[string]any {
	entries := readAllLogs()
	type kv struct {
		K string `json:"k"`
		V int    `json:"v"`
	}
	byRule, byIP := map[string]int{}, map[string]int{}
	hours := make([]int, 24) // hours[23] = 最近一小时
	total, today := 0, 0
	now := time.Now()
	todayStr := now.Format("2006-01-02")
	hourAgo24 := now.Add(-23 * time.Hour)
	for _, e := range entries {
		total++
		if strings.HasPrefix(e.Time, todayStr) {
			today++
		}
		byRule[e.Label]++
		byIP[e.IP]++
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", e.Time, time.Local); err == nil && t.After(hourAgo24) {
			idx := 23 - int(now.Sub(t)/time.Hour)
			if idx >= 0 && idx < 24 {
				hours[idx]++
			}
		}
	}
	topN := func(m map[string]int, n int) []kv {
		var l []kv
		for k, v := range m {
			l = append(l, kv{k, v})
		}
		sort.Slice(l, func(i, j int) bool { return l[i].V > l[j].V })
		if len(l) > n {
			l = l[:n]
		}
		return l
	}
	return map[string]any{
		"total":   total,
		"today":   today,
		"by_rule": topN(byRule, 6),
		"by_ip":   topN(byIP, 6),
		"hours":   hours,
	}
}

func readAllLogs() []WafLogEntry {
	var lines []string
	lines = append(lines, tailLines(wafLogPath()+".old", 1<<20)...)
	lines = append(lines, tailLines(wafLogPath(), 1<<20)...)
	var out []WafLogEntry
	for _, l := range lines {
		var e WafLogEntry
		if json.Unmarshal([]byte(l), &e) == nil && e.Rule != "" {
			out = append(out, e)
		}
	}
	return out
}

// tailLines 读文件最后 maxLines 行。只从文件尾部读估算字节量(单行按 600B 上限
// 估算,超出部分自然被行裁剪丢掉),不再把整个日志文件(最大 5MB)读进内存——
// 旧实现每次 WAF 日志请求都全量读文件 + split,高攻击流量下每请求数 MB 分配,
// 是面板卡死的诱因之一。
func tailLines(path string, maxLines int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	readSize := int64(maxLines) * 600
	if readSize > 8<<20 {
		readSize = 8 << 20
	}
	if st.Size() < readSize {
		readSize = st.Size()
	}
	if readSize == 0 {
		return nil
	}
	b := make([]byte, readSize)
	if _, err = f.ReadAt(b, st.Size()-readSize); err != nil {
		return nil
	}
	parts := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if readSize < st.Size() && len(parts) > 0 {
		parts = parts[1:] // 首行大概率被截断,丢弃
	}
	if len(parts) > maxLines {
		parts = parts[len(parts)-maxLines:]
	}
	return parts
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[len(s)-300:]
	}
	return s
}

// ValidateRulePublic 供测试与外部调用的单条规则校验入口
func ValidateRulePublic(pat string) error {
	return validateRuleRegex(pat)
}

// AutoBlacklistRun 自动封禁:扫描最近 24h 拦截日志,违规达阈值的 IP 加入自动黑名单。
// 返回本次新增的 IP 列表。autoBlackList 总量上限 500(超出时保留计数最高的)。
func AutoBlacklistRun(threshold int) (added []string, list []string) {
	if threshold <= 0 {
		threshold = 50
	}
	st := GetWafSettings()
	list = st.AutoBlackList
	if list == nil {
		list = []string{}
	}
	inList := func(ip string) bool {
		for _, x := range list {
			if x == ip {
				return true
			}
		}
		return false
	}
	entries, err := WafLogs(2000)
	if err != nil {
		return nil, list
	}
	cut := time.Now().Add(-24 * time.Hour)
	ipCnt := map[string]int{}
	for _, e := range entries {
		t, e2 := time.Parse("2006-01-02 15:04:05", e.Time)
		if e2 != nil || t.Before(cut) {
			continue
		}
		// 排除内网/环回地址:docker 网关、健康检查等内部流量不可自动拉黑(防误伤)
		if ip := net.ParseIP(e.IP); e.IP == "" || ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			continue
		}
		ipCnt[e.IP]++
	}
	type pair struct {
		ip string
		n  int
	}
	var cands []pair
	for ip, n := range ipCnt {
		if n >= threshold && !inList(ip) {
			cands = append(cands, pair{ip, n})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].n > cands[j].n })
	for _, c := range cands {
		list = append(list, c.ip)
		added = append(added, c.ip)
	}
	// 上限保护:超过 500 时按加入顺序保留前 500
	if len(list) > 500 {
		list = list[:500]
	}
	return added, list
}

// WafDigest 24 小时威胁摘要(供消息中心每日推送)
type WafDigest struct {
	Total    int               `json:"total"`
	ByRule   map[string]int    `json:"by_rule"`
	TopIP    string            `json:"top_ip"`
	TopIPCnt int               `json:"top_ip_cnt"`
	Sites    map[string]int    `json:"sites"`
}

// WafDailyDigest 汇总最近 24 小时的拦截记录
func WafDailyDigest() WafDigest {
	d := WafDigest{ByRule: map[string]int{}, Sites: map[string]int{}}
	entries, err := WafLogs(2000)
	if err != nil {
		return d
	}
	cut := time.Now().Add(-24 * time.Hour)
	ipCnt := map[string]int{}
	for _, e := range entries {
		t, e2 := time.Parse("2006-01-02 15:04:05", e.Time)
		if e2 != nil || t.Before(cut) {
			continue
		}
		d.Total++
		d.ByRule[e.Rule]++
		if e.Host != "" {
			d.Sites[e.Host]++
		}
		ipCnt[e.IP]++
	}
	for ip, n := range ipCnt {
		if n > d.TopIPCnt {
			d.TopIP, d.TopIPCnt = ip, n
		}
	}
	return d
}
