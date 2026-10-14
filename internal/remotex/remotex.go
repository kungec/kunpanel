package remotex

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"kunpanel/internal/config"
)

// Config SFTP 远程备份目标
type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Dir      string `json:"dir"`
}

func cfgPath() string { return filepath.Join(config.DataDir(), "remote_backup.json") }

var mu sync.Mutex

// Get 读取配置(密码明文存于数据目录,与面板自身的凭据同级保护)
func Get() Config {
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

// Save 保存配置
func Save(c Config) error {
	mu.Lock()
	defer mu.Unlock()
	if c.Port <= 0 {
		c.Port = 22
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath(), b, 0o600)
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
	sshCfg := &ssh.ClientConfig{
		User: c.User,
		Auth: []ssh.AuthMethod{ssh.Password(c.Password)},
		// 面板备份目标多为自建服务器/家宽 NAS,不强制校验主机指纹
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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
