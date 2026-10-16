// switch.go —— 流量切换/回滚的系统服务化处理。
//
// 此前停宝塔 Nginx 直接 nginx -s stop:服务器重启后宝塔 Nginx 会自启,
// 与面板容器抢 80/443;回滚也是在 systemd 体系外手动拉起。现统一:
//   - 优先 systemd 单元(nginx/bt),否则 /etc/init.d/nginx,最后才二进制
//   - 切换时禁用宝塔 Nginx 自启(记录方式,回滚时恢复)
package btmigrate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kunpanel/internal/util"
)

// 迁移状态文件:记录已导入站点/宝塔自启禁用方式,供回滚与续期使用
type MigrateState struct {
	Imported       []ImportedSite `json:"imported"`
	MigratedCrons  int            `json:"migrated_crons"`
	Switched       bool           `json:"switched"`
	AutostartOff   bool           `json:"autostart_off"`
	AutostartVia   string         `json:"autostart_via,omitempty"` // systemd / initd
	BTCronDisabled bool           `json:"bt_cron_disabled"`
	At             string         `json:"at,omitempty"`
}

type ImportedSite struct {
	Domain string `json:"domain"`
	SiteID string `json:"site_id"`
	SSL    bool   `json:"ssl"`
}

func stateFile() string { return filepath.Join(util.DataDir(), "btmigrate_state.json") }

func loadState() *MigrateState {
	st := &MigrateState{}
	if b, err := os.ReadFile(stateFile()); err == nil {
		_ = json.Unmarshal(b, st)
	}
	return st
}

func saveState(st *MigrateState) {
	st.At = time.Now().Format("2006-01-02 15:04:05")
	if b, err := json.Marshal(st); err == nil {
		_ = util.AtomicWrite(stateFile(), b, 0o600)
	}
}

// systemdUnitActive 探测 nginx systemd 单元是否存在
func btNginxUnit() string {
	for _, u := range []string{"nginx", "bt"} {
		if b, err := exec.Command("systemctl", "list-unit-files", u+".service", "--no-legend").CombinedOutput(); err == nil && strings.TrimSpace(string(b)) != "" {
			return u
		}
	}
	return ""
}

// stopBTNginx 停宝塔 Nginx:systemd → init.d → 二进制,逐级回退并校验
func stopBTNginx(w io.Writer) error {
	unit := btNginxUnit()
	if unit != "" {
		if out, err := exec.Command("systemctl", "stop", unit).CombinedOutput(); err == nil {
			fmt.Fprintf(w, "   已通过 systemd 停止 %s 服务\n", unit)
			return nil
		} else {
			fmt.Fprintf(w, "   systemd 停止失败(%s),改用 init.d\n", tailStr(string(out), 120))
		}
	}
	if _, err := os.Stat("/etc/init.d/nginx"); err == nil {
		if out, err := exec.Command("/etc/init.d/nginx", "stop").CombinedOutput(); err == nil {
			fmt.Fprintln(w, "   已通过 /etc/init.d/nginx stop 停止")
			_ = out
			return nil
		}
	}
	if _, err := os.Stat(btNginxBin); err == nil {
		_ = exec.Command(btNginxBin, "-s", "stop").Run()
		fmt.Fprintln(w, "   已通过 nginx -s stop 停止(无系统服务)")
	}
	time.Sleep(time.Second)
	return nil
}

// startBTNginx 启动宝塔 Nginx(回滚用):systemd → init.d → 二进制
func startBTNginx(w io.Writer) error {
	unit := btNginxUnit()
	if unit != "" {
		if out, err := exec.Command("systemctl", "start", unit).CombinedOutput(); err == nil {
			fmt.Fprintf(w, "   已通过 systemd 启动 %s 服务\n", unit)
			return nil
		} else {
			fmt.Fprintf(w, "   systemd 启动失败(%s),改用 init.d\n", tailStr(string(out), 120))
		}
	}
	if _, err := os.Stat("/etc/init.d/nginx"); err == nil {
		if err := exec.Command("/etc/init.d/nginx", "start").Run(); err == nil {
			fmt.Fprintln(w, "   已通过 /etc/init.d/nginx start 启动")
			return nil
		}
	}
	if err := exec.Command(btNginxBin).Start(); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	return nil
}

// disableBTNginxAutostart 禁宝塔 Nginx 自启(防止服务器重启后抢 80/443)。
// 记录禁用方式,回滚时按原样恢复。
func disableBTNginxAutostart(w io.Writer) (bool, string) {
	unit := btNginxUnit()
	if unit != "" {
		if out, err := exec.Command("systemctl", "disable", unit).CombinedOutput(); err == nil {
			fmt.Fprintf(w, "   已禁用 %s.service 自启(服务器重启不会再抢 80/443)\n", unit)
			_ = out
			return true, "systemd"
		}
	}
	if _, err := os.Stat("/etc/init.d/nginx"); err == nil {
		// Debian: update-rc.d;CentOS: chkconfig
		ok := false
		if out, err := exec.Command("update-rc.d", "-f", "nginx", "remove").CombinedOutput(); err == nil {
			ok = true
			_ = out
		} else if out, err := exec.Command("chkconfig", "nginx", "off").CombinedOutput(); err == nil {
			ok = true
			_ = out
		}
		if ok {
			fmt.Fprintln(w, "   已移除 nginx init.d 自启(服务器重启不会再抢 80/443)")
			return true, "initd"
		}
	}
	return false, ""
}

// restoreBTNginxAutostart 回滚时恢复宝塔 Nginx 自启
func restoreBTNginxAutostart(w io.Writer, via string) {
	switch via {
	case "systemd":
		_ = exec.Command("systemctl", "enable", "nginx").Run()
	case "initd":
		_ = exec.Command("update-rc.d", "nginx", "defaults").Run()
		_ = exec.Command("chkconfig", "nginx", "on").Run()
	}
	fmt.Fprintln(w, "   已恢复宝塔 Nginx 自启")
}
