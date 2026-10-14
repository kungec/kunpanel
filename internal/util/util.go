package util

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RandomString 生成密码学安全的随机字符串
func RandomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		b[i] = letters[x.Int64()]
	}
	return string(b)
}

// RandomInt 返回 [min, max] 范围内的随机整数
func RandomInt(min, max int) int {
	x, _ := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	return int(x.Int64()) + min
}

// RandomEntrance 生成随机后台入口路径(小写字母+数字,去除易混淆字符)
func RandomEntrance() string {
	const cs = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(cs))))
		b[i] = cs[x.Int64()]
	}
	return string(b)
}

// PickFreePort 返回一个随机且当前可绑定的端口(随机端口策略),失败回退 20000
func PickFreePort() int {
	for i := 0; i < 20; i++ {
		p := RandomInt(20000, 65535)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			_ = ln.Close()
			return p
		}
	}
	return 20000
}

// Hash 计算 sha256(salt+password) 的十六进制值(旧版算法,登录时自动升级)
func Hash(password, salt string) string {
	h := sha256.Sum256([]byte(salt + password))
	return hex.EncodeToString(h[:])
}

// HashPassword 计算 PBKDF2-HMAC-SHA256 口令哈希(当前存储算法)
func HashPassword(password, salt string) string {
	dk := PBKDF2([]byte(password), []byte(salt), PBKDF2Iterations, 32)
	return hex.EncodeToString(dk)
}

// ConstantTimeEq 常量时间字符串比较,防止时序侧信道
func ConstantTimeEq(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// HMAC 对 payload 做 hmac-sha256 签名
func HMAC(secret, payload string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func IsDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// HumanBytes 人类可读的容量格式
func HumanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// LocalIP 获取本机出口 IP(不真正发包)
func LocalIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:53", 2*time.Second)
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			return addr.IP.String()
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// Run 执行命令并返回合并输出
func Run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = ChildEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ChildEnv 子进程环境。systemd 服务默认没有 HOME,而 docker exec 等
// CLI 在无 HOME 时会挂死(实测 Docker 29),故兜底注入。
func ChildEnv() []string {
	env := os.Environ()
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			return env
		}
	}
	home := "/root"
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		home = h
	} else if u := os.Getenv("USER"); u != "" {
		home = "/home/" + u
	}
	return append(env, "HOME="+home)
}

// RunWithTimeout 带超时执行命令
func RunWithTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = ChildEnv()
	var sb strings.Builder
	cmd.Stdout = &sb
	cmd.Stderr = &sb
	if err := cmd.Start(); err != nil {
		return sb.String(), err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return sb.String(), fmt.Errorf("命令超时(%s)", timeout)
	case err := <-done:
		if err != nil {
			return sb.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(sb.String()))
		}
		return sb.String(), nil
	}
}

// DataDir 解析数据目录:环境变量 KP_DATA_DIR > 平台默认 > 可执行文件旁 data/
func DataDir() string {
	if d := os.Getenv("KP_DATA_DIR"); d != "" {
		return d
	}
	switch runtime.GOOS {
	case "windows":
		return `C:\kunpanel\data`
	default:
		return "/opt/kunpanel/data"
	}
}

// FallbackDataDir 程序所在目录下的 data(无权限写默认路径时使用)
func FallbackDataDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "data"
	}
	return filepath.Join(filepath.Dir(exe), "data")
}

func HomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// SanitizePath 防止路径穿越(限制在 base 内,path 为空则用 base)
func SanitizePath(base, path string) string {
	if path == "" {
		return base
	}
	p := filepath.Clean(path)
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return p
}

func FileModeString(mode os.FileMode) string {
	return fmt.Sprintf("%v", mode.Perm())
}

// RunWithTimeoutWriter 带超时执行命令,输出实时写入 w(进度可见)
func RunWithTimeoutWriter(timeout time.Duration, w io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = ChildEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, e := stdout.Read(buf)
			if n > 0 && w != nil {
				w.Write(buf[:n])
			}
			if e != nil {
				break
			}
		}
		done <- cmd.Wait()
	}()
	select {
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return fmt.Errorf("命令超时(%s)", timeout)
	case err := <-done:
		return err
	}
}
