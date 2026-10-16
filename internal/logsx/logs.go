package logsx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/util"
)

// Entry 一条日志
type Entry struct {
	Time   string `json:"time"`
	Action string `json:"action"`
	IP     string `json:"ip"`
	Detail string `json:"detail"`
	OK     bool   `json:"ok"`
}

var mu sync.Mutex

func logsDir() string { return filepath.Join(config.DataDir(), "logs") }

func file(kind string) string {
	return filepath.Join(logsDir(), kind+".log")
}

func appendEntry(kind string, e Entry) {
	mu.Lock()
	defer mu.Unlock()
	_ = os.MkdirAll(logsDir(), 0o755)
	if e.Time == "" {
		e.Time = time.Now().Format("2006-01-02 15:04:05")
	}
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(file(kind), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, string(b))
	// 超过 5MB 截断保留一半(按行边界切:任意字节切会留半行损坏 JSON,
	// 首条记录永远解析不出来)
	if fi, _ := f.Stat(); fi != nil && fi.Size() > 5<<20 {
		data, _ := os.ReadFile(file(kind))
		cut := len(data) / 2
		for cut < len(data) && data[cut] != '\n' {
			cut++
		}
		if cut < len(data) {
			cut++ // 跳过换行符
		}
		_ = util.AtomicWrite(file(kind), data[cut:], 0o600)
	}
}

// AppendLogin 记录登录相关日志(ok 表示成功)
func AppendLogin(action, ip, detail string) {
	appendEntry("login", Entry{Action: action, IP: ip, Detail: detail, OK: action == "login"})
}

// AppendLoginResult 登录成功/失败
func AppendLoginResult(ip string, ok bool, detail string) {
	appendEntry("login", Entry{Action: "login", IP: ip, Detail: detail, OK: ok})
}

// AppendOp 记录操作日志
func AppendOp(ip, method, path, detail string, status int) {
	appendEntry("operation", Entry{Action: method + " " + path, IP: ip, Detail: detail, OK: status < 400})
}

// Clear 清空指定类别日志(login / operation)
func Clear(kind string) error {
	if kind != "login" && kind != "operation" {
		return fmt.Errorf("未知日志类型")
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(logsDir(), 0o755); err != nil {
		return err
	}
	return util.AtomicWrite(file(kind), []byte{}, 0o600)
}

// Read 读取最近 n 条日志(倒序)
func Read(kind string, n int) ([]Entry, error) {
	if n <= 0 || n > 1000 {
		n = 200
	}
	mu.Lock()
	defer mu.Unlock()
	b, err := os.ReadFile(file(kind))
	if err != nil {
		return []Entry{}, nil
	}
	var out []Entry
	for _, line := range splitLines(b) {
		var e Entry
		if err := json.Unmarshal(line, &e); err == nil {
			out = append(out, e)
		}
	}
	// 倒序取最近 n 条
	res := make([]Entry, 0, n)
	for i := len(out) - 1; i >= 0 && len(res) < n; i-- {
		res = append(res, out[i])
	}
	return res, nil
}

func splitLines(b []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				lines = append(lines, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, b[start:])
	}
	return lines
}
