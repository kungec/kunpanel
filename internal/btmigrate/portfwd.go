// portfwd.go —— 同机宝塔迁移的反代上游端口转发。
//
// 背景:宝塔反代站点的上游写的是 127.0.0.1:端口,且这些服务往往只监听
// 127.0.0.1。面板 Nginx 跑在容器里,改写成 172.17.0.1:端口 后依然 502:
// 容器访问的是宿主机网桥地址,而服务根本没监听这个地址,防火墙也可能拦。
//
// 方案:转发器监听 <网桥>:端口 转发到 127.0.0.1:端口;iptables 只对网关
// 容器 IP 放行(不开网段,第三方客户端不可达);无论转发器能否监听(上游
// 可能自己已监听网桥地址)都先放行,最后从网关容器内验证可达,不通即报错。
// 注册表落盘;安装 kun-fwd 独立 systemd 服务,面板升级/重启期间转发不中断。
package btmigrate

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

var (
	pfMu        sync.Mutex
	pfListeners = map[int]net.Listener{}
	pfRules     = map[int]string{} // port -> 已放行的容器 IP(容器重建换 IP 时撤旧规则)
)

func portFwdFile() string { return filepath.Join(util.DataDir(), "btproxy_ports.json") }

func loadProxyPorts() []int {
	b, err := os.ReadFile(portFwdFile())
	if err != nil {
		return nil
	}
	var ports []int
	_ = json.Unmarshal(b, &ports)
	return ports
}

func pfRemember(port int) {
	ports := loadProxyPorts()
	for _, p := range ports {
		if p == port {
			return
		}
	}
	ports = append(ports, port)
	b, _ := json.Marshal(ports)
	if err := util.AtomicWrite(portFwdFile(), b, 0o600); err != nil {
		fmt.Fprintf(io.Discard, "btproxy_ports 落盘失败: %v\n", err)
	}
}

// panelPort 面板自身监听端口(未初始化/测试环境返回 0)
func panelPort() (port int) {
	defer func() { _ = recover() }()
	if config.M == nil {
		return 0
	}
	return config.M.Get().Port
}

// protectedPortReason 转发器拒绝承接的端口:80/443 是网关端口、面板端口是
// 管理入口——一旦被转发器(或独立服务)在重启后抢占,全站/面板直接宕机。
func protectedPortReason(port int) string {
	switch port {
	case 80, 443:
		return "80/443 是网关端口"
	}
	if port > 0 && port == panelPort() {
		return "面板自身端口"
	}
	return ""
}

// RegisterProxyForward 注册反代上游端口转发(gw:port → 127.0.0.1:port)。
// 无论转发器能否监听(上游可能已自行监听网桥地址)都先放行防火墙,最后
// 从网关容器内验证可达;不可达返回错误,调用方应中止该站点的迁移。
func RegisterProxyForward(w io.Writer, port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("非法端口 %d", port)
	}
	if reason := protectedPortReason(port); reason != "" {
		return fmt.Errorf("拒绝转发端口 %d(%s)", port, reason)
	}
	gw := bridgeGateway()

	// 1) 防火墙放行(只对网关容器 IP):Listen 成败都必须做,否则 ufw
	//    默认拒绝入站时,容器访问宿主机端口一律超时 → 全站 502
	ensureForwardFirewall(w, port)
	pfRemember(port)

	// 2) 启动转发器;Listen 失败只说明该地址已被占用(最常见:上游自己
	//    监听 0.0.0.0),容器流量可直达,不算错误——可达性由第 3 步裁决
	pfMu.Lock()
	_, running := pfListeners[port]
	pfMu.Unlock()
	if !running {
		ln, err := net.Listen("tcp", net.JoinHostPort(gw, strconv.Itoa(port)))
		if err != nil {
			fmt.Fprintf(w, "   端口转发器未启动(%s:%d):%v(上游可能已自行监听该地址,无需转发)\n", gw, port, err)
		} else {
			pfMu.Lock()
			pfListeners[port] = ln
			pfMu.Unlock()
			fmt.Fprintf(w, "   反代上游端口转发已启动: %s:%d → 127.0.0.1:%d\n", gw, port, port)
			go acceptLoop(ln, port)
		}
	}
	registerKunFwdUnit(w)

	// 3) 网关容器内验证:放行是否生效、上游是否真的在听,以容器视角为准
	if err := verifyUpstream(w, gw, port); err != nil {
		return err
	}
	return nil
}

// verifyUpstream 从网关容器内探测 gw:port(curl 任意 HTTP 响应即算通,
// 退 bash /dev/tcp 纯 TCP、再退 wget)。无容器网关(宿主机 nginx)时本机拨号。
// 脚本以 [ -n "$KUNFWD_OK" ] 收尾:退出码 0=可达,避免中间命令的退出码误判。
func verifyUpstream(w io.Writer, gw string, port int) error {
	addr := net.JoinHostPort(gw, strconv.Itoa(port))
	mode := websites.DetectNginx()
	if mode.Mode == "container" && mode.Target != "" {
		probe := fmt.Sprintf(
			`KUNFWD_OK=; curl -s -o /dev/null -m 4 "http://%s/" && KUNFWD_OK=1; `+
				`[ -n "$KUNFWD_OK" ] || { timeout 4 bash -c "exec 3<>/dev/tcp/%s/%d" 2>/dev/null && KUNFWD_OK=1; }; `+
				`[ -n "$KUNFWD_OK" ] || { wget -q -T 4 -O /dev/null "http://%s/" 2>/dev/null && KUNFWD_OK=1; }; `+
				`[ -n "$KUNFWD_OK" ]`,
			addr, gw, port, addr)
		out, err := dockerx.ExecIn(mode.Target, "sh", "-c", probe)
		if err != nil {
			return fmt.Errorf("上游 %s 在网关容器 %s 内不可达(防火墙放行/上游监听地址需检查)%s",
				addr, mode.Target, tailStr(strings.TrimSpace(out), 120))
		}
		fmt.Fprintf(w, "   上游可达性已验证(容器内 %s)\n", addr)
		return nil
	}
	conn, err := net.DialTimeout("tcp", addr, 4*time.Second)
	if err != nil {
		return fmt.Errorf("上游 %s 不可达: %v", addr, err)
	}
	conn.Close()
	fmt.Fprintf(w, "   上游可达性已验证(本机 %s)\n", addr)
	return nil
}

// gatewayContainerIP 当前网关容器的 docker IP(放行规则的目标来源)
func gatewayContainerIP() string {
	mode := websites.DetectNginx()
	if mode.Mode == "container" && mode.Target != "" {
		if out, err := util.RunWithTimeout(6*time.Second, "docker", "inspect", mode.Target,
			"--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}"); err == nil {
			if ip := strings.TrimSpace(out); ip != "" {
				return ip
			}
		}
	}
	return kunNginxIP()
}

// ensureForwardFirewall 放行网关容器 IP 访问转发端口(iptables INPUT)。
// 只对容器 IP 放行(此前放行 172.16.0.0/12 整个容器网段,过宽);规则带
// kunfwd 注释;容器重建换 IP 后再次注册/刷新时自动撤旧换新。幂等。
func ensureForwardFirewall(w io.Writer, port int) {
	ip := gatewayContainerIP()
	if ip == "" {
		fmt.Fprintf(w, "   警告: 未获取到网关容器 IP,未放行防火墙(容器访问该端口可能被拦截)\n")
		return
	}
	pfMu.Lock()
	old := pfRules[port]
	pfMu.Unlock()
	if old != "" && old != ip {
		if out, err := exec.Command("iptables", "-D", "INPUT", "-p", "tcp", "--dport",
			strconv.Itoa(port), "-s", old, "-m", "comment", "--comment", "kunfwd", "-j", "ACCEPT").CombinedOutput(); err != nil {
			fmt.Fprintf(w, "   警告: 撤销旧放行规则(%s)失败: %s\n", old, tailStr(string(out), 100))
		}
	}
	rule := func(action string) ([]byte, error) {
		return exec.Command("iptables", action, "INPUT", "-p", "tcp", "--dport",
			strconv.Itoa(port), "-s", ip, "-m", "comment", "--comment", "kunfwd", "-j", "ACCEPT").CombinedOutput()
	}
	if _, err := rule("-C"); err != nil {
		if out, err := rule("-I"); err != nil {
			fmt.Fprintf(w, "   警告: 防火墙放行失败(%s → %d): %s\n", ip, port, tailStr(string(out), 100))
			return
		}
	}
	pfMu.Lock()
	pfRules[port] = ip
	pfMu.Unlock()
}

func acceptLoop(ln net.Listener, port int) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			up, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
			if err != nil {
				return
			}
			defer up.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(up, c); up.SetDeadline(time.Now()); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, up); c.SetDeadline(time.Now()); done <- struct{}{} }()
			<-done
			<-done
		}(conn)
	}
}

// StartProxyForwards 面板启动时恢复全部已登记的反代端口转发。
// kun-fwd 独立服务已在运行时跳过(避免双份转发器抢监听)。
func StartProxyForwards(stop <-chan struct{}) {
	go func() {
		// 立即恢复:转发器监听网桥地址是本机操作,不依赖 docker daemon 就绪;
		// 恢复越早,面板重启窗口内的反代 502 越短
		select {
		case <-stop:
			return
		case <-time.After(2 * time.Second):
		}
		ports := loadProxyPorts()
		if len(ports) == 0 {
			return
		}
		if out, err := exec.Command("systemctl", "is-active", "--quiet", "kun-fwd").CombinedOutput(); err == nil && len(out) == 0 {
			refreshForwardFirewall()
			return
		}
		for _, port := range ports {
			registerQuiet(port)
		}
	}()
}

// refreshForwardFirewall 容器 IP 可能已变化:按注册表把放行规则刷新到当前
// 网关容器 IP(启动时 iptables 规则不持久,也依赖这里重插)。
func refreshForwardFirewall() {
	for _, port := range loadProxyPorts() {
		if reason := protectedPortReason(port); reason != "" {
			continue // 历史误注册的端口(如 443)不恢复、不放行
		}
		ensureForwardFirewall(io.Discard, port)
	}
}

func registerQuiet(port int) {
	if reason := protectedPortReason(port); reason != "" {
		return
	}
	ensureForwardFirewall(io.Discard, port)
	pfMu.Lock()
	if _, running := pfListeners[port]; running {
		pfMu.Unlock()
		return
	}
	pfMu.Unlock()
	gw := bridgeGateway()
	ln, err := net.Listen("tcp", net.JoinHostPort(gw, strconv.Itoa(port)))
	if err != nil {
		return // 上游可能已自行监听该地址;放行规则已由 ensureForwardFirewall 落实
	}
	pfMu.Lock()
	pfListeners[port] = ln
	pfMu.Unlock()
	go acceptLoop(ln, port)
}

// kun-fwd 独立服务:面板停止/升级/重启期间,站点反代上游转发不中断。
const kunFwdUnitFile = "/etc/systemd/system/kun-fwd.service"

// registerKunFwdUnit 安装并启用 kun-fwd(幂等:内容不变不重写)。
// 安装失败不阻塞迁移——面板内转发器仍在工作,只记警告。
func registerKunFwdUnit(w io.Writer) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(w, "   警告: 无法定位面板可执行文件,独立转发服务未安装: %v\n", err)
		return
	}
	content := fmt.Sprintf(`[Unit]
Description=KunPanel BT-migrate upstream forwarder
After=network.target docker.service

[Service]
ExecStart=%s fwd
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
`, exe)
	if b, rerr := os.ReadFile(kunFwdUnitFile); rerr != nil || string(b) != content {
		if werr := util.AtomicWriteString(kunFwdUnitFile, content, 0o644); werr != nil {
			fmt.Fprintf(w, "   警告: 写入 kun-fwd 服务单元失败(systemd 沙箱需 ReadWritePaths 含 /etc/systemd/system): %v\n", werr)
			return
		}
		if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			fmt.Fprintf(w, "   警告: systemctl daemon-reload 失败: %s\n", tailStr(string(out), 100))
		}
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "kun-fwd").CombinedOutput(); err != nil {
		fmt.Fprintf(w, "   警告: 启用 kun-fwd 服务失败(面板内转发器仍工作): %s\n", tailStr(string(out), 100))
	}
}

// RunForwardDaemon 独立转发器主循环(kunpanel fwd):每 15s 重读端口清单
// 纳入新增端口(只增不减,避免误断现网流量),直到收到停止信号。
func RunForwardDaemon(stop <-chan struct{}) error {
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		for _, port := range loadProxyPorts() {
			registerQuiet(port)
		}
		select {
		case <-stop:
			return nil
		case <-time.After(15 * time.Second):
		}
	}
}
