package secmgr

// SSH 防爆破(fail2ban 集成):SSH 登录尝试次数过多的 IP 自动封禁,
// 支持查看封禁列表与手动解封。jail 用 backend = systemd——Debian 12 起
// 多数系统没有 /var/log/auth.log,只有 journald 里有登录失败记录。

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/util"
)

const (
	f2bJailName  = "sshd" // 发行版标准 jail 名(其 filter 自带 journald 匹配)
	f2bJailFile  = "/etc/fail2ban/jail.d/kun-sshd.local"
	f2bDefRetry  = 5   // 最大失败次数
	f2bDefFind   = 10  // 统计窗口(分钟)
	f2bDefBan    = 120 // 封禁时长(分钟)
	f2bDefTarget = "sshd"
)

// F2BState 防爆破状态
type F2BState struct {
	Installed  bool     `json:"installed"`   // fail2ban 已安装
	Running    bool     `json:"running"`     // 服务运行中
	JailActive bool     `json:"jail_active"` // 坤面板 SSH 防爆破 jail 已配置
	MaxRetry   int      `json:"max_retry"`   // 最大失败次数
	FindTime   int      `json:"findtime"`    // 统计窗口(分钟)
	BanTime    int      `json:"bantime"`     // 封禁时长(分钟)
	Port       int      `json:"port"`        // 监控的 SSH 端口
	Banned     []string `json:"banned"`      // 当前封禁的 IP
}

func f2bInstalled() bool {
	if _, err := exec.LookPath("fail2ban-client"); err == nil {
		return true
	}
	// LookPath 失败时用包管理器确认——systemctl cat 对不存在的 unit 也会输出
	// 含 "fail2ban" 的错误文本,不可作为判据
	if _, err := util.RunWithTimeout(10*time.Second, "dpkg", "-s", "fail2ban"); err == nil {
		return true
	}
	if _, err := util.RunWithTimeout(10*time.Second, "rpm", "-q", "fail2ban"); err == nil {
		return true
	}
	return false
}

func f2bRunning() bool {
	out, _ := util.RunWithTimeout(10*time.Second, "systemctl", "is-active", "fail2ban")
	return strings.TrimSpace(out) == "active"
}

func f2bClient(args ...string) (string, error) {
	full := append([]string{"fail2ban-client"}, args...)
	out, err := util.RunWithTimeout(20*time.Second, full[0], full[1:]...)
	return strings.TrimSpace(out), err
}

func tailOut(s string) string {
	if len(s) > 200 {
		return s[len(s)-200:]
	}
	return s
}

// currentSSHPortNum 当前生效的 SSH 端口
func currentSSHPortNum() (int, error) {
	st, err := GetStatus()
	if err != nil {
		return 0, err
	}
	if st.Port < 1 || st.Port > 65535 {
		return 0, fmt.Errorf("SSH 端口异常")
	}
	return st.Port, nil
}

// F2BStatus 读取防爆破状态
func F2BStatus() (*F2BState, error) {
	st := &F2BState{Installed: f2bInstalled(), MaxRetry: f2bDefRetry, FindTime: f2bDefFind, BanTime: f2bDefBan, Banned: []string{}}
	st.Running = st.Installed && f2bRunning()
	if b, err := os.ReadFile(f2bJailFile); err == nil && strings.Contains(string(b), "["+f2bDefTarget+"]") {
		st.JailActive = true
		conf := string(b)
		if m := regexp.MustCompile(`maxretry\s*=\s*(\d+)`).FindStringSubmatch(conf); m != nil {
			st.MaxRetry, _ = strconv.Atoi(m[1])
		}
		if m := regexp.MustCompile(`findtime\s*=\s*(\d+)m`).FindStringSubmatch(conf); m != nil {
			st.FindTime, _ = strconv.Atoi(m[1])
		}
		if m := regexp.MustCompile(`bantime\s*=\s*(\d+)m`).FindStringSubmatch(conf); m != nil {
			st.BanTime, _ = strconv.Atoi(m[1])
		}
		if m := regexp.MustCompile(`(?m)^\s*port\s*=\s*(\d+)`).FindStringSubmatch(conf); m != nil {
			st.Port, _ = strconv.Atoi(m[1])
		}
	}
	if st.Running && st.JailActive {
		if out, err := f2bClient("status", f2bJailName); err == nil {
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, "Banned IP list") {
					parts := strings.SplitN(l, ":", 2)
					if len(parts) == 2 {
						for _, ip := range strings.Fields(parts[1]) {
							st.Banned = append(st.Banned, ip)
						}
					}
				}
			}
		}
	}
	if st.Banned == nil {
		st.Banned = []string{}
	}
	return st, nil
}

// fail2banInstall 安装 fail2ban:按发行版依次尝试(apt/dnf/yum/zypper),
// 全部使用参数数组执行,无 shell 拼接
func fail2banInstall(w io.Writer) error {
	fmt.Fprintln(w, ">> 安装 fail2ban ...")
	// Debian/Ubuntu:先刷新索引再安装
	if _, err := util.RunWithTimeout(120*time.Second, "apt-get", "update", "-qq"); err == nil {
		if _, err := util.RunWithTimeout(300*time.Second, "apt-get", "install", "-y", "-qq", "--no-install-recommends", "fail2ban"); err == nil {
			fmt.Fprintln(w, ">> fail2ban 安装完成")
			return nil
		}
	}
	// RHEL 系:fail2ban 在 EPEL 仓库
	_, _ = util.RunWithTimeout(180*time.Second, "dnf", "install", "-y", "epel-release")
	_, _ = util.RunWithTimeout(180*time.Second, "yum", "install", "-y", "epel-release")
	if _, err := util.RunWithTimeout(300*time.Second, "dnf", "install", "-y", "fail2ban"); err == nil {
		fmt.Fprintln(w, ">> fail2ban 安装完成")
		return nil
	}
	if _, err := util.RunWithTimeout(300*time.Second, "yum", "install", "-y", "fail2ban"); err == nil {
		fmt.Fprintln(w, ">> fail2ban 安装完成")
		return nil
	}
	// SUSE 系
	if _, err := util.RunWithTimeout(300*time.Second, "zypper", "--non-interactive", "install", "fail2ban"); err == nil {
		fmt.Fprintln(w, ">> fail2ban 安装完成")
		return nil
	}
	return fmt.Errorf("fail2ban 安装失败:apt/dnf/yum/zypper 均未成功,请手动安装后重试")
}

// pkgInstall 按发行版安装软件包(apt/dnf/yum/zypper),纯参数数组执行
func pkgInstall(w io.Writer, pkgs ...string) error {
	// Debian/Ubuntu:先刷新索引再安装
	if _, err := util.RunWithTimeout(120*time.Second, "apt-get", "update", "-qq"); err == nil {
		args := append([]string{"install", "-y", "-qq", "--no-install-recommends"}, pkgs...)
		if _, err := util.RunWithTimeout(600*time.Second, "apt-get", args...); err == nil {
			return nil
		}
	}
	// RHEL 系(fail2ban 需要 EPEL;python3-systemd 在主仓库)
	_, _ = util.RunWithTimeout(180*time.Second, "dnf", "install", "-y", "epel-release")
	_, _ = util.RunWithTimeout(180*time.Second, "yum", "install", "-y", "epel-release")
	if _, err := util.RunWithTimeout(300*time.Second, "dnf", append([]string{"install", "-y", "--no-install-recommends"}, pkgs...)...); err == nil {
		return nil
	}
	if _, err := util.RunWithTimeout(300*time.Second, "yum", append([]string{"install", "-y"}, pkgs...)...); err == nil {
		return nil
	}
	// SUSE 系
	if _, err := util.RunWithTimeout(300*time.Second, "zypper", append([]string{"--non-interactive", "install"}, pkgs...)...); err == nil {
		return nil
	}
	return fmt.Errorf("安装失败(%s):apt/dnf/yum/zypper 均未成功,请手动安装后重试", strings.Join(pkgs, " "))
}

// F2BEnable 启用 SSH 防爆破(安装 fail2ban → 写 jail → 启动)
func F2BEnable(maxRetry, findMin, banMin int, w io.Writer) error {
	if !f2bInstalled() {
		if err := fail2banInstall(w); err != nil {
			return err
		}
	}
	port, err := currentSSHPortNum()
	if err != nil {
		return err
	}
	// 无 /var/log/auth.log(Debian 12 起默认无 rsyslog)的系统必须用 systemd
	// backend;有日志文件的用 auto。python3-systemd 是 systemd backend 的依赖
	backend := "auto"
	if _, err := os.Stat("/var/log/auth.log"); err != nil {
		backend = "systemd"
		_ = pkgInstall(w, "python3-systemd")
	}
	jail := fmt.Sprintf("[%s]\nenabled = true\nbackend = %s\nmaxretry = %d\nfindtime = %dm\nbantime = %dm\nport = %d\n",
		f2bDefTarget, backend, maxRetry, findMin, banMin, port)
	_ = os.MkdirAll("/etc/fail2ban/jail.d", 0o755)
	if err := os.WriteFile(f2bJailFile, []byte("# 由坤面板生成\n"+jail), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(w, ">> 启动 fail2ban 服务 ...")
	_, _ = util.RunWithTimeout(30*time.Second, "systemctl", "enable", "fail2ban")
	if _, err := util.RunWithTimeout(30*time.Second, "systemctl", "restart", "fail2ban"); err != nil {
		return fmt.Errorf("fail2ban 启动失败: %v", err)
	}
	// fail2ban(Python)从启动到控制 socket 就绪需要数秒,轮询等待
	var ready bool
	var lastOut string
	for i := 0; i < 12; i++ {
		time.Sleep(1500 * time.Millisecond)
		if out, err := f2bClient("ping"); err == nil {
			ready = true
			break
		} else {
			lastOut = tailOut(out)
		}
	}
	if !ready {
		return fmt.Errorf("fail2ban 启动失败: %s", lastOut)
	}
	if out, err := f2bClient("status", f2bDefTarget); err != nil {
		return fmt.Errorf("jail 启动失败: %s", tailOut(out))
	}
	fmt.Fprintf(w, ">> SSH 防爆破已启用:%d 分钟内失败 %d 次 → 封禁 IP %d 分钟\n", findMin, maxRetry, banMin)
	return nil
}

// F2BDisable 关闭防爆破(移除坤面板 jail,保留 fail2ban 本体与其他 jail)
func F2BDisable() error {
	if !f2bInstalled() {
		return nil
	}
	_, _ = f2bClient("stop", f2bDefTarget)
	_ = os.Remove(f2bJailFile)
	_, _ = f2bClient("reload")
	return nil
}

// F2BSetParams 调整防爆破参数(重写 jail 并重载)
func F2BSetParams(maxRetry, findMin, banMin int) error {
	if _, err := os.Stat(f2bJailFile); err != nil {
		return fmt.Errorf("防爆破尚未启用")
	}
	if maxRetry < 1 || maxRetry > 100 || findMin < 1 || findMin > 1440 || banMin < 1 || banMin > 525600 {
		return fmt.Errorf("参数超出范围")
	}
	port, err := currentSSHPortNum()
	if err != nil {
		return err
	}
	conf := fmt.Sprintf("[%s]\nenabled = true\nfilter = sshd\nport = %d\nbackend = systemd\nmaxretry = %d\nfindtime = %dm\nbantime = %dm\n",
		f2bJailName, port, maxRetry, findMin, banMin)
	if err := os.WriteFile(f2bJailFile, []byte("# 由坤面板生成\n"+conf), 0o644); err != nil {
		return err
	}
	_, _ = f2bClient("reload")
	return nil
}

// F2BUnban 解封指定 IP
func F2BUnban(ip string) error {
	if !regexp.MustCompile(`^[0-9a-fA-F.:]{3,45}$`).MatchString(ip) {
		return fmt.Errorf("IP 格式不正确")
	}
	out, err := f2bClient("set", f2bDefTarget, "unbanip", ip)
	if err != nil {
		// 该 IP 也可能被其他/默认 jail 封禁,逐一尝试解封
		if _, err2 := f2bClient("set", "sshd", "unbanip", ip); err2 == nil { // 已是默认名,保持
			return nil
		}
		return fmt.Errorf("解封失败: %s", tailOut(out))
	}
	return nil
}

// syncJailPort SSH 端口变更后,防爆破 jail 已启用时同步监控新端口
func syncJailPort(port int) {
	if _, err := os.Stat(f2bJailFile); err != nil {
		return
	}
	b, err := os.ReadFile(f2bJailFile)
	if err != nil {
		return
	}
	re := regexp.MustCompile(`(?m)^(\s*port\s*=\s*)\d+\s*$`)
	updated := re.ReplaceAllString(string(b), "${1}"+strconv.Itoa(port))
	if updated != string(b) {
		_ = os.WriteFile(f2bJailFile, []byte(updated), 0o644)
		_, _ = f2bClient("reload")
	}
}
