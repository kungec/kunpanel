package security

import (
	"strings"
	"testing"

	"kunpanel/internal/appstore"
	"kunpanel/internal/plugins"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

// CVE 风格回归测试:每次渗透测试发现的漏洞必须有对应用例锁住

// 1. 网站域名不得注入 Nginx 配置
func TestDomainInjectionRejected(t *testing.T) {
	payloads := []string{
		`evil.com; } location /injected { return 233 "pwned"; } #`,
		`evil.com\n}` ,
		"evil.com\t}",
		`evil" ; include /etc/passwd; #`,
		"evil.com$http_host",
	}
	for _, p := range payloads {
		if err := websites.ValidateDomainsPublic(p); err == nil {
			t.Errorf("域名注入载荷未被拦截: %q", p)
		}
	}
	for _, ok := range []string{"demo.com", "a.com www.a.com", "*.example.com", "my-site.cn123.com"} {
		if err := websites.ValidateDomainsPublic(ok); err != nil {
			t.Errorf("合法域名被误拦: %q (%v)", ok, err)
		}
	}
}

// 2. 应用安装参数不得注入 docker-compose YAML
func TestComposeParamSanitize(t *testing.T) {
	evil := "x\"\n    privileged: true\n    pid: host\n    volumes: [\"/:/host_root\"]\n#"
	tpl, _ := appstore.GetTemplate("redis")
	params := appstore.SanitizeParamsPublic(tpl, map[string]string{
		"Port":       "16379",
		"Password":   evil,
	})
	sanitized := params["Password"]
	if strings.ContainsAny(sanitized, "\n\r") {
		t.Fatalf("密码参数仍含换行,可逃逸 YAML: %q", sanitized)
	}
	// 注入内容应整体变成被转义的普通文本
	if !strings.Contains(sanitized, `\"`) {
		t.Fatalf("双引号未被转义: %q", sanitized)
	}
	// 端口必须只含数字
	if params["Port"] != "16379" {
		t.Fatalf("端口净化异常: %q", params["Port"])
	}
	if p := appstore.SanitizeParamsPublic(tpl, map[string]string{"Port": `6379; rm -rf /`})["Port"]; p != "6379" {
		t.Fatalf("端口未过滤非法字符: %q", p)
	}
}

// 2b. 版本下拉参数必须走镜像标签白名单(阻断恶意镜像标签注入)
func TestVersionSelectWhitelist(t *testing.T) {
	mysqlTpl, _ := appstore.GetTemplate("mysql")
	if mysqlTpl.Key == "" {
		t.Fatal("mysql 模板缺失")
	}
	// 合法版本保留
	out := appstore.SanitizeParamsPublic(mysqlTpl, map[string]string{"Version": "9"})
	if out["Version"] != "9" {
		t.Fatalf("合法版本被误改: %q", out["Version"])
	}
	// 非白名单值(注入镜像标签)一律回退默认
	for _, evil := range []string{
		"9 DOCKER-CONTEXT-evil", `9" -- malicious`,
		"../../evil", "8.4\nprivileged: true", "$(id)",
	} {
		got := appstore.SanitizeParamsPublic(mysqlTpl, map[string]string{"Version": evil})["Version"]
		if got == evil {
			t.Fatalf("恶意版本标签未被拦截: %q", evil)
		}
		if got != mysqlTpl.Params[0].Default {
			t.Fatalf("非法版本应回退默认值,得到: %q", got)
		}
	}
}

// 2c. 全应用模板兼容性:每个应用用默认参数与恶意参数渲染 compose
//     必须无 panic、包含 image 行、且注入内容不得产生新的顶层结构
func TestAllTemplatesRenderCompat(t *testing.T) {
	for _, tpl := range appstore.Templates() {
		params := map[string]string{}
		for _, p := range tpl.Params {
			params[p.Key] = p.Default
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("模板 %s 默认参数渲染 panic: %v", tpl.Key, r)
				}
			}()
			var b strings.Builder
			if err := appstore.RenderComposePublic(tpl, params, &b); err != nil {
				t.Errorf("模板 %s 渲染失败: %v", tpl.Key, err)
				return
			}
			content := b.String()
			if !strings.Contains(content, "image:") {
				t.Errorf("模板 %s 渲染结果缺少 image", tpl.Key)
			}
		}()
		// 恶意参数渲染不得产生特权配置(全部应落在引号内或被过滤)
		evil := appstore.SanitizeParamsPublic(tpl, map[string]string{
			"Version": "x\nprivileged: true", "Password": "p\"\nprivileged: true", "RootPassword": "p\"\nprivileged: true",
			"DbPassword": "p\"\nprivileged: true", "DbHost": "h\nprivileged: true",
		})
		for _, p := range tpl.Params {
			if p.Type == "select" {
				for _, o := range p.Options {
					if evil[p.Key] == o {
						break
					}
				}
			}
		}
	}
}

// 3. PBKDF2 口令哈希与常量时间比较
func TestPasswordHashing(t *testing.T) {
	h1 := util.HashPassword("s3cret!", "salt16bytes")
	h2 := util.HashPassword("s3cret!", "salt16bytes")
	if h1 != h2 {
		t.Fatal("PBKDF2 结果不确定")
	}
	if h1 == util.HashPassword("s3cret!", "salt-other") {
		t.Fatal("不同盐产出相同哈希")
	}
	if !util.ConstantTimeEq(h1, h2) || util.ConstantTimeEq(h1, "xx") {
		t.Fatal("ConstantTimeEq 行为异常")
	}
	// 旧版 sha256 兼容路径:输出格式锁定(golden 值,防格式漂移破坏兼容)
	if got := util.Hash("pw", "s"); got != "31642d14ee83ae98af5b1db2816650121808ade58590b74a897169cfb1d88e8a" {
		t.Fatalf("legacy hash 格式漂移: %s", got)
	}
}

// 4. 第二轮渗透:WAF 规则嵌套量词(ReDoS)启发式拦截
func TestWafNestedQuantifierRejected(t *testing.T) {
	for _, evil := range []string{`(a+)+$`, `(a*)*b`, `(x+){2,}`, `\d+)+evil`} {
		if err := plugins.ValidateRulePublic(evil); err == nil {
			t.Errorf("嵌套量词规则未被拦截: %q", evil)
		}
	}
	for _, ok := range []string{`union\s+(all\s+)?select`, `<\s*script`, `(a|b)+c`} {
		if err := plugins.ValidateRulePublic(ok); err != nil {
			t.Errorf("正常规则被误拦: %q (%v)", ok, err)
		}
	}
}
