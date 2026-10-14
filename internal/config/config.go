package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/util"
)

type Config struct {
	Port         int    `json:"port"`
	Username     string `json:"username"`
	Salt         string `json:"salt"`
	PasswordHash string `json:"password_hash"`
	HashAlgo     string `json:"hash_algo"`           // pbkdf2(当前) / 空 = 旧版 sha256,登录成功时自动升级
	JWTSecret    string `json:"jwt_secret"`
	Entrance     string `json:"entrance"` // 安全入口,空则关闭
	TotpSecret   string `json:"totp_secret,omitempty"` // 两步验证密钥(base32),空 = 未设置
	TotpEnabled  bool   `json:"totp_enabled"`          // 两步验证是否已启用
	PanelName    string `json:"panel_name"`
	SSL          bool   `json:"ssl"`
	Language     string `json:"language"`
	CreatedAt    string `json:"created_at"`
}

type Manager struct {
	mu   sync.RWMutex
	path string
	Cfg  *Config
}

var (
	M           *Manager
	InitialPwd  string // 首次安装生成的初始密码(同时落盘)
	initialFile string
)

func DataDir() string {
	dir := util.DataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		dir = util.FallbackDataDir()
		_ = os.MkdirAll(dir, 0o755)
	}
	return dir
}

// Load 读取配置,不存在则初始化(生成随机密码/密钥/入口)
func Load() (*Manager, error) {
	dir := DataDir()
	M = &Manager{path: filepath.Join(dir, "config.json")}
	initialFile = filepath.Join(dir, "initial_password.txt")

	if b, err := os.ReadFile(M.path); err == nil {
		if err := json.Unmarshal(b, &M.Cfg); err == nil {
			// 旧版本升级迁移:自动补生成随机安全入口(每人安装各异)
			if M.Cfg.Entrance == "" {
				M.Cfg.Entrance = util.RandomEntrance()
				_ = M.Save()
				fmt.Println("已为面板自动生成随机安全入口:/", M.Cfg.Entrance, "(可在面板设置中修改或关闭)")
			}
			return M, nil
		}
	}

	// ---- 首次初始化:随机端口 + 随机入口,每个安装实例独一无二 ----
	cfg := &Config{
		Port:      util.PickFreePort(),
		Username:  "admin",
		PanelName: "坤面板",
		Language:  "zh",
		Entrance:  util.RandomEntrance(),
		CreatedAt: nowStr(),
	}
	if v := os.Getenv("KP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			cfg.Port = p
		}
	}
	if v := os.Getenv("KP_USERNAME"); v != "" {
		cfg.Username = v
	}
	if v := os.Getenv("KP_ENTRANCE"); v != "" {
		cfg.Entrance = normalizeEntrance(v)
	}
	cfg.JWTSecret = util.RandomString(32)
	pwd := os.Getenv("KP_PASSWORD")
	if pwd == "" {
		pwd = util.RandomString(12)
	}
	InitialPwd = pwd
	cfg.Salt = util.RandomString(16)
	cfg.HashAlgo = "pbkdf2"
	cfg.PasswordHash = util.HashPassword(pwd, cfg.Salt)

	M.Cfg = cfg
	if err := M.Save(); err != nil {
		return nil, err
	}

	// 初始密码落盘,便于安装脚本读取展示
	addr := fmt.Sprintf("http://<服务器IP>:%d", cfg.Port)
	if cfg.Entrance != "" && cfg.Entrance != "-" {
		addr += "/" + cfg.Entrance
	}
	info := fmt.Sprintf("KunPanel 初始账号信息\n面板地址: %s\n用户名: %s\n密码: %s\n\n* 面板地址含随机入口路径,直接访问 IP:端口 将返回 404。\n* 入口可在【面板设置】中修改(设为 off 关闭入口校验),修改密码后请删除本文件。\n", addr, cfg.Username, pwd)
	_ = os.WriteFile(initialFile, []byte(info), 0o600)
	// 控制台同步打印,供首次启动/安装时展示
	fmt.Println("==============================================")
	fmt.Println("  KunPanel (坤面板) 初始化完成")
	fmt.Printf("  数据目录: %s\n", dir)
	fmt.Printf("  面板地址: %s\n", addr)
	fmt.Printf("  用户名:   %s\n", cfg.Username)
	fmt.Printf("  密码:     %s\n", pwd)
	fmt.Println("==============================================")
	return M, nil
}

// normalizeEntrance 归一化入口:小写、去空白与斜杠;off/- 表示关闭;非法字符回退为随机生成
func normalizeEntrance(v string) string {
	v = strings.ToLower(strings.Trim(strings.TrimSpace(v), "/"))
	if v == "off" || v == "-" {
		return "-"
	}
	if v == "" {
		return util.RandomEntrance()
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return util.RandomEntrance()
		}
	}
	if len(v) < 4 || len(v) > 32 {
		return util.RandomEntrance()
	}
	return v
}

func nowStr() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// InitialPwdFile 返回初始密码记录文件路径
func InitialPwdFile() string {
	return initialFile
}

// AvatarPath 返回用户头像存储路径
func AvatarPath() string {
	return filepath.Join(DataDir(), "avatar.png")
}

func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := json.MarshalIndent(m.Cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, b, 0o600)
}

func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return *m.Cfg
}

func (m *Manager) Update(fn func(c *Config)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.Cfg)
	b, err := json.MarshalIndent(m.Cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, b, 0o600)
}

func (m *Manager) CheckPassword(pwd string) bool {
	c := m.Get()
	if pwd == "" {
		return false
	}
	var got string
	if c.HashAlgo == "pbkdf2" {
		got = util.HashPassword(pwd, c.Salt)
	} else {
		got = util.Hash(pwd, c.Salt) // 旧版算法,登录成功后自动升级
	}
	return util.ConstantTimeEq(got, c.PasswordHash)
}

// UpgradePasswordHash 将旧版 sha256 口令哈希透明升级为 PBKDF2(不轮换会话密钥)
func (m *Manager) UpgradePasswordHash(pwd string) {
	c := m.Get()
	if c.HashAlgo == "pbkdf2" {
		return
	}
	_ = m.Update(func(cfg *Config) {
		cfg.HashAlgo = "pbkdf2"
		cfg.Salt = util.RandomString(16)
		cfg.PasswordHash = util.HashPassword(pwd, cfg.Salt)
	})
}

// SetPassword 修改密码:KDF 升级为 PBKDF2 并轮换 JWT 密钥(所有旧令牌立即失效)
func (m *Manager) SetPassword(newPwd string) error {
	return m.Update(func(c *Config) {
		c.Salt = util.RandomString(16)
		c.HashAlgo = "pbkdf2"
		c.PasswordHash = util.HashPassword(newPwd, c.Salt)
		c.JWTSecret = util.RandomString(32) // 吊销所有已签发令牌
	})
}

func (m *Manager) ResetPassword() (string, error) {
	if m == nil {
		return "", errors.New("config not loaded")
	}
	pwd := util.RandomString(12)
	if err := m.SetPassword(pwd); err != nil {
		return "", err
	}
	return pwd, nil
}
