// Package secmgr SSH 安全加固:端口修改 / Root 与密码登录开关 / authorized_keys 管理
package secmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/util"
)

// Status SSH 当前生效配置
type Status struct {
	Service      string   `json:"service"`       // ssh / sshd
	Port         int      `json:"port"`          // 当前端口
	PermitRoot   string   `json:"permit_root"`   // yes / no / prohibit-password / unset
	PasswordAuth string   `json:"password_auth"` // yes / no / unset
	ConfigFile   string   `json:"config_file"`
	Keys         []string `json:"keys"` // authorized_keys 公钥行
	HasInclude   bool     `json:"has_include"`
}

const mainConf = "/etc/ssh/sshd_config"
const ourDropin = "/etc/ssh/sshd_config.d/00-kunpanel.conf"

var portLineRe = regexp.MustCompile(`(?m)^\s*#?\s*Port\s+(\d+)\s*$`)

func detectService() string {
	for _, svc := range []string{"ssh", "sshd"} {
		if out, err := util.Run("systemctl", "is-active", svc); err == nil && strings.TrimSpace(out) == "active" {
			return svc
		}
	}
	return "ssh"
}

// GetStatus 读取当前配置(主配置 + sshd_config.d 覆盖)
func GetStatus() (*Status, error) {
	st := &Status{Service: detectService(), Port: 22, PermitRoot: "unset", PasswordAuth: "unset", ConfigFile: mainConf}
	b, err := os.ReadFile(mainConf)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %v", mainConf, err)
	}
	conf := string(b)
	// sshd 取第一个出现的值;Include 目录里的文件优先(通常 Include 在文件顶部)
	if m := portLineRe.FindStringSubmatch(conf); m != nil {
		st.Port, _ = strconv.Atoi(m[1])
	}
	if m := regexp.MustCompile(`(?m)^\s*#?\s*PermitRootLogin\s+(\S+)`).FindStringSubmatch(conf); m != nil {
		st.PermitRoot = m[1]
	}
	if m := regexp.MustCompile(`(?m)^\s*#?\s*PasswordAuthentication\s+(\S+)`).FindStringSubmatch(conf); m != nil {
		st.PasswordAuth = m[1]
	}
	if drop, err := readDropins(); err == nil && drop != "" {
		if m := regexp.MustCompile(`(?m)^\s*Port\s+(\d+)`).FindStringSubmatch(drop); m != nil {
			st.Port, _ = strconv.Atoi(m[1])
		}
		if m := regexp.MustCompile(`(?m)^\s*PermitRootLogin\s+(\S+)`).FindStringSubmatch(drop); m != nil {
			st.PermitRoot = m[1]
		}
		if m := regexp.MustCompile(`(?m)^\s*PasswordAuthentication\s+(\S+)`).FindStringSubmatch(drop); m != nil {
			st.PasswordAuth = m[1]
		}
	}
	st.HasInclude = strings.Contains(conf, "Include /etc/ssh/sshd_config.d") || strings.Contains(conf, "Include\t/etc/ssh/sshd_config.d")
	st.Keys, _ = ListKeys()
	return st, nil
}

func readDropins() (string, error) {
	ents, err := os.ReadDir("/etc/ssh/sshd_config.d")
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/etc/ssh/sshd_config.d", e.Name()))
		if err == nil {
			sb.Write(b)
			sb.WriteString("\n")
		}
	}
	return sb.String(), nil
}

// apply 写入设置并安全重启:备份 → 写配置 → sshd -t → 重启(失败自动回滚)
func apply(settings map[string]string) error {
	b, err := os.ReadFile(mainConf)
	if err != nil {
		return err
	}
	backup := fmt.Sprintf("%s.bak.%s", mainConf, time.Now().Format("20060102150405"))
	_ = util.AtomicWrite(backup, b, 0o600)

	if hasIncludeDir() {
		// 放到 sshd_config.d,文件名 00- 保证最先被 Include(sshd 首个值生效)
		var sb strings.Builder
		for k, v := range settings {
			sb.WriteString(k + " " + v + "\n")
		}
		if err := util.AtomicWrite(ourDropin, []byte("# 由坤面板生成\n"+sb.String()), 0o644); err != nil {
			return err
		}
	} else {
		// 无 Include:主配置内就地替换(注释旧行,顶部追加新值,首个值生效)
		conf := string(b)
		for k := range settings {
			re := regexp.MustCompile(`(?m)^(\s*#?\s*` + k + `\s+.*$)`)
			if re.MatchString(conf) {
				conf = re.ReplaceAllString(conf, "# $1")
			}
		}
		var sb strings.Builder
		sb.WriteString("# --- kunpanel settings ---\n")
		for k, v := range settings {
			sb.WriteString(k + " " + v + "\n")
		}
		conf = sb.String() + conf
		if err := util.AtomicWrite(mainConf, []byte(conf), 0o600); err != nil {
			return err
		}
	}

	// 语法检查
	if out, err := util.RunWithTimeout(10*time.Second, sshdBin(), "-t"); err != nil {
		rollback(backup)
		return fmt.Errorf("sshd 配置校验失败,已回滚: %s", strings.TrimSpace(out))
	}
	svc := detectService()
	if out, err := util.RunWithTimeout(30*time.Second, "systemctl", "restart", svc); err != nil {
		rollback(backup)
		_, _ = util.RunWithTimeout(30*time.Second, "systemctl", "restart", svc)
		return fmt.Errorf("重启 SSH 服务失败,已回滚: %s", strings.TrimSpace(out))
	}
	return nil
}

func rollback(backup string) {
	b, err := os.ReadFile(backup)
	if err != nil {
		return
	}
	_ = util.AtomicWrite(mainConf, b, 0o600)
	_ = os.Remove(ourDropin)
	_, _ = util.RunWithTimeout(10*time.Second, sshdBin(), "-t")
}

func hasIncludeDir() bool {
	if _, err := os.Stat("/etc/ssh/sshd_config.d"); err != nil {
		return false
	}
	b, err := os.ReadFile(mainConf)
	if err != nil {
		return false
	}
	// 有 Include 指令才会加载 drop-in
	return strings.Contains(string(b), "Include /etc/ssh/sshd_config.d") ||
		strings.Contains(string(b), "Include\t/etc/ssh/sshd_config.d") ||
		strings.Contains(string(b), "Include  /etc/ssh/sshd_config.d")
}

func sshdBin() string {
	for _, p := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "sshd"
}

// SetPort 修改 SSH 端口(自动放行防火墙新端口)
func SetPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口超出范围 1-65535")
	}
	if port < 1024 && port != 22 {
		return fmt.Errorf("低于 1024 的端口易与系统服务冲突,请使用 1024-65535")
	}
	st, err := GetStatus()
	if err != nil {
		return err
	}
	if st.Port == port {
		return fmt.Errorf("当前端口已是 %d", port)
	}
	// 启用中的防火墙先放行新端口,防止把自己锁在外面
	openFirewallPort(port)
	if err := apply(map[string]string{"Port": strconv.Itoa(port)}); err != nil {
		return err
	}
	// 防爆破 jail 已启用时同步监控新端口(改写 jail 的 port 行并重载)
	syncJailPort(port)
	return nil
}

// SetHardening Root 登录与密码认证开关;关闭密码认证前必须有公钥,否则拒绝
func SetHardening(permitRoot, passwordAuth string) error {
	settings := map[string]string{}
	switch permitRoot {
	case "yes", "no", "prohibit-password":
		settings["PermitRootLogin"] = permitRoot
	case "":
	default:
		return fmt.Errorf("permit_root 取值非法")
	}
	switch passwordAuth {
	case "yes", "no":
		settings["PasswordAuthentication"] = passwordAuth
	case "":
	default:
		return fmt.Errorf("password_auth 取值非法")
	}
	if len(settings) == 0 {
		return fmt.Errorf("没有要修改的设置")
	}
	if passwordAuth == "no" {
		keys, _ := ListKeys()
		if len(keys) == 0 {
			return fmt.Errorf("尚未配置任何 SSH 公钥,关闭密码登录将把自己锁在服务器外,请先添加公钥")
		}
	}
	return apply(settings)
}

// openFirewallPort 通过防火墙放行端口(UFW/Firewalld,尽力而为)
func openFirewallPort(port int) {
	ps := strconv.Itoa(port)
	if _, err := os.Stat("/etc/ufw"); err == nil {
		_, _ = util.Run("ufw", "allow", ps+"/tcp")
		return
	}
	if _, err := util.Run("firewall-cmd", "--state"); err == nil {
		_, _ = util.Run("firewall-cmd", "--permanent", "--add-port", ps+"/tcp")
		_, _ = util.Run("firewall-cmd", "--reload")
	}
}

// ---------- authorized_keys 管理 ----------

func authKeysFile() string { return "/root/.ssh/authorized_keys" }

// ListKeys 公钥列表(每行一条)
func ListKeys() ([]string, error) {
	b, err := os.ReadFile(authKeysFile())
	if err != nil {
		return []string{}, nil
	}
	out := []string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out, nil
}

var keyTypeRe = regexp.MustCompile(`^(ssh-(rsa|dss|ed25519)|ecdsa-sha2-\S+|sk-(ssh|ecdsa)-\S+)\s+\S+`)

// AddKey 添加公钥
func AddKey(pub string) error {
	pub = strings.TrimSpace(pub)
	if !keyTypeRe.MatchString(pub) {
		return fmt.Errorf("公钥格式不正确(应以 ssh-ed25519 / ssh-rsa 等开头)")
	}
	if len(pub) > 4000 {
		return fmt.Errorf("公钥内容过长")
	}
	if err := os.MkdirAll("/root/.ssh", 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(authKeysFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(pub + "\n")
	return err
}

// RemoveKey 按序号删除公钥(从 1 开始)
func RemoveKey(index int) error {
	keys, err := ListKeys()
	if err != nil {
		return err
	}
	if index < 1 || index > len(keys) {
		return fmt.Errorf("序号超出范围(共 %d 条)", len(keys))
	}
	keys = append(keys[:index-1], keys[index:]...)
	content := ""
	if len(keys) > 0 {
		content = strings.Join(keys, "\n") + "\n"
	}
	return util.AtomicWrite(authKeysFile(), []byte(content), 0o600)
}
