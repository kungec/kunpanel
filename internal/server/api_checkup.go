package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/appstore"
	"kunpanel/internal/config"
	"kunpanel/internal/cronx"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/plugins"
	"kunpanel/internal/system"
	"kunpanel/internal/websites"
)

type checkItem struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok / warn / fail
	Detail string `json:"detail"`
}

// handlePanelCheckup 面板一键自检:磁盘/内存/负载/证书/应用/安全配置/计划任务
// checkupCache 一键自检结果 60s 缓存:自检要跑磁盘/容器/证书/任务等十来项检查(~0.5s),
// 用户连点或重复进入不必重跑
var (
	checkupMu    sync.Mutex
	checkupCache []checkItem
	checkupAt    time.Time
	checkupFail  int
	checkupWarn  int
)

func (s *Server) handlePanelCheckup(w http.ResponseWriter, r *http.Request) {
	checkupMu.Lock()
	if checkupCache != nil && time.Since(checkupAt) < 60*time.Second {
		items, fail, warn := checkupCache, checkupFail, checkupWarn
		checkupMu.Unlock()
		OK(w, map[string]any{"items": items, "fail": fail, "warn": warn, "score": 100 - 15*fail - 5*warn, "cached": true})
		return
	}
	checkupMu.Unlock()
	items := []checkItem{}
	fail, warn := 0, 0
	add := func(status, name, detail string) {
		if status == "fail" {
			fail++
		} else if status == "warn" {
			warn++
		}
		items = append(items, checkItem{Name: name, Status: status, Detail: detail})
	}

	// 1. 磁盘空间
	snap := system.Get()
	for _, d := range snap.Disks {
		if d.Total == 0 {
			continue
		}
		st, note := "ok", fmt.Sprintf("已用 %.0f%%(剩余 %s)", d.UsageP, humanBytes(d.Free))
		if d.UsageP >= 90 {
			st = "fail"
			note = fmt.Sprintf("使用率 %s%%,空间即将耗尽!", fmt.Sprintf("%.0f", d.UsageP))
		} else if d.UsageP >= 80 {
			st = "warn"
			note = fmt.Sprintf("使用率 %.0f%%,建议清理", d.UsageP)
		}
		add(st, "磁盘空间 "+d.Path, note)
	}

	// 2. 内存
	st, note := "ok", fmt.Sprintf("已用 %.0f%%", snap.MemUsageP)
	if snap.MemUsageP >= 90 {
		st = "fail"
		note = fmt.Sprintf("内存使用率 %.0f%%,存在 OOM 风险", snap.MemUsageP)
	} else if snap.MemUsageP >= 80 {
		st = "warn"
		note = fmt.Sprintf("内存使用率 %.0f%%,偏高", snap.MemUsageP)
	}
	add(st, "内存", note)

	// 3. 系统负载
	if cores := float64(snap.CPUCores); cores > 0 && snap.Load15/cores >= 2 {
		add("warn", "系统负载", fmt.Sprintf("15 分钟平均负载 %.2f,超过核心数 %d 的 2 倍", snap.Load15, snap.CPUCores))
	} else {
		add("ok", "系统负载", fmt.Sprintf("15 分钟平均 %.2f / %d 核", snap.Load15, snap.CPUCores))
	}

	// 4. SSL 证书到期
	certs := websites.ListCerts()
	expSoon, expired := 0, 0
	names := []string{}
	for _, c := range certs {
		if c.Expire == "" {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02", c.Expire, time.Local)
		if err != nil {
			continue
		}
		days := int(time.Until(t).Hours() / 24)
		// shortlived 短期证书(IP 证书)有效期约 6 天且自动续期,按半衰期判定
		dueDays := 30
		if c.Profile == "shortlived" {
			dueDays = 3
		}
		if days < 0 {
			expired++
			names = append(names, c.Name+" 已过期")
		} else if days <= dueDays {
			expSoon++
			names = append(names, fmt.Sprintf("%s 剩余 %d 天", c.Name, days))
		}
	}
	if expired > 0 {
		add("fail", "SSL 证书", strings.Join(names, ";"))
	} else if expSoon > 0 {
		add("warn", "SSL 证书", strings.Join(names, ";"))
	} else {
		add("ok", "SSL 证书", fmt.Sprintf("共 %d 张,均未临期", len(certs)))
	}

	// 5. 应用容器状态(全部应运行)
	insts := []struct {
		Key string `json:"key"`
	}{}
	_ = insts
	tplKeys := []string{"nginx", "openresty", "mysql", "mariadb", "postgresql", "redis", "php", "phpmyadmin", "wordpress"}
	running, stopped := 0, []string{}
	for _, k := range tplKeys {
		if dockerx.ContainerRunning("kun-" + k) {
			running++
		} else if installedApp(k) {
			stopped = append(stopped, k)
		}
	}
	if len(stopped) > 0 {
		add("warn", "应用容器", "已停止: "+strings.Join(stopped, ","))
	} else {
		add("ok", "应用容器", fmt.Sprintf("%d 个运行中", running))
	}

	// 6. 安全配置
	cfg := config.M.Get()
	secChecks := []struct {
		name string
		ok   bool
		hint string
	}{
		{"安全入口", cfg.Entrance != "" && cfg.Entrance != "-", "建议开启,防止面板被扫描"},
		{"两步验证", cfg.TotpEnabled && cfg.TotpSecret != "", "建议开启,防止密码泄露后被直接登录"},
	}
	for _, sc := range secChecks {
		if sc.ok {
			add("ok", "安全-"+sc.name, "已启用")
		} else {
			add("warn", "安全-"+sc.name, sc.hint)
		}
	}

	// 7. WAF
	wafList := plugins.List()
	for _, p := range wafList {
		if p.Key != "waf" {
			continue
		}
		if p.Status != "installed" {
			add("warn", "WAF 防火墙", "未安装,网站无防护")
		} else if !p.Active {
			add("warn", "WAF 防火墙", "已安装但未启用")
		} else {
			add("ok", "WAF 防火墙", "运行中,网站受保护")
		}
	}

	// 8. 计划任务失败
	failed := []string{}
	for _, j := range cronx.List() {
		if j.Enabled && j.LastStat == "failed" {
			failed = append(failed, j.Name)
		}
	}
	if len(failed) > 0 {
		add("fail", "计划任务", "最近执行失败: "+strings.Join(failed, ","))
	} else {
		add("ok", "计划任务", "全部正常")
	}

	// 9. 面板 SSL
	if cfg.SSL {
		add("ok", "面板 HTTPS", "已启用")
	} else {
		add("warn", "面板 HTTPS", "未启用,面板通信为明文,可在 面板设置→安全 中开启")
	}

	score := 100 - fail*15 - warn*5
	if score < 0 {
		score = 0
	}
	checkupMu.Lock()
	checkupCache, checkupAt, checkupFail, checkupWarn = items, time.Now(), fail, warn
	checkupMu.Unlock()
	OK(w, map[string]any{
		"score": score,
		"items": items,
		"time":  time.Now().Format("2006-01-02 15:04:05"),
	})
}

// installedApp 检查应用是否已安装(存在 compose 实例目录)
func installedApp(key string) bool {
	_, ok := appstore.GetTemplate(key)
	if !ok {
		return false
	}
	return dirExists(filepath.Join(config.DataDir(), "apps", key))
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func humanBytes(n uint64) string {
	const kb = 1024
	switch {
	case n >= kb*kb*kb:
		return fmt.Sprintf("%.1f TB", float64(n)/kb/kb/kb)
	case n >= kb*kb:
		return fmt.Sprintf("%.1f GB", float64(n)/kb/kb)
	case n >= kb:
		return fmt.Sprintf("%.1f MB", float64(n)/kb)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
