// poolsec.go —— 宝塔 php-fpm kunpanel 进程池的访问控制。
//
// 风险背景:pool 监听 172.17.0.1:907x(宿主机 docker 网桥地址),
// 不加限制时本机任意容器都能直连 FastCGI,以 www 身份执行任意 PHP。
// 双层防护:
//  1. php-fpm listen.allowed_clients = 面板 Nginx 容器 IP(FastCGI 层白名单)
//  2. iptables KUNFPM 链:9071-9099 端口仅放行面板 Nginx 容器 IP(网络层兜底)
//
// 容器重建后 IP 可能变化:面板启动、迁移、切换流量时都会调 EnsurePoolSecurity 刷新。
package btmigrate

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kunpanel/internal/util"
)

// kunNginxIP 面板 Nginx 容器的 docker IP(php-fpm 看到的来源地址)
func kunNginxIP() string {
	for _, c := range []string{"kun-nginx", "kun-openresty"} {
		out, err := util.RunWithTimeout(6*time.Second, "docker", "inspect", c,
			"--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
		if err == nil {
			if ip := strings.TrimSpace(out); ip != "" {
				return ip
			}
		}
	}
	return ""
}

// EnsurePoolSecurity 刷新 kunpanel pool 的 allowed_clients 与 iptables 限源。
// 宝塔环境不存在或尚未创建 pool 时静默跳过。
func EnsurePoolSecurity(w io.Writer) {
	if !BtInstalled() {
		return
	}
	// 找到已写的 kunpanel pool
	var pools []struct {
		ver  string
		path string
	}
	if dirs, err := os.ReadDir(btPHPDir); err == nil {
		for _, d := range dirs {
			name := d.Name()
			if !d.IsDir() || len(name) == 0 {
				continue
			}
			valid := true
			for _, r := range name {
				if r < '0' || r > '9' {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			p := filepath.Join(btPHPDir, name, "etc", "php-fpm.d", "kunpanel.conf")
			if _, err := os.Stat(p); err == nil {
				pools = append(pools, struct {
					ver  string
					path string
				}{name, p})
			}
		}
	}
	if len(pools) == 0 {
		return
	}
	ip := kunNginxIP()
	if ip == "" {
		fmt.Fprintln(w, "   警告:未获取到面板 Nginx 容器 IP,跳过 php-fpm 访问控制刷新")
		return
	}
	changed := false
	for _, p := range pools {
		b, err := os.ReadFile(p.path)
		if err != nil {
			continue
		}
		content := string(b)
		reLine := regexp.MustCompile(`(?m)^;?\s*listen\.allowed_clients\s*=.*$`)
		var updated string
		if reLine.MatchString(content) {
			updated = reLine.ReplaceAllString(content, "listen.allowed_clients = "+ip)
		} else {
			updated = regexp.MustCompile(`(?m)^(listen\s*=.*$)`).ReplaceAllString(content, "$1\nlisten.allowed_clients = "+ip)
		}
		if updated != content {
			if err := util.AtomicWriteString(p.path, updated, 0o644); err == nil {
				changed = true
				fmt.Fprintf(w, "   PHP %s 进程池已限制来源为面板 Nginx 容器(%s)\n", p.ver, ip)
			}
		}
	}
	if changed {
		// 逐版本 reload fpm(存在 systemd 单元才生效,失败静默:USRBIN 兜底)
		for _, p := range pools {
			_ = exec.Command("systemctl", "reload", "php-fpm-"+p.ver).Run()
		}
	}
	ensureFpmIptables(w, ip)
}

// ensureFpmIptables 用独立链限制 9071-9099 仅面板 Nginx 容器可连(网络层兜底)
func ensureFpmIptables(w io.Writer, ip string) {
	if ip == "" {
		return
	}
	const chain = "KUNFPM"
	// 链不存在则建
	if out, err := exec.Command("iptables", "-L", chain, "-n").CombinedOutput(); err != nil || strings.Contains(string(out), "No chain") {
		if out, err := exec.Command("iptables", "-N", chain).CombinedOutput(); err != nil && !strings.Contains(string(out), "already exists") {
			return
		}
		// 挂到 INPUT(仅处理 9071-9099 目标端口)
		_ = exec.Command("iptables", "-I", "INPUT", "-p", "tcp", "--dport", "9071:9099", "-j", chain).Run()
	}
	// 重写链内容:清空 → 放行容器 IP + 回环/网关本机 → 其余拒绝
	_ = exec.Command("iptables", "-F", chain).Run()
	_ = exec.Command("iptables", "-A", chain, "-s", "127.0.0.1", "-j", "ACCEPT").Run()
	_ = exec.Command("iptables", "-A", chain, "-s", bridgeGateway(), "-j", "ACCEPT").Run()
	_ = exec.Command("iptables", "-A", chain, "-s", ip, "-j", "ACCEPT").Run()
	_ = exec.Command("iptables", "-A", chain, "-j", "DROP").Run()
	fmt.Fprintf(w, "   iptables 已限制 9071-9099 仅面板 Nginx 容器(%s)可达\n", ip)
}
