// fwmgmt/dockerfw.go —— 容器端口管控:DOCKER-USER 链管理
//
// 背景:Docker 发布的端口经 iptables nat(FORWARD 链)直达容器,
// 完全绕过 ufw/firewalld(INPUT 链)——面板「系统防火墙」对其无效。
// Docker 官方约定:用户自定义过滤必须放 DOCKER-USER 链(FORWARD 早段,
// Docker 自身不管理该链)。本文件对该链做端口级封禁/解封,并持久化重放。
package fwmgmt

import (
	"regexp"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/util"
)

// PubPort 容器发布的端口
type PubPort struct {
	Container string `json:"container"`
	HostPort  string `json:"host_port"` // "3306"
	Proto     string `json:"proto"`     // tcp / udp
	Blocked   bool   `json:"blocked"`   // 是否已封禁公网
}

// DockerFW 容器端口管控状态
type DockerFW struct {
	Available bool      `json:"available"` // docker 可用且 DOCKER-USER 链存在
	Ports     []PubPort `json:"ports"`
}

var dockerFWMu sync.Mutex

func dockerFWStatePath() string { return config.DataDir() + "/dockerfw.json" }

// extIf 默认路由出口网卡(外部流量入接口),输出需通过接口名校验
func extIf() string {
	out, err := util.Run("sh", "-c", "ip route show default | awk '{print $5; exit}'")
	if err != nil {
		return "eth0"
	}
	ifc := strings.TrimSpace(out)
	if ifcRe.MatchString(ifc) {
		return ifc
	}
	return "eth0"
}

var ifcRe = regexp.MustCompile(`^[a-zA-Z0-9._@-]{1,15}$`)

func iptables(args ...string) (string, error) {
	return util.RunWithTimeout(10*time.Second, "iptables", args...)
}

// dockerAvailable docker 引擎与 DOCKER-USER 链是否可用
func dockerAvailable() bool {
	if _, err := util.RunWithTimeout(5*time.Second, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		return false
	}
	// DOCKER-USER 链由 docker 守护进程创建;确保存在(-N 幂等)
	_, _ = iptables("-N", "DOCKER-USER")
	_, err := iptables("-L", "DOCKER-USER", "-n")
	return err == nil
}

// blockRuleSpec 生成封禁规则匹配参数(外部入接口 + 目标端口)
func blockRuleSpec(proto, hostPort string) []string {
	ifc := extIf()
	args := []string{"-i", ifc}
	if proto != "" {
		args = append(args, "-p", proto)
	}
	return append(args, "--dport", hostPort, "-j", "DROP")
}

// DockerPorts 列出容器发布端口及封禁状态
func DockerPorts() DockerFW {
	dockerFWMu.Lock()
	defer dockerFWMu.Unlock()
	var out DockerFW
	out.Available = dockerAvailable()
	if !out.Available {
		return out
	}
	blocked := dockerBlockedSet()

	outp, err := util.RunWithTimeout(10*time.Second, "docker", "ps",
		"--format", "{{.Names}}\t{{.Ports}}")
	if err != nil {
		out.Available = false
		return out
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(outp), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, portsStr := parts[0], parts[1]
		// 形如 0.0.0.0:3306->3306/tcp, [::]:3306->3306/tcp, 33060/tcp
		for _, seg := range strings.Split(portsStr, ", ") {
			// 只关心 host->container 映射段
			if !strings.Contains(seg, "->") {
				continue
			}
			hostPart := strings.SplitN(seg, "->", 2)[0]      // 0.0.0.0:3306 或 [::]:3306
			hp := hostPart
			if i := strings.LastIndex(hp, ":"); i >= 0 {
				hp = hp[i+1:]
			}
			proto := "tcp"
			if strings.HasSuffix(seg, "/udp") {
				proto = "udp"
			}
			key := hp + "/" + proto
			if seen[key] {
				continue
			}
			seen[key] = true
			out.Ports = append(out.Ports, PubPort{
				Container: name, HostPort: hp, Proto: proto,
				Blocked: blocked[key],
			})
		}
	}
	return out
}

// dockerBlockedSet 当前 DOCKER-USER 中已被 DROP 的端口集合("端口/proto")
func dockerBlockedSet() map[string]bool {
	set := map[string]bool{}
	out, err := iptables("-S", "DOCKER-USER")
	if err != nil {
		return set
	}
	for _, ln := range strings.Split(out, "\n") {
		if !strings.HasPrefix(ln, "-A DOCKER-USER") || !strings.Contains(ln, "--dport") || !strings.Contains(ln, "-j DROP") {
			continue
		}
		f := strings.Fields(ln)
		var port, proto string
		for i, w := range f {
			if w == "--dport" && i+1 < len(f) {
				port = f[i+1]
			}
			if w == "-p" && i+1 < len(f) {
				proto = f[i+1]
			}
		}
		if port != "" {
			set[port+"/"+proto] = true
		}
	}
	return set
}

// DockerBlock 封禁端口的外部访问(回环/内网经由 docker-proxy 不受影响)
func DockerBlock(hostPort, proto string) error {
	dockerFWMu.Lock()
	defer dockerFWMu.Unlock()
	if !dockerAvailable() {
		return fmt.Errorf("Docker 或 DOCKER-USER 链不可用")
	}
	if proto != "tcp" && proto != "udp" {
		proto = "tcp"
	}
	// 幂等:已存在则跳过
	if dockerBlockedSet()[hostPort+"/"+proto] {
		return nil
	}
	spec := blockRuleSpec(proto, hostPort)
	args := append([]string{"-I", "DOCKER-USER", "1"}, spec...)
	if _, err := iptables(args...); err != nil {
		return fmt.Errorf("iptables 失败: %v", err)
	}
	saveDockerFW()
	return nil
}

// DockerUnblock 解封端口的外部访问
func DockerUnblock(hostPort, proto string) error {
	dockerFWMu.Lock()
	defer dockerFWMu.Unlock()
	if !dockerAvailable() {
		return fmt.Errorf("Docker 或 DOCKER-USER 链不可用")
	}
	if proto != "tcp" && proto != "udp" {
		proto = "tcp"
	}
	spec := blockRuleSpec(proto, hostPort)
	args := append([]string{"-D", "DOCKER-USER"}, spec...)
	out, err := iptables(args...)
	if err != nil && !strings.Contains(out, "No such file or directory") {
		// 规则不存在视为已解封
		if !strings.Contains(out, "does not exist") {
			return fmt.Errorf("iptables 失败: %v %s", err, strings.TrimSpace(out))
		}
	}
	saveDockerFW()
	return nil
}

// dockerFWState 持久化的封禁清单(开机重放)
type dockerFWState struct {
	Blocked []struct {
		Port  string `json:"port"`
		Proto string `json:"proto"`
	} `json:"blocked"`
}

func saveDockerFW() {
	state := dockerFWState{}
	for k := range dockerBlockedSet() {
		pp := strings.SplitN(k, "/", 2)
		e := struct {
			Port  string `json:"port"`
			Proto string `json:"proto"`
		}{pp[0], "tcp"}
		if len(pp) == 2 {
			e.Proto = pp[1]
		}
		state.Blocked = append(state.Blocked, e)
	}
	b, _ := json.MarshalIndent(state, "", "  ")
	_ = util.AtomicWrite(dockerFWStatePath(), b, 0o600)
}

func loadDockerFW() dockerFWState {
	var s dockerFWState
	if b, err := os.ReadFile(dockerFWStatePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// DockerReapply 面板启动时重放封禁规则(重启后 iptables 规则丢失)
func DockerReapply() {
	dockerFWMu.Lock()
	defer dockerFWMu.Unlock()
	if !dockerAvailable() {
		return
	}
	for _, e := range loadDockerFW().Blocked {
		if !dockerBlockedSet()[e.Port+"/"+e.Proto] {
			spec := blockRuleSpec(e.Proto, e.Port)
			args := append([]string{"-I", "DOCKER-USER", "1"}, spec...)
			_, _ = iptables(args...)
		}
	}
}

// ---------- ping (ICMP) 开关 ----------

func pingSysctl() string   { return "/etc/sysctl.d/99-kunpanel.conf" }

// PingStatus 当前是否允许 ping(true=允许)
func PingStatus() bool {
	out, err := util.Run("sysctl", "-n", "net.ipv4.icmp_echo_ignore_all")
	if err != nil {
		return true
	}
	return strings.TrimSpace(out) != "1"
}

// SetPing 开/关 ping(sysctl 应用 + /etc/sysctl.d 持久化)。
// 面板 systemd 单元带 ProtectKernelTunables(禁写 /proc/sys)与 ProtectSystem=full
// (禁写 /etc),因此实际执行通过 systemd-run 瞬态单元逃逸沙箱;无 systemd 时回退直连。
func SetPing(on bool) error {
	val := "0"
	if !on {
		val = "1"
	}
	script := "sysctl -w net.ipv4.icmp_echo_ignore_all=" + val +
		"; mkdir -p /etc/sysctl.d" +
		"; printf '%s\n%s\n' '# by kunpanel' 'net.ipv4.icmp_echo_ignore_all = " + val + "' > /etc/sysctl.d/99-kunpanel.conf"
	if _, err := util.Run("systemd-run", "--collect", "--unit",
		fmt.Sprintf("kp-sysctl-%d", time.Now().UnixNano()), "/bin/sh", "-c", script); err != nil {
		// 回退:直连执行(非 systemd 或 systemd-run 不可用)
		if _, e2 := util.Run("sysctl", "-w", "net.ipv4.icmp_echo_ignore_all="+val); e2 != nil {
			return fmt.Errorf("sysctl 失败: %v", e2)
		}
		if err := util.AtomicWriteString(pingSysctl(), "# by kunpanel\nnet.ipv4.icmp_echo_ignore_all = "+val+"\n", 0o644); err != nil {
			return err
		}
	}
	return nil
}
