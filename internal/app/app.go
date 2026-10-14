package app

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"crypto/tls"
	"kunpanel/internal/alertx"
	"path/filepath"
	"runtime/debug"

	"kunpanel/internal/config"
	"kunpanel/internal/cronx"
	"kunpanel/internal/monhist"
	"kunpanel/internal/panelupdate"
	"kunpanel/internal/plugins"
	"kunpanel/internal/server"
	"kunpanel/internal/system"
	"kunpanel/internal/upmon"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

// Options 启动参数
type Options struct {
	Version    string
	ConfigFile string // 预留:自定义配置路径
	WebFS      fs.FS
}

var version = "dev"

// Run 面板主入口:加载配置 -> 启动调度器 -> 监听 HTTP
func Run(o Options) error {
	// GC 调优:面板内存占用 <50MB,降低 GC 频率减少 CPU 开销
	debug.SetGCPercent(200)

	version = o.Version
	server.Version = o.Version

	if _, err := config.Load(); err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}

	// 计划任务调度器
	stop := make(chan struct{})
	defer close(stop)
	cronx.StartScheduler(stop)

	// 插件系统(注入网站模块 WAF 钩子)
	plugins.Init()

	// 升级后把全部站点配置迁移到当前格式(如新增的 ACME 挑战 location)
	go func() {
		time.Sleep(10 * time.Second)
		if err := websites.RewriteAllConfs(); err != nil {
			log.Printf("站点配置迁移失败: %v", err)
		}
	}()

	// 监控告警循环
	// 系统状态后台采样(dashboard 轮询读缓存,不再阻塞)
	system.StartSampler(stop)

	alertx.Start(stop)

	// 网站拨测:状态翻转时通过告警渠道通知
	upmon.Notifier = func(name, target string, up bool, detail string) {
		text := map[bool]string{
			true:  fmt.Sprintf("【恢复】拨测目标 %s(%s) 已恢复, %s", name, target, detail),
			false: fmt.Sprintf("【宕机】拨测目标 %s(%s) 无法访问, %s", name, target, detail),
		}[up]
		alertx.Broadcast(alertx.Get().Webhook, text, map[string]any{"type": map[bool]string{true: "up_recover", false: "up_down"}[up]})
	}
	upmon.Start(stop)

	// 监控历史采样
	monhist.Start(stop)

	// SSL 证书到期巡检(使用告警设置中的 Webhook)
	go func() {
		time.Sleep(2 * time.Minute)
		websites.EnsureSslDir()
		notify := func(msg string) {
			if wh := alertx.Get().Webhook; wh != "" {
				_ = alertx.Push(wh, map[string]any{"type": "cert_expire", "text": msg})
			}
		}
		websites.StartCertWatch(stop, notify)
		websites.StartACMERenew(stop, notify)
	}()

	cfg := config.M.Get()
	webSub, err := fs.Sub(o.WebFS, "web")
	if err != nil {
		return fmt.Errorf("内嵌前端资源异常: %w", err)
	}

	srv := server.New(server.Options{Version: o.Version, WebFS: webSub})

	// 面板在线更新:定时检查 GitHub 新版本,发现即写消息中心
	panelupdate.SetCurrent(o.Version)
	panelupdate.Start(stop)

	// 拼全面板地址(含随机入口;"-" 表示已关闭)
	path := "/"
	if cfg.Entrance != "" && cfg.Entrance != "-" {
		path = "/" + cfg.Entrance
	}

	// 优雅退出
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		fmt.Println("收到退出信号,正在关闭 ...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	// 面板 SSL:启用且证书可用时走 HTTPS;证书异常自动回退 HTTP,避免把自己锁在门外
	if cfg.SSL {
		cert, key := filepath.Join(config.DataDir(), "panelssl", "cert.pem"), filepath.Join(config.DataDir(), "panelssl", "key.pem")
		if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
			fmt.Printf("  ⚠ 面板 SSL 证书加载失败(%v),已回退为 HTTP 访问\n", err)
			cfg.SSL = false
		}
	}
	proto := "http"
	if cfg.SSL {
		proto = "https"
	}
	fmt.Println("──────────────────────────────────────────")
	fmt.Printf("  坤面板 KunPanel %s\n", o.Version)
	fmt.Printf("  面板地址: %s://%s:%d%s\n", proto, util.LocalIP(), cfg.Port, path)
	fmt.Printf("  本机地址: %s://127.0.0.1:%d%s\n", proto, cfg.Port, path)
	if cfg.Entrance != "" && cfg.Entrance != "-" {
		fmt.Println("  (地址含随机入口,直接访问 http://IP:" + strconv.Itoa(cfg.Port) + " 将返回 404)")
	} else {
		fmt.Println("  ⚠ 安全入口已关闭,建议在【面板设置】中开启")
	}
	fmt.Printf("  数据目录: %s\n", config.DataDir())
	fmt.Println("──────────────────────────────────────────")

	// 优雅退出
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		fmt.Println("收到退出信号,正在关闭 ...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if cfg.SSL {
		cert, key := filepath.Join(config.DataDir(), "panelssl", "cert.pem"), filepath.Join(config.DataDir(), "panelssl", "key.pem")
		if err := srv.ListenAndServeTLS(cert, key); err != nil && err.Error() != "http: Server closed" {
			return fmt.Errorf("监听 :%d 失败: %w", cfg.Port, err)
		}
		return nil
	}
	if err := srv.ListenAndServe(); err != nil && err.Error() != "http: Server closed" {
		return fmt.Errorf("监听 :%d 失败: %w", cfg.Port, err)
	}
	return nil
}

// ResetPassword 命令行重置管理员密码
func ResetPassword() error {
	if _, err := config.Load(); err != nil {
		return err
	}
	pwd, err := config.M.ResetPassword()
	if err != nil {
		return err
	}
	fmt.Println("管理员密码已重置为:", pwd)
	fmt.Println("请使用该密码重新登录,并尽快在面板中修改。")
	return nil
}
