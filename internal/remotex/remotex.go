package remotex

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"kunpanel/internal/config"
	"kunpanel/internal/util"
)

// Config SFTP 远程备份目标
type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"` // Get 返回时脱敏为空
	Dir      string `json:"dir"`
	// HostKeyFingerprint 可选:SHA256 主机指纹(ssh.FingerprintSHA256 格式)。
	// 非空时强制校验,防中间机窃取备份凭据;为空时兼容自建服务器/家宽 NAS 场景。
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
	PasswordSet        bool   `json:"password_set"` // Get 返回:密码是否已配置
}

// LoadPlain 读取含密码的完整配置(仅上传/测试连接等内部用途)
func LoadPlain() Config { return load() }

func cfgPath() string { return filepath.Join(config.DataDir(), "remote_backup.json") }

var mu sync.Mutex

// Get 读取配置(密码脱敏:只返回是否已配置,不回传明文)
func Get() Config {
	c := load()
	c.Password = ""
	c.PasswordSet = load().Password != ""
	return c
}

func load() Config {
	mu.Lock()
	defer mu.Unlock()
	b, err := os.ReadFile(cfgPath())
	if err != nil {
		return Config{}
	}
	var c Config
	if json.Unmarshal(b, &c) == nil {
		if c.Port <= 0 {
			c.Port = 22
		}
		return c
	}
	return Config{}
}

// Save 保存配置(Password 为空表示沿用已存密码)
func Save(c Config) error {
	mu.Lock()
	defer mu.Unlock()
	if c.Port <= 0 {
		c.Port = 22
	}
	if c.Password == "" {
		c.Password = load().Password
	}
	c.PasswordSet = c.Password != ""
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(cfgPath(), b, 0o600)
}

// connect 建立 SFTP 客户端
func connect(c Config) (*sftp.Client, error) {
	if c.Host == "" || c.User == "" {
		return nil, fmt.Errorf("远程备份未配置(需要主机与用户名)")
	}
	port := c.Port
	if port <= 0 {
		port = 22
	}
	// 主机密钥必须校验:空白名单放行 = 接受中间人攻击,备份凭据与数据可被窃取。
	fp := strings.TrimSpace(c.HostKeyFingerprint)
	if fp == "" {
		// 首次登记(TOFU):握手抓取服务器实际指纹回显,用户确认后填入保存
		var actual string
		capCb := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			actual = ssh.FingerprintSHA256(key)
			return fmt.Errorf("capture-only")
		}
		capCfg := &ssh.ClientConfig{
			User:            c.User,
			Auth:            []ssh.AuthMethod{ssh.Password(c.Password)},
			HostKeyCallback: capCb,
			Timeout:         12 * time.Second,
		}
		cl, err := ssh.Dial("tcp", net.JoinHostPort(c.Host, fmt.Sprintf("%d", port)), capCfg)
		if cl != nil {
			cl.Close()
		}
		if actual != "" {
			return nil, fmt.Errorf("尚未配置主机指纹,已拒绝连接(防中间人攻击)。服务器实际指纹: %s —— 确认无误后把它填入「主机指纹」并保存即可", actual)
		}
		return nil, fmt.Errorf("尚未配置主机指纹,已拒绝连接;且无法获取服务器指纹(%v)。可手动获取: ssh-keyscan -t ed25519 <主机> 后用 ssh-keygen -lf 查看", err)
	}
	hostKeyCb := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		actual := ssh.FingerprintSHA256(key)
		if actual != fp {
			return fmt.Errorf("主机指纹不匹配: 期望 %s,实际 %s", fp, actual)
		}
		return nil
	}
	sshCfg := &ssh.ClientConfig{
		User:            c.User,
		Auth:            []ssh.AuthMethod{ssh.Password(c.Password)},
		HostKeyCallback: hostKeyCb,
		Timeout:         12 * time.Second,
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(c.Host, fmt.Sprintf("%d", port)), sshCfg)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %v", err)
	}
	sc, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("SFTP 会话失败: %v", err)
	}
	return sc, nil
}

// Test 测试连接:登录 + 确保目录存在
func Test(c Config) error {
	sc, err := connect(c)
	if err != nil {
		return err
	}
	defer sc.Close()
	dir := c.Dir
	if dir == "" {
		dir = "."
	}
	if fi, e := sc.Stat(dir); e != nil {
		if e = sc.MkdirAll(dir); e != nil {
			return fmt.Errorf("无法创建远程目录 %s: %v", dir, e)
		}
	} else if !fi.IsDir() {
		return fmt.Errorf("远程路径 %s 不是目录", dir)
	}
	return nil
}

// Upload 上传本地文件到远程目录(同名覆盖),返回远程完整路径
func Upload(c Config, localPath string) (string, error) {
	fi, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("本地文件不存在: %v", err)
	}
	sc, err := connect(c)
	if err != nil {
		return "", err
	}
	defer sc.Close()
	dir := c.Dir
	if dir == "" {
		dir = "."
	}
	if _, e := sc.Stat(dir); e != nil {
		if e = sc.MkdirAll(dir); e != nil {
			return "", fmt.Errorf("无法创建远程目录: %v", e)
		}
	}
	remote := dir + "/" + filepath.Base(localPath)
	src, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := sc.Create(remote)
	if err != nil {
		return "", fmt.Errorf("远程写入失败: %v", err)
	}
	defer dst.Close()
	n, err := dst.ReadFrom(src)
	if err != nil {
		return "", fmt.Errorf("上传中断: %v", err)
	}
	if n != fi.Size() {
		return "", fmt.Errorf("上传不完整: %d/%d 字节", n, fi.Size())
	}
	return remote, nil
}
