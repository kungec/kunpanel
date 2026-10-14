// Package alertx 监控告警:CPU/内存超阈值时向 Webhook 推送通知,恢复时发送恢复消息。
package alertx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/notifyx"
	"kunpanel/internal/system"
	"kunpanel/internal/util"
)

// Settings 告警配置
type Settings struct {
	Enabled   bool   `json:"enabled"`
	Webhook   string `json:"webhook"`
	Channel   string `json:"channel"` // generic / dingtalk / feishu
	CpuThres  int    `json:"cpu_thres"`  // CPU 阈值 %
	MemThres  int    `json:"mem_thres"`  // 内存阈值 %
	DiskThres int    `json:"disk_thres"` // 磁盘阈值 %
	Duration  int    `json:"duration"`   // 持续秒数(连续超阈值多久才告警)
	SMTPSettings
}

var (
	mu      sync.Mutex
	over    = map[string]int{}  // 指标 -> 连续超阈值次数
	alerted = map[string]bool{} // 指标 -> 本次 episode 是否已告警
)

func confPath() string { return filepath.Join(config.DataDir(), "alert.json") }

// Get 读取配置(无文件返回默认)
func Get() Settings {
	b, err := os.ReadFile(confPath())
	if err == nil {
		var s Settings
		if json.Unmarshal(b, &s) == nil {
			if s.CpuThres <= 0 {
				s.CpuThres = 90
			}
			if s.MemThres <= 0 {
				s.MemThres = 90
			}
			if s.DiskThres <= 0 {
				s.DiskThres = 90
			}
			if s.Duration <= 0 {
				s.Duration = 120
			}
			return s
		}
	}
	return Settings{Enabled: false, CpuThres: 90, MemThres: 90, Duration: 120}
}

// Save 保存配置
func Save(s Settings) error {
	if s.Webhook != "" && !strings.HasPrefix(s.Webhook, "http://") && !strings.HasPrefix(s.Webhook, "https://") {
		return fmt.Errorf("Webhook 地址必须以 http:// 或 https:// 开头")
	}
	if s.DiskThres < 1 || s.DiskThres > 100 {
		s.DiskThres = 90
	}
	if s.CpuThres < 1 || s.CpuThres > 100 {
		return fmt.Errorf("CPU 阈值应在 1-100 之间")
	}
	if s.MemThres < 1 || s.MemThres > 100 {
		return fmt.Errorf("内存阈值应在 1-100 之间")
	}
	if s.Duration < 30 {
		s.Duration = 30
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return util.AtomicWrite(confPath(), b, 0o600)
}

// Start 启动后台检测循环(每 30 秒)
func Start(stop <-chan struct{}) {
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				check()
			}
		}
	}()
}

func check() {
	s := Get()
	if !s.Enabled {
		return
	}
	snap := system.Get()
	maxDisk := 0.0
	for _, d := range snap.Disks {
		if d.UsageP > maxDisk {
			maxDisk = d.UsageP
		}
	}
	metrics := []struct {
		name  string
		value float64
		thres float64
	}{
		{"CPU", snap.CPUUsageP, float64(s.CpuThres)},
		{"内存", snap.MemUsageP, float64(s.MemThres)},
		{"磁盘", maxDisk, float64(s.DiskThres)},
	}
	mu.Lock()
	defer mu.Unlock()
	for _, m := range metrics {
		key := m.name
		if m.value >= m.thres {
			over[key]++
			need := s.Duration/30
			if need < 1 {
				need = 1
			}
			if over[key] >= int(need) && !alerted[key] {
				alerted[key] = true
				text := fmt.Sprintf("【告警】%s 使用率 %.1f%% 超过阈值 %.0f%%", m.name, m.value, m.thres)
				notifyx.Add("alert", text, "主机: "+util.LocalIP())
				if s.Webhook != "" {
					go notify(s.Webhook, "alert", m.name, m.value, m.thres)
				}
			}
		} else {
			over[key] = 0
			if alerted[key] {
				alerted[key] = false
				text := fmt.Sprintf("【恢复】%s 使用率 %.1f%% 已回落至阈值 %.0f%% 以下", m.name, m.value, m.thres)
				notifyx.Add("alert", text, "主机: "+util.LocalIP())
				if s.Webhook != "" {
					go notify(s.Webhook, "recover", m.name, m.value, m.thres)
				}
			}
		}
	}
}

func notify(webhook, typ, metric string, value, thres float64) {
	text := map[bool]string{
		true:  fmt.Sprintf("【告警】%s 使用率 %.1f%% 超过阈值 %.0f%%", metric, value, thres),
		false: fmt.Sprintf("【恢复】%s 使用率 %.1f%% 已回落至阈值 %.0f%% 以下", metric, value, thres),
	}[typ == "alert"]
	Push(webhook, map[string]any{
		"type": typ, "metric": metric, "value": value, "threshold": thres,
		"host": util.LocalIP(), "time": time.Now().Format("2006-01-02 15:04:05"), "text": text,
	})
}

// Push 向指定 Webhook 推送任意 JSON 负载(供证书到期等其他模块复用)
func Push(webhook string, payload map[string]any) error {
	// 按渠道适配请求体(钉钉/飞书机器人要求固定格式)
	if ch := Get().Channel; ch != "" && ch != "generic" {
		text, _ := payload["text"].(string)
		if ch == "dingtalk" {
			payload = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
		} else if ch == "feishu" {
			payload = map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
		}
	}
	payload["host"] = util.LocalIP()
	payload["time"] = time.Now().Format("2006-01-02 15:04:05")
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", webhook, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("Webhook 地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("对端返回 %s", resp.Status)
	}
	return nil
}

// Test 发送测试通知
func Test(webhook string) error {
	payload, _ := json.Marshal(map[string]any{
		"type": "test",
		"text": "【测试】坤面板告警通知连通性测试",
		"host": util.LocalIP(),
		"time": time.Now().Format("2006-01-02 15:04:05"),
	})
	req, err := http.NewRequest("POST", webhook, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("Webhook 地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("对端返回 %s", resp.Status)
	}
	return nil
}
