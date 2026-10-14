package terminalx

import (
	"net/url"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"kunpanel/internal/logsx"
)

// wsMessage 终端消息:{"t":"i","d":base64} 输入 / {"t":"o","d":base64} 输出 / {"t":"resize","cols":n,"rows":n}
type wsMessage struct {
	T    string `json:"t"`
	D    string `json:"d"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// 同源校验:携带 Origin 头的浏览器请求必须同源(防跨站终端劫持);
	// 无 Origin 的非浏览器客户端(ssh 终端工具)放行,连接本身仍需一次性票据
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
}

// 一次性连接票据:面板签发(30 秒有效、用后即焚),WS 地址不再携带长期令牌
var (
	ticketMu  sync.Mutex
	ticketMap = map[string]time.Time{}
)

// IssueTicket 签发一次性连接票据
func IssueTicket() string {
	ticketMu.Lock()
	defer ticketMu.Unlock()
	now := time.Now()
	for k, exp := range ticketMap {
		if now.After(exp) {
			delete(ticketMap, k)
		}
	}
	tk := make([]byte, 16)
	if _, err := rand.Read(tk); err != nil {
		return ""
	}
	ticket := fmt.Sprintf("%x", tk)
	ticketMap[ticket] = now.Add(30 * time.Second)
	return ticket
}

// ConsumeTicket 校验并销毁票据(单次有效)
func ConsumeTicket(tk string) bool {
	ticketMu.Lock()
	defer ticketMu.Unlock()
	exp, ok := ticketMap[tk]
	if !ok {
		return false
	}
	delete(ticketMap, tk)
	return time.Now().Before(exp)
}

// Handle WebSocket 终端入口。Linux/macOS 使用 PTY;Windows 使用管道 shell(功能受限)。
// 鉴权由调用方(server.handleTerminal)完成;票据是一次性的,此处不得重复校验,
// 否则票据被二次消费必然 401(终端无法连接的根因)。
func Handle(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20) // 单条消息上限 1MB
	logsx.AppendOp(r.RemoteAddr, "打开", "终端会话", "", 200)

	if runtime.GOOS == "windows" {
		handlePiped(conn)
		return
	}

	// 容器终端:?container=kun-xxx 进入指定容器(仅允许面板管理的 kun-* 应用容器)
	container := r.URL.Query().Get("container")
	if container != "" {
		if !isValidContainer(container) {
			writeMsg(conn, wsMessage{T: "o", D: base64.StdEncoding.EncodeToString([]byte("非法容器名\r\n"))})
			return
		}
		cmd := exec.Command("docker", "exec", "-it", container, "bash")
		cmd.Env = append(os.Environ(), "TERM=xterm-256color")
		ptmx, err := pty.Start(cmd)
		if err != nil {
			// bash 不存在则降级 sh
			cmd = exec.Command("docker", "exec", "-it", container, "sh")
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			ptmx, err = pty.Start(cmd)
			if err != nil {
				writeMsg(conn, wsMessage{T: "o", D: base64.StdEncoding.EncodeToString([]byte("无法进入容器终端: " + err.Error() + "\r\n"))})
				return
			}
		}
		sess := &terminalSession{conn: conn, ptmx: ptmx, cmd: cmd}
		sess.pump()
		return
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	if _, err := exec.LookPath(shell); err != nil {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	// 与 SSH 登录一致:从当前用户家目录开始,而不是面板进程的工作目录
	if u, uerr := user.Current(); uerr == nil && u.HomeDir != "" {
		cmd.Dir = u.HomeDir
	}
	if _, herr := os.Stat(cmd.Dir); herr != nil {
		cmd.Dir = "/"
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		writeMsg(conn, wsMessage{T: "o", D: base64.StdEncoding.EncodeToString([]byte("无法启动终端: " + err.Error()))})
		return
	}
	defer ptmx.Close()
	defer func() { _ = cmd.Process.Kill() }()

	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			_ = ptmx.Close()
			_ = cmd.Process.Kill()
			_ = conn.Close()
		})
	}
	// PTY -> WebSocket
	go func() {
		defer closeAll()
		buf := make([]byte, 8192)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if err := writeMsg(conn, wsMessage{T: "o", D: base64.StdEncoding.EncodeToString(buf[:n])}); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// WebSocket -> PTY
	for {
		var m wsMessage
		if err := conn.ReadJSON(&m); err != nil {
			closeAll()
			return
		}
		switch m.T {
		case "i":
			b, _ := base64.StdEncoding.DecodeString(m.D)
			if _, err := ptmx.Write(b); err != nil {
				closeAll()
				return
			}
		case "resize":
			if m.Cols > 0 && m.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(m.Cols), Rows: uint16(m.Rows)})
			}
		}
	}
}

// handlePiped Windows 降级方案:PowerShell 管道(非交互 TTY,支持基本命令)
func handlePiped(conn *websocket.Conn) {
	shell := "powershell"
	if _, err := exec.LookPath("powershell"); err != nil {
		shell = "cmd"
	}
	cmd := exec.Command(shell)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		writeMsg(conn, wsMessage{T: "o", D: base64.StdEncoding.EncodeToString([]byte("无法启动 PowerShell: " + err.Error()))})
		return
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
	}()

	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if writeMsg(conn, wsMessage{T: "o", D: base64.StdEncoding.EncodeToString(buf[:n])}) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		var m wsMessage
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		if m.T == "i" {
			b, _ := base64.StdEncoding.DecodeString(m.D)
			if _, err := stdin.Write(b); err != nil {
				return
			}
		}
	}
}

func writeMsg(conn *websocket.Conn, m wsMessage) error {
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteJSON(m)
}

var _ = fmt.Sprintf // 占位避免误删 import

// isValidContainer 仅允许面板管理的应用容器名(kun- 前缀 + 安全字符)
func isValidContainer(name string) bool {
	if !strings.HasPrefix(name, "kun-") || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// terminalSession 封装一次 PTY 会话的读写泵
type terminalSession struct {
	conn *websocket.Conn
	ptmx *os.File
	cmd  *exec.Cmd
}

func (t *terminalSession) pump() {
	defer t.ptmx.Close()
	defer t.cmd.Process.Kill()
	defer t.conn.Close()
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := t.ptmx.Read(buf)
			if n > 0 {
				if werr := writeMsg(t.conn, wsMessage{T: "o", D: base64.StdEncoding.EncodeToString(buf[:n])}); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		var m wsMessage
		if err := t.conn.ReadJSON(&m); err != nil {
			return
		}
		switch m.T {
		case "i":
			b, _ := base64.StdEncoding.DecodeString(m.D)
			if _, werr := t.ptmx.Write(b); werr != nil {
				return
			}
		case "resize":
			// 容器终端尺寸由内层 shell 自适应,忽略
		}
	}
}
