// portfwd.go —— 同机宝塔迁移的反代上游端口转发。
//
// 背景:宝塔反代站点的上游写的是 127.0.0.1:端口,且这些服务往往只监听
// 127.0.0.1。面板 Nginx 跑在容器里,改写成 172.17.0.1:端口 后依然 502:
// 容器访问的是宿主机网桥地址,而服务根本没监听这个地址,防火墙也可能拦。
//
// 方案:面板自身起 TCP 转发器——监听 <网关>:端口,转发到 127.0.0.1:端口,
// 并为该端口放行 docker 网桥来源(iptables)。注册表落盘,面板重启自动恢复。
package btmigrate

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"kunpanel/internal/util"
)

var (
	pfMu       sync.Mutex
	pfListeners = map[int]net.Listener{}
)

func portFwdFile() string { return "/opt/kunpanel/data/btproxy_ports.json" }

func loadProxyPorts() []int {
	b, err := os.ReadFile(portFwdFile())
	if err != nil {
		return nil
	}
	var ports []int
	_ = json.Unmarshal(b, &ports)
	return ports
}

func saveProxyPorts(ports []int) {
	b, _ := json.Marshal(ports)
	_ = util.AtomicWrite(portFwdFile(), b, 0o600)
}

// RegisterProxyForward 注册并启动一个上游端口转发(<网关>:端口 → 127.0.0.1:端口),
// 同时放行 docker 网桥来源的防火墙规则。重复注册幂等。
func RegisterProxyForward(w io.Writer, port int) {
	if port <= 0 || port > 65535 {
		return
	}
	pfMu.Lock()
	if _, running := pfListeners[port]; running {
		pfMu.Unlock()
		return
	}
	pfMu.Unlock()

	gw := bridgeGateway()
	ln, err := net.Listen("tcp", net.JoinHostPort(gw, strconv.Itoa(port)))
	if err != nil {
		// 已有其他进程占用该地址时无需重复转发(服务本身可能已监听全网卡)
		fmt.Fprintf(w, "   端口转发器未启动(%s:%d): %v\n", gw, port, err)
		return
	}
	pfMu.Lock()
	pfListeners[port] = ln
	pfMu.Unlock()
	fmt.Fprintf(w, "   反代上游端口转发已启动: %s:%d → 127.0.0.1:%d\n", gw, port, port)
	go acceptLoop(ln, port)

	// 防火墙放行 docker 网桥来源访问该端口(ufw/iptables 均可能拦)
	if out, err := exec.Command("iptables", "-C", "INPUT", "-p", "tcp", "--dport",
		strconv.Itoa(port), "-s", "172.16.0.0/12", "-j", "ACCEPT").CombinedOutput(); err != nil {
		_ = out
		if out2, err2 := exec.Command("iptables", "-I", "INPUT", "-p", "tcp", "--dport",
			strconv.Itoa(port), "-s", "172.16.0.0/12", "-j", "ACCEPT").CombinedOutput(); err2 != nil {
			fmt.Fprintf(w, "   警告: 防火墙放行失败(%s),容器可能仍无法访问该端口\n", tailStr(string(out2), 100))
		}
	}

	// 登记落盘(重启自动恢复)
	ports := loadProxyPorts()
	for _, p := range ports {
		if p == port {
			return
		}
	}
	ports = append(ports, port)
	saveProxyPorts(ports)
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

// StartProxyForwards 面板启动时恢复全部已登记的反代端口转发
func StartProxyForwards(stop <-chan struct{}) {
	go func() {
		time.Sleep(20 * time.Second) // 等 docker/网关就绪
		select {
		case <-stop:
			return
		default:
		}
		for _, port := range loadProxyPorts() {
			registerQuiet(port)
		}
	}()
}

func registerQuiet(port int) {
	pfMu.Lock()
	if _, running := pfListeners[port]; running {
		pfMu.Unlock()
		return
	}
	pfMu.Unlock()
	gw := bridgeGateway()
	ln, err := net.Listen("tcp", net.JoinHostPort(gw, strconv.Itoa(port)))
	if err != nil {
		return
	}
	pfMu.Lock()
	pfListeners[port] = ln
	pfMu.Unlock()
	go acceptLoop(ln, port)
	_ = exec.Command("iptables", "-C", "INPUT", "-p", "tcp", "--dport",
		strconv.Itoa(port), "-s", "172.16.0.0/12", "-j", "ACCEPT")
}
