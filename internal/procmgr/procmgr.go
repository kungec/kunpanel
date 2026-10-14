// Package procmgr 进程管理器:进程列表 / CPU 内存排序 / 终止进程
package procmgr

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

// Proc 进程信息
type Proc struct {
	PID      int32   `json:"pid"`
	PPID     int32   `json:"ppid"`
	Name     string  `json:"name"`
	User     string  `json:"user"`
	CPUPercent float64 `json:"cpu_percent"`
	MemPercent float32 `json:"mem_percent"`
	MemRSS   uint64  `json:"mem_rss"` // bytes
	Status   string  `json:"status"`
	Threads  int32   `json:"threads"`
	Cmdline  string  `json:"cmdline"`
	CreateTime int64 `json:"create_time"` // ms
}

var (
	listMu    sync.Mutex
	listCache []Proc
	listAt    time.Time
)

// List 列出全部进程;sortBy: cpu / mem / pid
// 全量进程扫描开销较大(每进程多次系统调用),而进程页每 5 秒自动刷新,
// 加 3 秒 TTL 缓存,排序/筛选始终在内存中进行
func List(sortBy string, limit int) ([]Proc, error) {
	listMu.Lock()
	var cached []Proc
	fresh := listCache != nil && time.Since(listAt) < 3*time.Second
	if fresh {
		cached = listCache
	}
	listMu.Unlock()
	if !fresh {
		var err error
		cached, err = scan()
		if err != nil {
			return nil, err
		}
		listMu.Lock()
		listCache = cached
		listAt = time.Now()
		listMu.Unlock()
	}
	out := make([]Proc, len(cached))
	copy(out, cached)
	switch sortBy {
	case "mem":
		sort.SliceStable(out, func(i, j int) bool { return out[i].MemRSS > out[j].MemRSS })
	case "pid":
		sort.SliceStable(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	default: // cpu
		sort.SliceStable(out, func(i, j int) bool { return out[i].CPUPercent > out[j].CPUPercent })
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func scan() ([]Proc, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	list := make([]Proc, 0, len(procs))
	for _, p := range procs {
		info := Proc{PID: p.Pid}
		if name, e := p.Name(); e == nil {
			info.Name = name
		}
		if u, e := p.Username(); e == nil {
			info.User = u
		}
		if c, e := p.CPUPercent(); e == nil {
			info.CPUPercent = c
		}
		if m, e := p.MemoryInfo(); e == nil && m != nil {
			info.MemRSS = m.RSS
		}
		if mp, e := p.MemoryPercent(); e == nil {
			info.MemPercent = mp
		}
		if st, e := p.Status(); e == nil {
			info.Status = strings.Join(st, ",")
		}
		if t, e := p.NumThreads(); e == nil {
			info.Threads = t
		}
		if cl, e := p.Cmdline(); e == nil {
			if len(cl) > 200 {
				cl = cl[:200] + "..."
			}
			info.Cmdline = cl
		}
		if ct, e := p.CreateTime(); e == nil {
			info.CreateTime = ct
		}
		if pp, e := p.Ppid(); e == nil {
			info.PPID = pp
		}
		if info.Name == "" && info.Cmdline == "" {
			continue // 权限不足或已退出
		}
		list = append(list, info)
	}
	return list, nil
}

// Kill 终止指定进程(SIGTERM,force 时 SIGKILL)
func Kill(pid int32, force bool) error {
	if pid <= 1 {
		return fmt.Errorf("拒绝终止 PID %d", pid)
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return fmt.Errorf("进程 %d 不存在", pid)
	}
	// 保护面板自身
	if name, _ := p.Name(); strings.Contains(strings.ToLower(name), "kunpanel") {
		if pid == int32(os.Getpid()) {
			return fmt.Errorf("拒绝终止面板自身进程")
		}
	}
	if force {
		return p.Kill()
	}
	return p.Terminate()
}

// Detail 单个进程详情
func Detail(pid int32) (*Proc, error) {
	p, err := process.NewProcess(pid)
	if err != nil {
		return nil, fmt.Errorf("进程 %d 不存在", pid)
	}
	info := &Proc{PID: p.Pid}
	if name, e := p.Name(); e == nil {
		info.Name = name
	}
	if u, e := p.Username(); e == nil {
		info.User = u
	}
	if c, e := p.CPUPercent(); e == nil {
		info.CPUPercent = c
	}
	if m, e := p.MemoryInfo(); e == nil && m != nil {
		info.MemRSS = m.RSS
	}
	if mp, e := p.MemoryPercent(); e == nil {
		info.MemPercent = mp
	}
	if ct, e := p.CreateTime(); e == nil {
		info.CreateTime = ct
	}
	if cl, e := p.Cmdline(); e == nil {
		info.Cmdline = cl
	}
	return info, nil
}

// FormatPID 解析字符串 PID
func ParsePID(s string) (int32, error) {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("无效 PID")
	}
	return int32(v), nil
}
