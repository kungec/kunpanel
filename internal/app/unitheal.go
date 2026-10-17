package app

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"kunpanel/internal/notifyx"
	"kunpanel/internal/util"
)

// 旧版 systemd 单元自愈。
// v2.75.4 及之前的 install.sh 给面板服务单元加了 ProtectSystem=full /
// ReadWritePaths 白名单 / RestrictSUIDSGID,把 /usr、/etc 对面板及其全部子进程
// 设为只读,导致包管理(apt/dpkg 写 /usr、/etc)、Docker 一键安装、daemon.json
// 编辑等核心功能隐性失败(curl: 23、dpkg subprocess killed by Broken pipe 等)。
// v2.75.5 起单元模板已移除这些选项;本文件在启动时把存量安装的旧单元自动改写,
// 并在当前进程仍受旧沙箱限制时安排一次自重启使修复立即生效。

const systemUnitPath = "/etc/systemd/system/kunpanel.service"

// deprecatedUnitPrefixes 需要从旧单元中移除的行(前缀匹配,保留用户其余自定义)
var deprecatedUnitPrefixes = []string{
	"ProtectSystem=",
	"ReadWritePaths=",
	"RestrictSUIDSGID=",
}

// HealSystemdUnit 启动时调用:检测并修复旧版单元文件
func HealSystemdUnit() {
	if runtime.GOOS != "linux" {
		return
	}
	b, err := os.ReadFile(systemUnitPath)
	if err != nil {
		return // 无 systemd 单元(Docker / OpenRC / nohup 部署)无需处理
	}
	if !strings.Contains(string(b), "ProtectSystem=") &&
		!strings.Contains(string(b), "ReadWritePaths=") &&
		!strings.Contains(string(b), "RestrictSUIDSGID=") {
		return // 单元已是新版
	}
	healed := removeDeprecatedLines(string(b))
	if err := util.AtomicWrite(systemUnitPath, []byte(healed), 0o644); err != nil {
		log.Printf("[unit-heal] 改写单元文件失败: %v", err)
		return
	}
	if _, err := util.Run("systemctl", "daemon-reload"); err != nil {
		log.Printf("[unit-heal] systemctl daemon-reload 失败: %v", err)
	}
	log.Printf("[unit-heal] 已移除旧版单元的文件系统沙箱限制(ProtectSystem 等),包管理功能恢复")

	// 当前进程启动时仍加载了旧沙箱,/etc 探测写失败 ⇒ 需要重启一次才能生效。
	// 单元改写本身就是一次性开关:下次启动单元已是新版,不会再走到这里。
	if dirWritable("/etc") {
		log.Printf("[unit-heal] 当前进程未受限制,重启面板后完全生效")
		return
	}
	log.Printf("[unit-heal] 5 秒后自动重启面板使修复生效 ...")
	notifyx.Add("system", "面板服务单元已自动修复",
		"检测到旧版 systemd 单元的文件系统沙箱(ProtectSystem=full)会导致软件包管理、Docker 安装等功能失败,已自动改写单元文件;面板将在数秒内自动重启一次,重启后相关功能即可正常使用。")
	unit := fmt.Sprintf("kp-unit-heal-%d", time.Now().UnixNano())
	if _, err := util.Run("systemd-run", "--collect", "--on-active=5",
		"--unit="+unit, "systemctl", "restart", "kunpanel"); err != nil {
		log.Printf("[unit-heal] 自动重启调度失败,请手动执行 systemctl restart kunpanel: %v", err)
	}
}

func removeDeprecatedLines(content string) string {
	var kept []string
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		deprecated := false
		for _, p := range deprecatedUnitPrefixes {
			if strings.HasPrefix(t, p) {
				deprecated = true
				break
			}
		}
		if !deprecated {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// dirWritable 目录可写探测(写后即删)
func dirWritable(dir string) bool {
	probe := filepath.Join(dir, ".kunpanel-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}
