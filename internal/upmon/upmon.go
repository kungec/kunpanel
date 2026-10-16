// Package upmon 网站可用性拨测:周期探测 HTTP/TCP 目标,记录历史,状态变化时告警
package upmon

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/util"
)

// Monitor 拨测目标
type Monitor struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // http / tcp
	Target  string `json:"target"` // http://... 或 host:port
	Enabled bool   `json:"enabled"`
	Created string `json:"created"`
}

// Sample 一次探测结果
type Sample struct {
	TS int64  `json:"ts"` // unix 秒
	Up bool   `json:"up"`
	MS int64  `json:"ms"` // 耗时毫秒
	Code int  `json:"code,omitempty"` // HTTP 状态码
	Err string `json:"err,omitempty"`
}

// State 拨测目标实时状态
type State struct {
	Monitor
	Up        bool     `json:"up"`
	LastMS    int64    `json:"last_ms"`
	LastCheck string   `json:"last_check"`
	UptimePct float64  `json:"uptime_pct"` // 24h 可用率
	AvgMS     int64    `json:"avg_ms"`     // 24h 平均延迟
	History   []Sample `json:"history"`    // 最近 24h
}

const (
	maxHistory  = 1440 // 24h * 每分钟 1 次
	failThreshold = 2 // 连续失败 N 次才判为宕机(防抖)
)

var (
	mu        sync.Mutex
	monitors  []Monitor
	history   = map[string][]Sample{}   // id -> samples
	fails     = map[string]int{}        // id -> 连续失败次数
	wasUp     = map[string]bool{}       // id -> 上次状态
	notified  = map[string]bool{}       // id -> 宕机事件是否已通知
	histLoaded bool                     // history 只从盘上加载一次,别在每次 List 轮询重读
)

func dataDir() string  { return filepath.Join(config.DataDir(), "upmon") }
func storeFile() string { return filepath.Join(dataDir(), "monitors.json") }
func histFile() string  { return filepath.Join(dataDir(), "history.json") }

func load() {
	if monitors == nil {
		if b, err := os.ReadFile(storeFile()); err == nil {
			_ = json.Unmarshal(b, &monitors)
		}
	}
	if history == nil {
		history = map[string][]Sample{}
	}
	if !histLoaded {
		histLoaded = true
		if b, err := os.ReadFile(histFile()); err == nil {
			_ = json.Unmarshal(b, &history)
		}
	}
}

func save() error {
	_ = os.MkdirAll(dataDir(), 0o755)
	b, _ := json.MarshalIndent(monitors, "", "  ")
	if err := util.AtomicWrite(storeFile(), b, 0o600); err != nil {
		return err
	}
	hb, _ := json.Marshal(history)
	return util.AtomicWrite(histFile(), hb, 0o600)
}

// Notifier 由 alertx 注入:状态变化时推送(id, name, up, detail)
var Notifier func(name, target string, up bool, detail string)

// List 全部拨测目标及状态
func List() []State {
	mu.Lock()
	defer mu.Unlock()
	load()
	now := time.Now().Unix()
	out := make([]State, 0, len(monitors))
	for _, m := range monitors {
		st := State{Monitor: m, Up: true, History: []Sample{}}
		h := prune(history[m.ID], now)
		history[m.ID] = h
		st.History = h
		if len(h) > 0 {
			last := h[len(h)-1]
			st.Up = last.Up
			st.LastMS = last.MS
			st.LastCheck = time.Unix(last.TS, 0).Format("15:04:05")
			var upCount, totalMS int64
			for _, s := range h {
				if s.Up {
					upCount++
					totalMS += s.MS
				}
			}
			st.UptimePct = float64(upCount) * 100 / float64(len(h))
			if upCount > 0 {
				st.AvgMS = totalMS / upCount
			}
		}
		// 展示防抖后的状态(连续失败不足阈值仍视为正常)
		if deb, ok := wasUp[m.ID]; ok && m.Enabled {
			st.Up = deb
		}
		if !m.Enabled {
			st.Up = true // 未启用不显示宕机
			st.UptimePct = 0
		}
		out = append(out, st)
	}
	return out
}

func prune(h []Sample, now int64) []Sample {
	cut := now - 86400
	i := 0
	for i < len(h) && h[i].TS < cut {
		i++
	}
	if i > 0 {
		h = append([]Sample{}, h[i:]...)
	}
	return h
}

// Add 新增拨测目标
func Add(name, kind, target string) (*Monitor, error) {
	name = strings.TrimSpace(name)
	target = strings.TrimSpace(target)
	if name == "" || len(name) > 64 {
		return nil, fmt.Errorf("名称不能为空且不超过 64 字")
	}
	if err := validateTarget(kind, target); err != nil {
		return nil, err
	}
	mu.Lock()
	defer mu.Unlock()
	load()
	for _, m := range monitors {
		if m.Target == target && m.Kind == kind {
			return nil, fmt.Errorf("该目标已在监控中: %s", m.Name)
		}
	}
	m := &Monitor{
		ID:      fmt.Sprintf("um_%d", time.Now().UnixNano()/1000000),
		Name:    name, Kind: kind, Target: target, Enabled: true,
		Created: time.Now().Format("2006-01-02 15:04:05"),
	}
	monitors = append(monitors, *m)
	if err := save(); err != nil {
		return nil, err
	}
	go probeOne(*m) // 立即探测一次
	return m, nil
}

func validateTarget(kind, target string) error {
	switch kind {
	case "http":
		if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			return fmt.Errorf("HTTP 目标必须以 http:// 或 https:// 开头")
		}
		if len(target) > 500 {
			return fmt.Errorf("URL 过长")
		}
		return nil
	case "tcp":
		host, port, err := net.SplitHostPort(target)
		if err != nil || host == "" || port == "" {
			return fmt.Errorf("TCP 目标格式应为 host:port")
		}
		for _, c := range target {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == ':' || c == '-' {
				continue
			}
			return fmt.Errorf("TCP 目标包含非法字符")
		}
		p := 0
		for _, c := range port {
			if c < '0' || c > '9' {
				return fmt.Errorf("端口必须为数字")
			}
			p = p*10 + int(c-'0')
		}
		if p < 1 || p > 65535 {
			return fmt.Errorf("端口超出范围")
		}
		return nil
	default:
		return fmt.Errorf("kind 仅支持 http / tcp")
	}
}

// Delete 删除拨测目标
func Delete(id string) error {
	mu.Lock()
	defer mu.Unlock()
	load()
	found := false
	var kept []Monitor
	for _, m := range monitors {
		if m.ID == id {
			found = true
			continue
		}
		kept = append(kept, m)
	}
	if !found {
		return fmt.Errorf("拨测目标不存在")
	}
	monitors = kept
	delete(history, id)
	// 同步清运行态:残留的 fails/wasUp/notified 会让同名新目标立刻误报/漏报
	delete(fails, id)
	delete(wasUp, id)
	delete(notified, id)
	return save()
}

// Toggle 启用/停用
func Toggle(id string, enabled bool) error {
	mu.Lock()
	defer mu.Unlock()
	load()
	for i := range monitors {
		if monitors[i].ID == id {
			monitors[i].Enabled = enabled
			// 重新启用时重置连续失败/通知状态,从当前真实探测结果重新计数
			if enabled {
				delete(fails, id)
				delete(wasUp, id)
				delete(notified, id)
			}
			return save()
		}
	}
	return fmt.Errorf("拨测目标不存在")
}

// probeOne 探测单个目标并记录
func probeOne(m Monitor) (Sample, error) {
	s := Sample{TS: time.Now().Unix()}
	switch m.Kind {
	case "tcp":
		host, port, _ := net.SplitHostPort(m.Target)
		start := time.Now()
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 8*time.Second)
		s.MS = time.Since(start).Milliseconds()
		if err != nil {
			s.Err = shortErr(err)
		} else {
			conn.Close()
			s.Up = true
		}
	default: // http
		client := &http.Client{
			Timeout: 12 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // 自签证书也算通
			},
		}
		start := time.Now()
		resp, err := client.Get(m.Target)
		s.MS = time.Since(start).Milliseconds()
		if err != nil {
			s.Err = shortErr(err)
		} else {
			resp.Body.Close()
			s.Code = resp.StatusCode
			// 2xx/3xx/401 视为存活(401 说明服务在响应)
			if resp.StatusCode < 500 {
				s.Up = true
			} else {
				s.Err = "HTTP " + fmt.Sprint(resp.StatusCode)
			}
		}
	}

	mu.Lock()
	h := history[m.ID]
	h = append(h, s)
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	history[m.ID] = h
	if m.Enabled {
		if s.Up {
			fails[m.ID] = 0
		} else {
			fails[m.ID]++
		}
		wasUpNow := s.Up || fails[m.ID] < failThreshold
		prev, hadPrev := wasUp[m.ID]
		wasUp[m.ID] = wasUpNow
		if hadPrev && prev != wasUpNow {
			// 状态翻转才通知一次(恢复时清标志)
			if !wasUpNow && !notified[m.ID] {
				notified[m.ID] = true
				if Notifier != nil {
					detail := s.Err
					if detail == "" {
						detail = "HTTP " + fmt.Sprint(s.Code)
					}
					go Notifier(m.Name, m.Target, false, detail)
				}
			} else if wasUpNow {
				notified[m.ID] = false
				if Notifier != nil {
					go Notifier(m.Name, m.Target, true, fmt.Sprintf("延迟 %dms", s.MS))
				}
			}
		}
		if !hadPrev {
			wasUp[m.ID] = wasUpNow
		}
	}
	_ = save()
	mu.Unlock()
	return s, nil
}

func shortErr(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i >= 0 && len(msg) > 60 {
		// 保留最后一段关键信息
		parts := strings.Split(msg, ": ")
		msg = parts[len(parts)-1]
	}
	if len(msg) > 80 {
		msg = msg[:80]
	}
	return msg
}

// Start 启动拨测循环(每 60 秒一轮)
func Start(stop <-chan struct{}) {
	go func() {
		// 启动 30 秒后先跑一轮
		select {
		case <-stop:
			return
		case <-time.After(30 * time.Second):
		}
		runAll()
		tick := time.NewTicker(60 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				runAll()
			}
		}
	}()
}

func runAll() {
	mu.Lock()
	load()
	var list []Monitor
	for _, m := range monitors {
		if m.Enabled {
			list = append(list, m)
		}
	}
	mu.Unlock()
	for _, m := range list {
		mm := m
		go func() {
			_, _ = probeOne(mm)
		}()
	}
}
