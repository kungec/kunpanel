// plugins/wafapache.go —— WAF 的 Apache 支持:在 PHP(Apache)容器内启用
// ModSecurity,将面板 WAF 设置的核心子集(IP 黑白名单 / UA 黑名单 / CC / 敏感文件 /
// 危险方法 / 自定义规则)生成为 ModSecurity 规则,写入 conf-enabled/kunwaf.conf
// (实测该位置规则参与请求处理)。面板站点流量默认先经 Nginx WAF(全量规则),
// 此模块用于保护直连 Apache 的流量,属纵深防护。
package plugins

import (
	"fmt"
	"strings"

	"kunpanel/internal/dockerx"
)

const apacheWafContainer = "kun-php"

// ApacheWafStatus Apache 防护状态(供 API/前端)
func ApacheWafStatus() map[string]any {
	out := map[string]any{
		"available": false, "installed": false, "enabled": false,
	}
	if !dockerx.ContainerRunning(apacheWafContainer) {
		return out
	}
	out["available"] = true
	if _, err := dockerx.ExecIn(apacheWafContainer, "test", "-f", "/etc/apache2/conf-enabled/kunwaf.conf"); err == nil {
		out["installed"] = true
		if out2, e2 := dockerx.ExecIn(apacheWafContainer, "sh", "-c",
			"awk '/SecRuleEngine/{print $2; exit}' /etc/apache2/conf-enabled/kunwaf.conf 2>/dev/null"); e2 == nil {
			out["enabled"] = strings.TrimSpace(out2) == "On"
		}
	}
	return out
}

// apacheWafInstalled 容器内是否已安装 mod_security
func apacheWafInstalled() bool {
	_, err := dockerx.ExecIn(apacheWafContainer, "sh", "-c",
		"test -f /usr/lib/apache2/modules/mod_security2.so")
	return err == nil
}

// secEscape ModSec 参数字符串转义:双引号必须转义;反斜杠保留原样
// (对 "\\.x" 这类输入 ModSec 宽容处理,翻倍反而会破坏用户正则)
func secEscape(s string) string {
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// GenerateApacheWafConf 由 WafSettings 生成 ModSecurity 规则(核心子集)。
// 注意:输出不含 SecRuleEngine(由调用方统一写入)。
func GenerateApacheWafConf(st WafSettings) string {
	var b strings.Builder
	id := 2000
	next := func() int { id++; return id }

	// IP 白名单(优先放行)
	if arg := ipMatchArg(st.IpWhite); arg != "" {
		fmt.Fprintf(&b, "SecRule REMOTE_ADDR \"@ipMatch %s\" \"id:%d,phase:1,allow,nolog\"\n", secEscape(arg), next())
	}
	// CC 防护(per-IP 固定窗口限流:超限拒绝该请求并清零重计,
	// 避免计数随持续请求无限累积导致永久拦截)
	if st.CcEnable {
		fmt.Fprintf(&b, "SecAction \"id:%d,phase:1,initcol:ip=%%{REMOTE_ADDR},pass,nolog\"\n", next())
		fmt.Fprintf(&b, "SecRule IP:KUN_CC \"@ge %d\" \"id:%d,phase:1,deny,status:429,msg:'CC rate limit',setvar:ip.KUN_CC=0\"\n",
			st.CcLimit, next())
		fmt.Fprintf(&b, "SecRule REQUEST_URI \"@beginsWith /\" \"id:%d,phase:1,pass,nolog,setvar:ip.KUN_CC=+1,expirevar:ip.KUN_CC=%d\"\n",
			next(), st.CcInterval)
	}
	// 空 UA
	if st.EmptyUaBlock {
		fmt.Fprintf(&b, "SecRule REQUEST_HEADERS:User-Agent \"@rx ^$\" \"id:%d,phase:1,deny,status:403,msg:'empty UA'\"\n", next())
		fmt.Fprintf(&b, "SecRule &REQUEST_HEADERS:User-Agent \"@eq 0\" \"id:%d,phase:1,deny,status:403,msg:'missing UA'\"\n", next())
	}
	// HTTP/1.0
	if st.Http10Block {
		fmt.Fprintf(&b, "SecRule REQUEST_PROTOCOL \"@streq HTTP/1.0\" \"id:%d,phase:1,deny,status:405,msg:'http 1.0'\"\n", next())
	}
	// UA 黑名单
	if len(st.UaBlack) > 0 {
		var pats []string
		for _, u := range st.UaBlack {
			if u = strings.TrimSpace(u); u != "" {
				pats = append(pats, secEscape(u))
			}
		}
		if len(pats) > 0 {
			fmt.Fprintf(&b, "SecRule REQUEST_HEADERS:User-Agent \"@rx (?i)(%s)\" \"id:%d,phase:1,deny,status:403,msg:'UA blacklisted'\"\n",
				strings.Join(pats, "|"), next())
		}
	}
	// 敏感文件
	fmt.Fprintf(&b, "SecRule REQUEST_URI \"@rx (?i)(\\.git|\\.env$|\\.sql$|\\.bak$|\\.old$|/\\.svn|\\.DS_Store)\" \"id:%d,phase:1,deny,status:403,msg:'sensitive file'\"\n", next())
	// 危险方法
	if st.MethodBlockEnable && len(st.MethodBlockList) > 0 {
		var ms []string
		for _, m := range st.MethodBlockList {
			if m = strings.TrimSpace(m); m != "" {
				ms = append(ms, m)
			}
		}
		if len(ms) > 0 {
			fmt.Fprintf(&b, "SecRule REQUEST_METHOD \"@rx ^(%s)$\" \"id:%d,phase:1,deny,status:405,msg:'blocked method'\"\n",
				strings.Join(ms, "|"), next())
		}
	}
	// 自定义规则(URI/参数/请求体扫描,phase 2)
	for i, cr := range st.CustomRules {
		pat := secEscape(cr.Pattern)
		label := cr.Label
		if label == "" {
			label = "custom rule"
		}
		fmt.Fprintf(&b, "SecRule REQUEST_URI|ARGS|REQUEST_BODY \"@rx %s\" \"id:%d,phase:2,deny,status:403,msg:'%s #%d'\"\n",
			pat, next(), secEscape(label), i+1)
	}
	return b.String()
}

// ipMatchArg 把 IP 列表合并为 @ipMatch 参数
func ipMatchArg(list []string) string {
	var out []string
	for _, e := range list {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		out = append(out, e)
	}
	return strings.Join(out, ",")
}

// ApacheWafToggle 启用/禁用 Apache 防护(安装 mod_security + 生成规则 + 生效)
func ApacheWafToggle(enabled bool) error {
	if !dockerx.ContainerRunning(apacheWafContainer) {
		return fmt.Errorf("PHP 运行时未运行")
	}
	if enabled && !apacheWafInstalled() {
		if _, err := dockerx.ExecIn(apacheWafContainer, "sh", "-c",
			"apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends libapache2-mod-security2"); err != nil {
			return fmt.Errorf("ModSecurity 安装失败: %v", err)
		}
	}
	conf := GenerateApacheWafConf(loadWafConfig().WafSettings)
	return apacheWafWriteRules(conf, enabled)
}

// apacheWafWriteRules 写规则文件并生效;enabled=false 时引擎 Off(规则保留)
func apacheWafWriteRules(conf string, enabled bool) error {
	engine := "On"
	if !enabled {
		engine = "Off"
	}
	content := "# 由坤面板生成 —— Apache ModSecurity 防护\nSecRuleEngine " + engine + "\n" + conf + "\n"
	if err := dockerx.ExecInStdin(apacheWafContainer, []byte(content), "sh", "-c",
		"cat > /etc/apache2/conf-enabled/kunwaf.conf && apache2ctl -t"); err != nil {
		return fmt.Errorf("写入规则失败: %v", err)
	}
	_, _ = dockerx.ExecIn(apacheWafContainer, "apache2ctl", "-k", "graceful")
	return nil
}

// ApplyApacheWafRules WAF 设置保存后同步 Apache 规则(仅在已启用时)
func ApplyApacheWafRules() {
	st := ApacheWafStatus()
	if enabled, ok := st["enabled"].(bool); !ok || !enabled {
		return
	}
	conf := GenerateApacheWafConf(loadWafConfig().WafSettings)
	_ = apacheWafWriteRules(conf, true)
}
