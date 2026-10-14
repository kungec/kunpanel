// Package monhist 面板监控历史:每分钟采样 CPU/内存/网络,保留 24h,供图表展示
package monhist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/system"
)

// Point 一个采样点
type Point struct {
	Time   string  `json:"time"`
	CPU    float64 `json:"cpu"`
	Mem    float64 `json:"mem"`
	NetRx  float64 `json:"net_rx"`
	NetTx  float64 `json:"net_tx"`
	DiskR  float64 `json:"disk_r"`
	DiskW  float64 `json:"disk_w"`
	Load1  float64 `json:"load1"`
}

const maxPoints = 1440 // 24h × 60/min

var (
	mu       sync.Mutex
	points   []Point
	started  bool
)

func dataFile() string {
	return filepath.Join(config.DataDir(), "monhist.json")
}

// Start 启动采样循环(每分钟一次,首次立即)
func Start(stop <-chan struct{}) {
	mu.Lock()
	if started {
		mu.Unlock()
		return
	}
	started = true
	loadFromDisk()
	mu.Unlock()

	sample()
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				saveToDisk()
				return
			case <-tick.C:
				sample()
			}
		}
	}()
}

func sample() {
	snap := system.Get()
	p := Point{
		Time:  time.Now().Format("15:04"),
		CPU:   snap.CPUUsageP,
		Mem:   snap.MemUsageP,
		NetRx: snap.NetRxSpeed,
		NetTx: snap.NetTxSpeed,
		DiskR: snap.DiskRSpeed,
		DiskW: snap.DiskWSpeed,
		Load1: snap.Load1,
	}
	mu.Lock()
	points = append(points, p)
	if len(points) > maxPoints {
		points = points[len(points)-maxPoints:]
	}
	mu.Unlock()
}

// History 返回最近 n 个采样点
func History(n int) []Point {
	mu.Lock()
	defer mu.Unlock()
	if n <= 0 || n > len(points) {
		n = len(points)
	}
	out := make([]Point, n)
	copy(out, points[len(points)-n:])
	return out
}

func saveToDisk() {
	mu.Lock()
	defer mu.Unlock()
	if len(points) == 0 {
		return
	}
	b, _ := json.Marshal(points)
	_ = os.WriteFile(dataFile(), b, 0o600)
}

func loadFromDisk() {
	b, err := os.ReadFile(dataFile())
	if err != nil {
		return
	}
	var saved []Point
	if json.Unmarshal(b, &saved) == nil && len(saved) > 0 {
		// 只保留 24h 内的
		cut := time.Now().Add(-24 * time.Hour).Format("2006-01-02 15:04")
		var valid []Point
		for _, p := range saved {
			if p.Time >= cut {
				valid = append(valid, p)
			}
		}
		points = valid
	}
}
