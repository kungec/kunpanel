// extras.go —— 宝塔站点自定义配置的保真提取。
//
// 宝塔 vhost 里除了 server_name/root 等基础指令,还经常有:
//   - 自定义 location 块(反代子目录、websocket、拦截规则)
//   - include /www/server/panel/vhost/nginx/proxy/<site>/*.conf(宝塔「反向代理」生成的子目录代理)
//   - 各种 if 判断、error_page、add_header
//
// 此前迁移只取 server_name/root/第一个 proxy_pass/伪静态,以上全部丢弃,
// 导致多 location 站点迁移后功能残缺。本文件把「面板不生成的部分」原样提取,
// 落到站点额外配置(data/websites/extras/<id>.conf),由 writeConf 注入 server 块。
package btmigrate

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

// 面板自己会生成的指令 → 从提取内容里剔除,避免重复/冲突。
// \w+ 通配覆盖 ssl_certificate/session_cache/protocols/stapling 与
// fastcgi_pass/index/param 全族;http3/quic_*/Alt-Svc 是宝塔新构站的
// HTTP3 残留,官方镜像无 quic 模块且 :443 会误导后续端口处理,一并剔除
var (
	reDropDirective = regexp.MustCompile(`(?m)^\s*(listen|server_name|root|index|ssl_\w+|http3|quic_\w+|add_header\s+[Aa]lt-[Ss]vc|access_log|error_log|fastcgi_\w+|proxy_cache_purge)(\s|;).*$`)
	// 宝塔标记注释与内部 include(#SSL-START/#SSL-STOP/#REWRITE-END 等)
	reBtMarkers = regexp.MustCompile(`(?m)^\s*#.*-(START|STOP|END).*$`)
	// enable-php include:PHP 由面板 fastcgi 直连宝塔 php-fpm,不能重复
	reIncludePHP = regexp.MustCompile(`(?m)^\s*include\s+enable-php[^;]*;.*$`)
	// 伪静态 include:单独迁移到面板 rewrite
	reIncludeRewrite = regexp.MustCompile(`(?m)^\s*include\s+/www/server/panel/vhost/rewrite/[^;]*;.*$`)

	// 宝塔 well-known include:非通配、指向容器内不存在的文件,致命错误,剔除
	reIncludeWellKnown = regexp.MustCompile(`(?m)^\s*include\s+[^;]*vhost/nginx/well-known/[^;]*;.*$`)
	// 宝塔专有第三方模块指令(proxy_cache_purge):官方 nginx 镜像无此模块,保留必炸
	reBtModuleDirective = regexp.MustCompile(`(?m)^\s*proxy_cache_purge\s.*$`)
	// 面板已内置 ACME 挑战 location(^~ 优先),宝塔的同类块剔除避免重复
	reAcmeLoc = regexp.MustCompile(`(?ms)^\s*location\s+[^\n]*acme-challenge[^\n]*\{[^}]*\}\s*$`)
	// 宝塔 vhost include(反代子目录 proxy/、well-known 等):解析时内联文件内容,
	// 容器内读不到宝塔路径——凡 /www/server/panel 的 include 原样保留都会成为
	// nginx -t 致命错误(include 不存在的文件),必须全部处理
	reIncludeProxyDir = regexp.MustCompile(`(?m)^(\s*)include\s+(/www/server/panel/vhost/nginx/[^;]+);.*$`)
	// 回环地址:面板 nginx 跑在容器里,127.0.0.1/localhost 是容器自己,必须改成宿主机网关
	reLoopback = regexp.MustCompile(`(https?://)(127\.0\.0\.1|\[::1\]|localhost)([:\s;/]|$)`)
	// 回环上游端口:只认 proxy_pass/grpc_pass 指向回环地址的端口。此前对整段
	// 文本扫 :数字,把 Alt-Svc 头里的 :443 也当成上游端口,转发器注册 443
	// 重启后抢占网关端口导致全站宕机
	reLoopbackUpstreamPort = regexp.MustCompile(`(?i)(?:proxy_pass|grpc_pass)\s+(?:https?|grpc)://(?:127\.0\.0\.1|\[::1\]|localhost):(\d{1,5})`)
)

// extractExtras 从宝塔 vhost conf 提取保真额外配置(空串=没有需要保留的)。
// siteType 用于区分:php 站点的宝塔 fastcgi location 整块剔除(面板自己生成)
func extractExtras(confPath, siteType string) (string, int, []int, error) {
	return extractExtrasAt(confPath, siteType, "", "")
}

// extractExtrasRemote 远程元数据包内的提取:宝塔 include 路径重映射到元数据包,
// 回环上游(远程机的 127.0.0.1)改指远程机地址(DNS 切换前继续回源旧机)
func extractExtrasRemote(confPath, siteType, metaRoot, remoteHost string) (string, int, []int, error) {
	return extractExtrasAt(confPath, siteType, metaRoot, remoteHost)
}

// extractExtrasAt 提取核心:rootRemap 非空时,宝塔 include 的绝对路径前缀重映射
// (远程元数据解压根);loopbackHost 非空时,回环地址改写为该主机而非本地网关
func extractExtrasAt(confPath, siteType, rootRemap, loopbackHost string) (string, int, []int, error) {
	b, err := os.ReadFile(confPath)
	if err != nil {
		return "", 0, nil, err
	}
	body := firstServerBlock(string(b))
	if strings.TrimSpace(body) == "" {
		return "", 0, nil, nil
	}

	// 反代子目录 include → 内联文件内容(逐个文件再过滤一遍回环地址)
	body = reIncludeProxyDir.ReplaceAllStringFunc(body, func(m0 string) string {
		m := reIncludeProxyDir.FindStringSubmatch(m0)
		if m == nil {
			return m0
		}
		pattern := strings.TrimSpace(m[2])
		if rootRemap != "" {
			pattern = filepath.Join(rootRemap, strings.TrimPrefix(pattern, "/"))
		}
		files, _ := filepath.Glob(pattern)
		if len(files) == 0 {
			// 通配可能带具体 conf 路径;仍不存在(如 well-known 挑战目录)则整体剔除
			if st, err := os.Stat(pattern); err == nil && !st.IsDir() {
				files = []string{pattern}
			} else {
				return ""
			}
		}
		var sb strings.Builder
		for _, f := range files {
			cb, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			sb.WriteString("\n    # ---- 内联自 " + filepath.Base(f) + " (宝塔反向代理) ----\n")
			sb.WriteString(indentBlock(string(cb), "    "))
		}
		return sb.String()
	})

	// 块级剔除:宝塔 SSL 301 跳转 if 块、(php 站点)宝塔 fastcgi location 块、
	// proxy_cache_purge 的 purge location 块(指令行剔除后会留下空壳 location)
	body = dropBlocks(body, func(head, blk string) bool {
		return strings.Contains(head, "$server_port")
	})
	body = dropBlocks(body, func(head, blk string) bool {
		isLoc := strings.HasPrefix(strings.TrimSpace(head), "location")
		return isLoc && strings.Contains(blk, "proxy_cache_purge")
	})
	// 宝塔的 PHP fastcgi location 各类型都剔除:php 站点由面板重新生成 fastcgi,
	// 静态/反代站点的宝塔 enable-php 残壳会拦截 .php 请求(面板侧语义不同)
	_ = siteType
	body = dropBlocks(body, func(head, blk string) bool {
		isLoc := strings.HasPrefix(strings.TrimSpace(head), "location")
		return isLoc && (strings.Contains(blk, "fastcgi_pass") || strings.Contains(head, "\\.php"))
	})

	// 逐行剔除面板会生成的指令与宝塔内部标记
	var lines []string
	for _, ln := range strings.Split(body, "\n") {
		switch {
		case reDropDirective.MatchString(ln):
			continue
		case reBtMarkers.MatchString(ln):
			continue
		case reIncludePHP.MatchString(ln):
			continue
		case reIncludeRewrite.MatchString(ln):
			continue
		case reIncludeWellKnown.MatchString(ln):
			continue
		case reBtModuleDirective.MatchString(ln):
			continue
		case reAcmeLoc.MatchString(ln):
			continue
		}
		lines = append(lines, ln)
	}
	extra := strings.Trim(strings.Join(lines, "\n"), "\n")
	// 去掉剔除指令后留下的连续空行
	extra = regexp.MustCompile(`\n{3,}`).ReplaceAllString(extra, "\n\n")
	if strings.TrimSpace(extra) == "" {
		return "", 0, nil, nil
	}
	// 回环上游端口只认 proxy_pass/grpc_pass 的回环地址(改写前的原文),
	// 顺序即配置出现顺序; Alt-Svc/listen 等处的端口号不再混入
	var ports []int
	for _, m := range reLoopbackUpstreamPort.FindAllStringSubmatch(body, -1) {
		pn, e := strconv.Atoi(m[1])
		if e != nil || pn <= 0 || pn > 65535 {
			continue
		}
		dup := false
		for _, have := range ports {
			if have == pn {
				dup = true
				break
			}
		}
		if !dup {
			ports = append(ports, pn)
		}
	}
	// 回环地址改写:同机迁移 → 宿主机网关;远程迁移 → 远程机地址(DNS 切换前回源旧机)
	if loopbackHost != "" {
		extra = reRemoteLoopback.ReplaceAllString(extra, "${1}"+loopbackHost+"${3}")
	} else {
		extra = rewriteLoopback(extra)
	}
	locs := len(regexp.MustCompile(`(?m)^\s*location\s`).FindAllString(extra, -1))
	return extra + "\n", locs, ports, nil
}

// dropBlocks 删除文本中匹配谓词的花括号块(head=块首行,blk=完整块文本)
func dropBlocks(text string, match func(head, blk string) bool) string {
	lines := strings.Split(text, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		trimmed := strings.TrimSpace(ln)
		if (strings.HasPrefix(trimmed, "location") || strings.HasPrefix(trimmed, "if (")) && strings.Contains(ln, "{") {
			// 收集完整块(嵌套配对)
			depth := strings.Count(ln, "{") - strings.Count(ln, "}")
			block := []string{ln}
			j := i
			for depth > 0 && j+1 < len(lines) {
				j++
				block = append(block, lines[j])
				depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
			}
			blk := strings.Join(block, "\n")
			head := block[0]
			if match(head, blk) {
				i = j
				continue
			}
			out = append(out, block...)
			i = j
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// firstServerBlock 截取第一个 server { ... } 块体(含花括号配对)
func firstServerBlock(content string) string {
	start := strings.Index(content, "server")
	for start >= 0 {
		// 必须是 server 指令(而非 server_name);宝塔原生格式 server 与 {
		// 分两行写,必须连换行一起跳过(此前只跳空格制表符,一个站都解析不出)
		rest := content[start+len("server"):]
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		if strings.HasPrefix(trimmed, "{") {
			brace := strings.Index(trimmed, "{")
			depth := 0
			for i := brace; i < len(trimmed); i++ {
				switch trimmed[i] {
				case '{':
					depth++
				case '}':
					depth--
					if depth == 0 {
						return trimmed[brace+1 : i]
					}
				}
			}
			// 花括号不配对(异常文件):取到文件尾
			return trimmed[brace+1:]
		}
		next := strings.Index(rest, "server")
		if next < 0 {
			return ""
		}
		start = start + len("server") + next
	}
	return ""
}

// indentBlock 整体缩进(内联文件对齐 server 块层级)
func indentBlock(s, pad string) string {
	lines := strings.Split(strings.Trim(s, "\n"), "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) != "" {
			lines[i] = pad + ln
		}
	}
	return strings.Join(lines, "\n")
}

// rewriteLoopback 把 proxy_pass/fastcgi_pass 里的回环地址改写为宿主机网关
func rewriteLoopback(s string) string {
	gw := bridgeGateway()
	return reLoopback.ReplaceAllString(s, "${1}"+gw+"${3}")
}

// loopbackUpstreamPort 判断单个反代目标是否回环地址,是则返回其端口
func loopbackUpstreamPort(target string) (int, bool) {
	m := reLoopbackUpstreamPort.FindStringSubmatch("proxy_pass " + strings.TrimSpace(target) + ";")
	if m == nil {
		return 0, false
	}
	pn, err := strconv.Atoi(m[1])
	if err != nil || pn <= 0 || pn > 65535 {
		return 0, false
	}
	return pn, true
}

// bridgeGateway docker0 网关(优先动态探测,失败回退 172.17.0.1)
func bridgeGateway() string {
	if out, err := util.RunWithTimeout(8*time.Second, "docker", "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}"); err == nil {
		if g := strings.TrimSpace(out); g != "" && !strings.Contains(g, " ") {
			return g
		}
	}
	return "172.17.0.1"
}

// saveExtras 落站点额外配置(空内容清除)
func saveExtras(siteID, extra string) error {
	return websites.SetSiteRawExtra(siteID, extra)
}
