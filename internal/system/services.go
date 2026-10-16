package system

import (
	"sync"
	"sync/atomic"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"kunpanel/internal/util"
)

// ServiceUnit systemd 服务单元
type ServiceUnit struct {
	Name    string `json:"name"`
	Active  string `json:"active"` // active / inactive / failed /激活中...
	Sub     string `json:"sub"`
	Desc    string `json:"desc"`
	Enabled string `json:"enabled"` // enabled / disabled / static / -
}

// SystemdAvailable 当前系统是否可用 systemd 管理
func SystemdAvailable() bool {
	return runtime.GOOS == "linux" && hasBinary("systemctl") && dirExists("/run/systemd/system")
}

func hasBinary(name string) bool {
	path, err := exec.LookPath(name)
	return err == nil && path != ""
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// ListServices 列出全部 systemd 服务单元
// svcCache 服务列表 30s SWR 缓存:systemctl list-units 本身要 1.5s,
// 服务列表页面轮询/重复访问不应每次都付这个代价;启停操作后立即失效
var (
	svcMu       sync.Mutex
	svcCache    []ServiceUnit
	svcCacheAt  time.Time
	svcRefreshing int32
)

// ListServicesCached 列出服务(30s SWR 缓存:有旧值立即返回并后台刷新)
func ListServicesCached() ([]ServiceUnit, error) {
	svcMu.Lock()
	if svcCache != nil && time.Since(svcCacheAt) < 30*time.Second {
		out := svcCache
		svcMu.Unlock()
		return out, nil
	}
	svcMu.Unlock()
	if atomic.CompareAndSwapInt32(&svcRefreshing, 0, 1) {
		go func() {
			defer atomic.StoreInt32(&svcRefreshing, 0)
			list, err := ListServices()
			if err != nil {
				return
			}
			for i := range list {
				list[i].Enabled = IsServiceEnabled(list[i].Name)
			}
			svcMu.Lock()
			svcCache, svcCacheAt = list, time.Now()
			svcMu.Unlock()
		}()
	}
	svcMu.Lock()
	defer svcMu.Unlock()
	if svcCache != nil {
		return svcCache, nil
	}
	// 冷启动:同步取一次
	list, err := ListServices()
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Enabled = IsServiceEnabled(list[i].Name)
	}
	svcCache, svcCacheAt = list, time.Now()
	return list, nil
}

// InvalidateServicesCache 服务启停后调用,下次读取立即取新
func InvalidateServicesCache() {
	svcMu.Lock()
	svcCache, svcCacheAt = nil, time.Time{}
	svcMu.Unlock()
}

func ListServices() ([]ServiceUnit, error) {
	if !SystemdAvailable() {
		return nil, fmt.Errorf("当前系统不支持 systemd 服务管理")
	}
	out, err := util.RunWithTimeout(20*time.Second, "systemctl", "list-units", "--type=service", "--all", "--no-pager", "--no-legend")
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, err
	}
	var list []ServiceUnit
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 4 {
			continue
		}
		name := fields[0]
		name = strings.TrimSuffix(name, ".service")
		list = append(list, ServiceUnit{
			Name:   name,
			Active: fields[2],
			Sub:    fields[3],
			Desc:   strings.Join(fields[4:], " "),
		})
	}
	return list, nil
}

// IsServiceEnabled 查询服务是否开机自启(is-enabled)
func IsServiceEnabled(name string) string {
	out, err := util.RunWithTimeout(8*time.Second, "systemctl", "is-enabled", unitName(name))
	if err != nil {
		if s := strings.TrimSpace(out); s != "" {
			return s
		}
		return "-"
	}
	return strings.TrimSpace(out)
}

var serviceRe = regexp.MustCompile(`^[A-Za-z0-9@._\\-]{1,100}$`)

func unitName(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasSuffix(name, ".service") {
		return name
	}
	return name + ".service"
}

// ServiceAction 对服务执行操作:start / stop / restart / enable / disable
func ServiceAction(name, action string) (string, error) {
	if !SystemdAvailable() {
		return "", fmt.Errorf("当前系统不支持 systemd 服务管理")
	}
	if !serviceRe.MatchString(name) {
		return "", fmt.Errorf("非法服务名")
	}
	switch action {
	case "start", "stop", "restart", "enable", "disable":
	default:
		return "", fmt.Errorf("不支持的操作: %s", action)
	}
	if name == "kunpanel" && (action == "stop" || action == "disable") {
		return "", fmt.Errorf("拒绝操作:停止面板自身会导致面板失联,请在服务器终端手动执行")
	}
	out, err := util.RunWithTimeout(60*time.Second, "systemctl", action, unitName(name))
	if err != nil {
		return out, fmt.Errorf("%s", tailStr2(out))
	}
	return out, nil
}

func tailStr2(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[len(s)-200:]
	}
	return s
}
