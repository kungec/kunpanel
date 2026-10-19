package system

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"

	"kunpanel/internal/util"
)

type DiskInfo struct {
	Path   string  `json:"path"`
	Total  uint64  `json:"total"`
	Used   uint64  `json:"used"`
	Free   uint64  `json:"free"`
	UsageP float64 `json:"usage_percent"`
}

type Snapshot struct {
	Hostname   string     `json:"hostname"`
	OS         string     `json:"os"`       // linux / windows / darwin
	Platform   string     `json:"platform"` // ubuntu / centos ...
	PlatformV  string     `json:"platform_ver"`
	Kernel     string     `json:"kernel"`
	Arch       string     `json:"arch"`
	CPUModel   string     `json:"cpu_model"`
	CPUCores   int        `json:"cpu_cores"`
	CPUUsageP  float64    `json:"cpu_percent"`
	Load1      float64    `json:"load1"`
	Load5      float64    `json:"load5"`
	Load15     float64    `json:"load15"`
	MemTotal   uint64     `json:"mem_total"`
	MemUsed    uint64     `json:"mem_used"`
	MemUsageP  float64    `json:"mem_percent"`
	SwapTotal  uint64     `json:"swap_total"`
	SwapUsed   uint64     `json:"swap_used"`
	SwapUsageP float64    `json:"swap_percent"`
	Uptime     uint64     `json:"uptime"`
	Disks      []DiskInfo `json:"disks"`
	NetRx      uint64     `json:"net_rx_total"`
	NetTx      uint64     `json:"net_tx_total"`
	NetRxSpeed float64    `json:"net_rx_speed"` // bytes/s
	NetTxSpeed float64    `json:"net_tx_speed"`
	DiskRSpeed float64    `json:"disk_r_speed"` // 磁盘读速率 bytes/s
	DiskWSpeed float64    `json:"disk_w_speed"` // 磁盘写速率 bytes/s
	IP         string     `json:"ip"`
	PublicIP   string     `json:"public_ip"`
	BootTime   uint64     `json:"boot_time"`
}

func rootPath() string {
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	return "/"
}

var (
	mu      sync.Mutex
	prevNet gnet.IOCountersStat
	prevAt  time.Time

	snapMu   sync.RWMutex
	snapshot Snapshot
	// 静态信息只采一次,重分区表每 30s 刷新一次
	staticOnce   sync.Once
	staticHost   host.InfoStat
	staticCPU    []cpu.InfoStat
	staticCores  int
	partDisks    []DiskInfo
	sampleRounds int
)

// StartSampler 启动后台采样循环(每 2 秒一轮)。
// 此前 Get() 每次调用都阻塞 300ms 做 CPU 采样,前端每 5s 轮询 dashboard
// 时每次请求都产生一次阻塞采样,并发轮询还会互相干扰网速差值基线。
// 改为后台单点采样 + 原子读缓存:API 读缓存零阻塞,网速窗口稳定。
func StartSampler(stop <-chan struct{}) {
	startPublicIPFetcher(stop)
	go func() {
		sample()
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				sample()
			}
		}
	}()
}

func sample() {
	s := collect()
	collectDiskIO(&s)
	snapMu.Lock()
	snapshot = s
	snapMu.Unlock()
}

// Get 返回最近一次采样结果(后台每 2 秒刷新,无阻塞)
func Get() Snapshot {
	snapMu.RLock()
	defer snapMu.RUnlock()
	return snapshot
}

// ---------- 磁盘 IO 速率(基于 /proc/diskstats 扇区差值) ----------
var (
	ioMu       sync.Mutex
	ioPrevR    uint64
	ioPrevW    uint64
	ioPrevTime time.Time
)

// readDiskIO 汇总物理盘(/sys/block 顶层设备,过滤 loop/ram/sr/dm)读写扇区数
func readDiskIO() (r, w uint64) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return 0, 0
	}
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return 0, 0
	}
	secR := map[string]uint64{}
	secW := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}
		var rs, ws uint64
		fmt.Sscanf(fields[5], "%d", &rs)
		fmt.Sscanf(fields[9], "%d", &ws)
		secR[fields[2]] = rs * 512
		secW[fields[2]] = ws * 512
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") ||
			strings.HasPrefix(name, "sr") || strings.HasPrefix(name, "dm-") {
			continue
		}
		r += secR[name]
		w += secW[name]
	}
	return r, w
}

func collectDiskIO(s *Snapshot) {
	r, w := readDiskIO()
	now := time.Now()
	ioMu.Lock()
	defer ioMu.Unlock()
	if !ioPrevTime.IsZero() {
		if el := now.Sub(ioPrevTime).Seconds(); el > 0.5 {
			if r >= ioPrevR {
				s.DiskRSpeed = float64(r-ioPrevR) / el
			}
			if w >= ioPrevW {
				s.DiskWSpeed = float64(w-ioPrevW) / el
			}
		}
	}
	ioPrevR, ioPrevW, ioPrevTime = r, w, now
}

func collect() Snapshot {
	s := Snapshot{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		IP:       util.LocalIP(),
		PublicIP: PublicIPValue(),
	}

	// 静态信息(主机名/内核/CPU 型号)仅采集一次
	staticOnce.Do(func() {
		if hi, err := host.Info(); err == nil {
			staticHost = *hi
		}
		if ci, err := cpu.Info(); err == nil {
			staticCPU = ci
		}
		if n, err := cpu.Counts(true); err == nil {
			staticCores = n
		}
	})
	s.Hostname, s.Platform, s.PlatformV = staticHost.Hostname, staticHost.Platform, staticHost.PlatformVersion
	s.Kernel, s.Uptime, s.BootTime = staticHost.KernelVersion, staticHost.Uptime, staticHost.BootTime
	if len(staticCPU) > 0 {
		s.CPUModel = staticCPU[0].ModelName
	}
	s.CPUCores = staticCores

	// cpu.Percent(0):以上一次调用为起点的窗口,不阻塞
	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		s.CPUUsageP = round1(pct[0])
	}
	if la, err := load.Avg(); err == nil {
		s.Load1, s.Load5, s.Load15 = round2(la.Load1), round2(la.Load5), round2(la.Load15)
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemTotal = vm.Total
		s.MemUsed = vm.Used
		s.MemUsageP = round1(vm.UsedPercent)
	}
	if sw, err := mem.SwapMemory(); err == nil {
		s.SwapTotal = sw.Total
		s.SwapUsed = sw.Used
		s.SwapUsageP = round1(sw.UsedPercent)
	}

	// 根分区每轮采,其余挂载点每 30 秒采一次
	sampleRounds++
	if sampleRounds%15 == 1 || partDisks == nil {
		partDisks = collectDisks()
	}
	if du, err := disk.Usage(rootPath()); err == nil {
		s.Disks = append(s.Disks, DiskInfo{
			Path: rootPath(), Total: du.Total, Used: du.Used, Free: du.Free,
			UsageP: round1(du.UsedPercent),
		})
	}
	for _, d := range partDisks {
		if d.Path != rootPath() {
			s.Disks = append(s.Disks, d)
		}
	}

	// 网络速率(与上次采样做差值)
	if io, err := gnet.IOCounters(false); err == nil && len(io) > 0 {
		s.NetRx = io[0].BytesRecv
		s.NetTx = io[0].BytesSent
		mu.Lock()
		now := time.Now()
		if prevAt.IsZero() {
			s.NetRxSpeed, s.NetTxSpeed = 0, 0
		} else {
			dt := now.Sub(prevAt).Seconds()
			if dt > 0.2 && io[0].BytesRecv >= prevNet.BytesRecv {
				s.NetRxSpeed = float64(io[0].BytesRecv-prevNet.BytesRecv) / dt
				s.NetTxSpeed = float64(io[0].BytesSent-prevNet.BytesSent) / dt
			}
		}
		prevNet = io[0]
		prevAt = now
		mu.Unlock()
	}
	return s
}

func collectDisks() []DiskInfo {
	var out []DiskInfo
	if parts, err := disk.Partitions(false); err == nil {
		seen := map[string]bool{}
		for _, p := range parts {
			if seen[p.Mountpoint] {
				continue
			}
			seen[p.Mountpoint] = true
			if du, err := disk.Usage(p.Mountpoint); err == nil && du.Total > 1<<30 {
				out = append(out, DiskInfo{
					Path: p.Mountpoint, Total: du.Total, Used: du.Used, Free: du.Free,
					UsageP: round1(du.UsedPercent),
				})
				if len(out) >= 8 {
					break
				}
			}
		}
	}
	return out
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
