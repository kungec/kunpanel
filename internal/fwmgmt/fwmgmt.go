// Package fwmgmt 防火墙端口管理:UFW / Firewalld / 通用 iptables 检测与端口 CRUD
package fwmgmt

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/secmgr"
	"kunpanel/internal/util"
)

// Kind 防火墙类型
type Kind string

const (
	UFW       Kind = "ufw"
	FIREWALLD Kind = "firewalld"
	NONE      Kind = "none"
)

// Rule 端口规则
type Rule struct {
	Port     string `json:"port"` // "80" 或 "4000:4010" 或 "80/tcp"
	Proto    string `json:"proto"` // tcp / udp / ""
	Action   string `json:"action"` // allow / deny
	From     string `json:"from"` // 来源 IP(可空)
	Comment  string `json:"comment"`
}

// Status 防火墙整体状态
type Status struct {
	Kind    Kind   `json:"kind"`
	Active  bool   `json:"active"`
	Running bool   `json:"running"` // 服务可用
	Rules   []Rule `json:"rules"`
}

var portRe = regexp.MustCompile(`^(\d{1,5})(:(\d{1,5}))?$`)

func have(bin string) bool {
	if _, err := util.Run("sh", "-c", "command -v "+bin); err == nil {
		return true
	}
	// util.Run 返回非零时 err != nil;command -v 找到返回 0
	_, err := os.Stat("/usr/sbin/" + bin)
	return err == nil
}

// Detect 检测防火墙类型与状态
func Detect() *Status {
	st := &Status{Kind: NONE}
	if have("ufw") {
		st.Kind = UFW
		out, err := util.Run("ufw", "status")
		if err == nil {
			st.Active = strings.Contains(out, "Status: active")
			st.Running = true
			if st.Active {
				st.Rules = parseUFW(out)
			} else {
				// 未启用时 ufw status 不列出规则,用 show added 兜底
				if added, e := util.Run("ufw", "show", "added"); e == nil {
					st.Rules = parseUFWAdded(added)
				}
			}
		}
		return st
	}
	if have("firewall-cmd") {
		st.Kind = FIREWALLD
		out, err := util.Run("firewall-cmd", "--state")
		st.Running = err == nil && strings.TrimSpace(out) == "running"
		st.Active = st.Running
		if st.Running {
			if ports, e := util.Run("firewall-cmd", "--list-ports"); e == nil {
				for _, p := range strings.Fields(ports) {
					st.Rules = append(st.Rules, parseFwdPort(p))
				}
			}
			if svcs, e := util.Run("firewall-cmd", "--list-services"); e == nil {
				for _, s := range strings.Fields(svcs) {
					st.Rules = append(st.Rules, Rule{Port: "svc:" + s, Action: "allow"})
				}
			}
		}
		return st
	}
	return st
}

func parseUFW(out string) []Rule {
	var rules []Rule
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "/") && !strings.Contains(line, "ALLOW") && !strings.Contains(line, "DENY") {
			continue
		}
		if strings.HasPrefix(line, "To") || strings.HasPrefix(line, "---") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		r := Rule{Port: fields[0]}
		if strings.Contains(line, "ALLOW") {
			r.Action = "allow"
		} else if strings.Contains(line, "DENY") {
			r.Action = "deny"
		}
		// "80/tcp ALLOW IN Anywhere" 或 "80 ALLOW IN 1.2.3.4"
		for i, f := range fields {
			if f == "IN" && i+1 < len(fields) && fields[i+1] != "Anywhere" {
				r.From = fields[i+1]
			}
		}
		if idx := strings.Index(line, "#"); idx > 0 {
			r.Comment = strings.TrimSpace(line[idx+1:])
		}
		rules = append(rules, r)
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Port < rules[j].Port })
	return rules
}

// parseUFWAdded 解析 `ufw show added` 输出(规则未启用时也能看到)
// 行格式如: "ufw allow 80/tcp" / "ufw allow from 1.2.3.4 to any port 22 proto tcp" / "ufw deny 3306"
func parseUFWAdded(out string) []Rule {
	var rules []Rule
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ufw ") {
			continue
		}
		fields := strings.Fields(line)
		// fields[0]=ufw fields[1]=allow|deny|limit|reject
		if len(fields) < 3 {
			continue
		}
		r := Rule{Action: fields[1]}
		rest := fields[2:]
		// "allow 8080/comment" 形式:端口与备注可能以 / 连接
		if i := strings.Index(rest[0], "/comment"); i > 0 {
			r.Comment = strings.Trim(rest[0][i+len("/comment"):], " '\"")
			rest[0] = rest[0][:i]
		}
		r.Port = rest[0]
		if len(rest) >= 2 && rest[1] == "from" && len(rest) >= 3 {
			// "from IP to any port N proto tcp"
			for i := 0; i < len(rest)-2; i++ {
				if rest[i] == "port" {
					r.Port = rest[i+1]
				}
				if rest[i] == "proto" {
					r.Proto = rest[i+1]
				}
			}
			for i := 0; i < len(rest)-1; i++ {
				if rest[i] == "from" {
					r.From = rest[i+1]
					break
				}
			}
		} else if strings.Contains(r.Port, "/") {
			parts := strings.SplitN(r.Port, "/", 2)
			r.Port = parts[0]
			r.Proto = parts[1]
		}
		rules = append(rules, r)
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Port < rules[j].Port })
	return rules
}

func parseFwdPort(p string) Rule {	r := Rule{Port: p, Action: "allow"}
	if i := strings.Index(p, "/"); i > 0 {
		r.Proto = p[i+1:]
	}
	return r
}

// ValidatePort 校验端口或端口段
func ValidatePort(p string) error {
	if strings.Contains(p, ":") {
		parts := strings.SplitN(p, ":", 2)
		a, e1 := strconv.Atoi(parts[0])
		b, e2 := strconv.Atoi(parts[1])
		if e1 != nil || e2 != nil || a < 1 || b > 65535 || a >= b {
			return fmt.Errorf("无效端口段 %s", p)
		}
		return nil
	}
	if !portRe.MatchString(p) {
		return fmt.Errorf("无效端口 %s", p)
	}
	v, _ := strconv.Atoi(p)
	if v < 1 || v > 65535 {
		return fmt.Errorf("端口超出范围 1-65535")
	}
	return nil
}

// Allow 开放端口(可带 /tcp /udp 后缀)
// ufwRun 执行 ufw 命令:失败时带输出报错并自动重试一次
// (ufw 为 python 脚本且每次操作重建整个规则集,偶发 exit 1 为瞬态)
func ufwRun(args ...string) (string, error) {
	out, err := util.Run("ufw", args...)
	if err != nil {
		time.Sleep(1500 * time.Millisecond)
		out2, err2 := util.Run("ufw", args...)
		if err2 != nil {
			return out2, fmt.Errorf("%v, 输出: %s", err2, strings.TrimSpace(out2))
		}
		return out2, nil
	}
	return out, nil
}

func Allow(port, comment string) error {
	port = strings.TrimSpace(port)
	if err := ValidatePort(strings.Split(port, "/")[0]); err != nil {
		return err
	}
	if len(comment) > 100 {
		comment = comment[:100]
	}
	st := Detect()
	switch st.Kind {
	case UFW:
		args := []string{"allow", port}
		if comment != "" {
			args = append(args, "comment", comment)
		}
		if _, err := ufwRun(args...); err != nil {
			return err
		}
		return nil
	case FIREWALLD:
		p, proto := splitPortProto(port)
		if p == "" {
			return fmt.Errorf("firewalld 需要明确端口")
		}
		if proto == "" {
			if _, err := util.Run("firewall-cmd", "--permanent", "--add-port", p+"/tcp"); err != nil {
				return err
			}
			if _, err := util.Run("firewall-cmd", "--permanent", "--add-port", p+"/udp"); err != nil {
				return err
			}
		} else if _, err := util.Run("firewall-cmd", "--permanent", "--add-port", p+"/"+proto); err != nil {
			return err
		}
		_, err := util.Run("firewall-cmd", "--reload")
		return err
	default:
		return fmt.Errorf("未检测到防火墙(UFW/Firewalld),请先安装")
	}
}

// Deny 拒绝/移除端口
func Deny(port string) error {
	port = strings.TrimSpace(port)
	if err := ValidatePort(strings.Split(port, "/")[0]); err != nil {
		return err
	}
	st := Detect()
	switch st.Kind {
	case UFW:
		if _, err := ufwRun("delete", "allow", port); err != nil {
			return err
		}
		return nil
	case FIREWALLD:
		p, proto := splitPortProto(port)
		if proto == "" {
			_, e1 := util.Run("firewall-cmd", "--permanent", "--remove-port", p+"/tcp")
			_, e2 := util.Run("firewall-cmd", "--permanent", "--remove-port", p+"/udp")
			if e1 != nil && e2 != nil {
				return fmt.Errorf("移除失败")
			}
		} else if _, err := util.Run("firewall-cmd", "--permanent", "--remove-port", p+"/"+proto); err != nil {
			return err
		}
		_, err := util.Run("firewall-cmd", "--reload")
		return err
	default:
		return fmt.Errorf("未检测到防火墙")
	}
}

// SetActive 启用/关闭防火墙。
// 启用前必须先放行:SSH 实际端口、面板自身端口、Web 80/443,
// 否则默认拒绝策略会把面板和站点全部挡在外面(自锁)。
func SetActive(on bool) error {
	st := Detect()
	switch st.Kind {
	case UFW:
		if on {
			allow := func(p string) {
				_, _ = util.Run("ufw", "allow", p)
			}
			allow(fmt.Sprintf("%d/tcp", sshPort()))
			allow(fmt.Sprintf("%d/tcp", func() int { return config.M.Get().Port }()))
			allow("80/tcp")
			allow("443/tcp")
			out, err := util.Run("ufw", "--force", "enable")
			if err != nil {
				return fmt.Errorf("ufw 启用失败: %s", strings.TrimSpace(out))
			}
			return nil
		}
		_, err := util.Run("ufw", "disable")
		return err
	case FIREWALLD:
		if on {
			if _, err := util.Run("systemctl", "start", "firewalld"); err != nil {
				return fmt.Errorf("启动 firewalld 失败")
			}
			// 默认 zone 已含 ssh;补放行面板与 Web 端口
			_, _ = util.Run("firewall-cmd", "--permanent", "--add-port", fmt.Sprintf("%d/tcp", func() int { return config.M.Get().Port }()))
			_, _ = util.Run("firewall-cmd", "--permanent", "--add-port=80/tcp")
			_, _ = util.Run("firewall-cmd", "--permanent", "--add-port=443/tcp")
			_, _ = util.Run("firewall-cmd", "--reload")
			return nil
		}
		_, err := util.Run("systemctl", "stop", "firewalld")
		return err
	default:
		return fmt.Errorf("未检测到防火墙")
	}
}

// sshPort 读取 SSH 实际端口(SSH 安全页改过端口时这里同步)
func sshPort() int {
	if st, err := secmgr.GetStatus(); err == nil && st.Port > 0 && st.Port <= 65535 {
		return st.Port
	}
	return 22
}

func splitPortProto(p string) (string, string) {
	if i := strings.Index(p, "/"); i > 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

// Install 一键安装防火墙(仅 Linux):ufw(apt) / firewalld(dnf/yum)
func Install(kind string, w io.Writer) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("仅在 Linux 上支持安装防火墙")
	}
	// 识别包管理器
	pm := ""
	b, err := os.ReadFile("/etc/os-release")
	id := strings.ToLower(string(b))
	switch {
	case err == nil && (strings.Contains(id, "ubuntu") || strings.Contains(id, "debian")):
		pm = "apt"
	case err == nil && (strings.Contains(id, "centos") || strings.Contains(id, "rhel") || strings.Contains(id, "almalinux") || strings.Contains(id, "rocky") || strings.Contains(id, "fedora")):
		pm = "dnf"
		if _, e := exec.LookPath("dnf"); e != nil {
			pm = "yum"
		}
	default:
		if _, e := exec.LookPath("apt-get"); e == nil {
			pm = "apt"
		} else if _, e := exec.LookPath("dnf"); e == nil {
			pm = "dnf"
		} else if _, e := exec.LookPath("yum"); e == nil {
			pm = "yum"
		}
	}
	if pm == "" {
		return fmt.Errorf("未识别的发行版,请手动安装防火墙")
	}
	pkg := "ufw"
	if kind == "firewalld" {
		pkg = "firewalld"
	}
	fmt.Fprintf(w, ">> 使用 %s 安装 %s ...\n", pm, pkg)
	var cmd *exec.Cmd
	if pm == "apt" {
		_, _ = util.RunWithTimeout(120*time.Second, "apt-get", "update")
		cmd = exec.Command("apt-get", "install", "-y", pkg)
	} else {
		cmd = exec.Command(pm, "install", "-y", pkg)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("安装失败: %v", err)
	}
	// firewalld 需启动服务
	if pkg == "firewalld" {
		fmt.Fprintln(w, ">> 启动 firewalld 服务 ...")
		_, _ = util.RunWithTimeout(30*time.Second, "systemctl", "enable", "--now", "firewalld")
	}
	fmt.Fprintln(w, ">> 安装完成")
	return nil
}
