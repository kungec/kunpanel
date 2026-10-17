// p1switch —— 1Panel → 坤面板 流量切换:停用 1Panel OpenResty(禁自启)→
// 面板网关改绑 80/443 → 验证(端口归属 + 逐域名状态码)→ 失败自动回滚。
package p1migrate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/btmigrate"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

// P1TrafficState 流量归属
type P1TrafficState struct {
	P1GWActive   bool   `json:"p1_gw_active"`
	PanelGW      string `json:"panel_gw"`
	PanelOn80    bool   `json:"panel_on_80"`
	Switched     bool   `json:"switched"`
	OrigPorts    string `json:"orig_ports,omitempty"`
}

func p1SwitchStateFile() string { return filepath.Join(util.DataDir(), "p1switch_state.json") }

func loadP1SwitchState() *P1TrafficState {
	st := &P1TrafficState{}
	if b, err := os.ReadFile(p1SwitchStateFile()); err == nil {
		_ = json.Unmarshal(b, st)
	}
	return st
}

func saveP1SwitchState(st *P1TrafficState) {
	st.Switched = st.PanelOn80 && !st.P1GWActive
	b, _ := json.Marshal(st)
	_ = util.AtomicWrite(p1SwitchStateFile(), b, 0o600)
}

// P1TrafficStatus 查询
func P1TrafficStatus() P1TrafficState {
	st := *loadP1SwitchState()
	if gw := p1OpenRestyContainer(); gw != "" {
		out, err := util.Run("docker", "inspect", gw, "--format", "{{.State.Status}}")
		st.P1GWActive = err == nil && strings.TrimSpace(out) == "running"
	}
	if key, err := solePanelGateway(); err == nil {
		st.PanelGW = key
		p80, _, e := btmigrate.ContainerPublishedPortsE(key)
		st.PanelOn80 = e == nil && p80 == 80
	}
	st.Switched = st.PanelOn80 && !st.P1GWActive
	return st
}

// solePanelGateway 运行中的面板网关容器必须唯一
func solePanelGateway() (string, error) {
	var running []string
	for _, name := range []string{"kun-nginx", "kun-openresty"} {
		out, err := util.Run("docker", "ps", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
		if err == nil && strings.TrimSpace(out) != "" {
			running = append(running, name)
		}
	}
	switch len(running) {
	case 1:
		return running[0], nil
	case 0:
		return "", fmt.Errorf("未检测到运行中的面板网关容器,请先在应用商店安装并启动")
	default:
		return "", fmt.Errorf("检测到多个运行中的网关容器(%s),请只保留一个", strings.Join(running, "、"))
	}
}

// P1SwitchTraffic 切换:基线 → 停 1Panel 网关(禁自启)→ 面板网关改绑
// 80/443 → 验证(端口归属 + 逐域名状态码)→ 失败自动回滚
func P1SwitchTraffic(w io.Writer) error {
	p1gw := p1OpenRestyContainer()
	if p1gw == "" {
		return fmt.Errorf("未检测到运行中的 1Panel OpenResty 容器,无需切换")
	}
	gwKey, err := solePanelGateway()
	if err != nil {
		return err
	}
	// 基线(当前 1Panel 网关在服务)
	var baseline []probeItem
	all, lerr := websites.List()
	if lerr == nil {
		for _, s := range all {
			if !s.Enabled || s.Domain == "" {
				continue
			}
			d := strings.Fields(s.Domain)[0]
			baseline = append(baseline, probeItem{domain: d, path: "/", code: p1Probe(d, "/"), https: false})
		}
	}
	fmt.Fprintf(w, ">> 已采集 %d 个域名的切换前状态码基线\n", len(baseline))

	// 停 1Panel 网关并禁自启(重启后不再与面板抢 80/443;回滚时恢复)
	fmt.Fprintf(w, ">> 停止 1Panel OpenResty(%s)...\n", p1gw)
	_, _ = util.Run("docker", "update", "--restart=no", p1gw)
	if out, err := util.Run("docker", "stop", p1gw); err != nil {
		fmt.Fprintf(w, "   停止出现问题(%s %s),继续 ...\n", tailOf(out, 80), tailOf(err.Error(), 80))
	}
	time.Sleep(time.Second)

	// 面板网关改绑 80/443
	st := loadP1SwitchState()
	st.PanelGW = gwKey
	var switchErr error
	cur80, cur443, perr := btmigrate.ContainerPublishedPortsE(gwKey)
	if perr != nil {
		switchErr = fmt.Errorf("读取 %s 端口映射失败: %v", gwKey, perr)
	} else if cur80 == 80 && cur443 == 443 {
		fmt.Fprintf(w, "   [%s] 已绑定 80/443,无需改写\n", gwKey)
	} else {
		fmt.Fprintf(w, ">> 面板网关 %s 改绑 80/443(当前 %v/%v)...\n", gwKey, cur80, cur443)
		dir := GatewayComposeDir(gwKey)
		cf := filepath.Join(dir, "docker-compose.yml")
		var content []byte
		content, switchErr = os.ReadFile(cf)
		if switchErr == nil {
			newContent, o80, o443, changed := btmigrate.RewriteComposePortsE(string(content))
			if !changed {
				switchErr = fmt.Errorf("compose 中找不到可改写的 80/443 端口映射")
			} else {
				if st.OrigPorts == "" {
					st.OrigPorts = fmt.Sprintf("%d/%d", o80, o443)
					saveP1SwitchState(st)
				}
				if werr := util.AtomicWriteString(cf, newContent, 0o644); werr != nil {
					switchErr = werr
				} else {
					out, uerr := dockerx.Compose(dir, "up", "-d")
					fmt.Fprintln(w, out)
					if uerr != nil {
						switchErr = fmt.Errorf("容器重建失败: %s", tailOf(out, 300))
					}
					time.Sleep(2 * time.Second)
				}
			}
		}
	}
	// 验证:端口归属 + 逐域名状态码
	if switchErr == nil {
		p80, p443, e := btmigrate.ContainerPublishedPortsE(gwKey)
		switch {
		case e != nil:
			switchErr = fmt.Errorf("读取端口映射失败: %v", e)
		case p80 != 80 || p443 != 443:
			switchErr = fmt.Errorf("切换后 %s 未监听宿主 80/443(%v/%v)", gwKey, p80, p443)
		default:
			var mism []string
			for _, want := range baseline {
				if want.code == "" || want.code == "000" {
					continue
				}
				if got := p1Probe(want.domain, want.path); got != want.code {
					mism = append(mism, fmt.Sprintf("%s:切换前 %s → 切换后 %s", want.domain, want.code, got))
				}
			}
			if len(mism) > 0 {
				switchErr = fmt.Errorf("%d 个域名状态码与切换前不一致: %s", len(mism), strings.Join(mism, "; "))
			} else {
				fmt.Fprintf(w, ">> 验证通过:80/443 已由 %s 监听,域名状态码与切换前一致\n", gwKey)
			}
		}
	}
	// 失败自动回滚:还原端口 → 拉起面板网关 → 恢复 1Panel 网关
	if switchErr != nil {
		fmt.Fprintf(w, ">> 切换失败: %v\n>> 自动回滚 ...\n", switchErr)
		if rerr := p1Rollback(gwKey, p1gw, st, w); rerr != nil {
			return fmt.Errorf("%v;且自动回滚存在问题: %v(请立即检查 80/443 占用!)", switchErr, rerr)
		}
		return fmt.Errorf("切换失败已自动回滚: %v", switchErr)
	}
	saveP1SwitchState(st)
	fmt.Fprintf(w, ">> 流量已切换到面板网关。1Panel OpenResty 已停用并禁自启;回滚时会自动恢复\n")
	return nil
}

// P1RollbackTraffic 手动回滚:恢复端口映射、拉起面板网关(保持面板可用)、
// 启动 1Panel OpenResty(恢复自启)
func P1RollbackTraffic(w io.Writer) error {
	st := loadP1SwitchState()
	gwKey := st.PanelGW
	if gwKey == "" {
		if k, err := solePanelGateway(); err == nil {
			gwKey = k
		}
	}
	if gwKey != "" && st.OrigPorts != "" {
		parts := strings.SplitN(st.OrigPorts, "/", 2)
		o80, _ := strconv.Atoi(parts[0])
		o443 := 443
		if len(parts) > 1 {
			o443, _ = strconv.Atoi(parts[1])
		}
		dir := GatewayComposeDir(gwKey)
		cf := filepath.Join(dir, "docker-compose.yml")
		if b, err := os.ReadFile(cf); err == nil {
			content := btmigrate.RestoreComposePortsE(string(b), o80, o443)
			if err := util.AtomicWriteString(cf, content, 0o644); err == nil {
				fmt.Fprintf(w, "   [%s] compose 端口已还原为 %s\n", gwKey, st.OrigPorts)
			}
		}
		if out, err := dockerx.Compose(GatewayComposeDir(gwKey), "up", "-d"); err != nil {
			fmt.Fprintf(w, "   面板网关重建失败: %s\n", tailOf(out, 150))
		}
		st.OrigPorts = ""
	}
	// 启动 1Panel 网关
	if p1gw := p1OpenRestyContainer(); p1gw != "" {
		_, _ = util.Run("docker", "update", "--restart=unless-stopped", p1gw)
		if _, err := util.Run("docker", "start", p1gw); err != nil {
			fmt.Fprintf(w, "   启动 1Panel OpenResty 失败: %s\n", tailOf(err.Error(), 100))
		} else {
			fmt.Fprintf(w, "   已启动 1Panel OpenResty(%s)\n", p1gw)
		}
	} else {
		fmt.Fprintf(w, "   未检测到 1Panel OpenResty 容器(可能已卸载),跳过\n")
	}
	time.Sleep(2 * time.Second)
	saveP1SwitchState(st)
	fmt.Fprintf(w, ">> 已回滚:1Panel OpenResty 恢复服务;面板站点数据未改动\n")
	return nil
}

// p1Rollback 切换失败时的自动回滚
func p1Rollback(gwKey, p1gw string, st *P1TrafficState, w io.Writer) error {
	var errs []string
	if st.OrigPorts != "" {
		parts := strings.SplitN(st.OrigPorts, "/", 2)
		o80, _ := strconv.Atoi(parts[0])
		o443 := 443
		if len(parts) > 1 {
			o443, _ = strconv.Atoi(parts[1])
		}
		dir := GatewayComposeDir(gwKey)
		cf := filepath.Join(dir, "docker-compose.yml")
		if b, err := os.ReadFile(cf); err == nil {
			content := btmigrate.RestoreComposePortsE(string(b), o80, o443)
			if err := util.AtomicWriteString(cf, content, 0o644); err != nil {
				errs = append(errs, "还原端口失败: "+err.Error())
			} else {
				fmt.Fprintf(w, "   [%s] compose 端口已还原为 %s\n", gwKey, st.OrigPorts)
			}
		}
	}
	dir := GatewayComposeDir(gwKey)
	if out, err := dockerx.Compose(dir, "up", "-d"); err != nil {
		errs = append(errs, "面板网关重建失败: "+tailOf(out, 120))
		if _, serr := dockerx.Compose(dir, "stop"); serr != nil {
			errs = append(errs, "停止面板网关也失败")
		} else {
			fmt.Fprintf(w, "   已停止面板网关(端口未还原,避免抢占)\n")
		}
	} else {
		st.OrigPorts = ""
	}
	if _, err := util.Run("docker", "start", p1gw); err != nil {
		errs = append(errs, "启动 1Panel OpenResty 失败: "+err.Error())
	}
	_, _ = util.Run("docker", "update", "--restart=unless-stopped", p1gw)
	time.Sleep(2 * time.Second)
	// 回滚确认:80/443 应有监听(1Panel OpenResty 经 docker-proxy)
	if out, err := exec.Command("sh", "-c", "ss -tln | grep -E ':(80|443) '").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) == "" {
		errs = append(errs, "回滚后未检测到 80/443 监听: "+tailOf(string(out), 120))
	} else {
		fmt.Fprintln(w, ">> 回滚确认:80/443 已恢复监听")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// probeItem 本地探测项(与 btmigrate.probeItem 同构,独立定义避免跨包导出)
type probeItem struct {
	domain string
	path   string
	https  bool
	code   string
}

// p1Probe 本机探测域名状态码(1Panel 网关在 80 时走 http)
func p1Probe(domain, path string) string {
	out, err := util.Run("curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"--max-time", "8", "--resolve", domain+":80:127.0.0.1", "http://"+domain+path)
	if err != nil {
		return "000"
	}
	code := strings.TrimSpace(out)
	if len(code) != 3 {
		return "000"
	}
	return code
}

// StopP1GatewayIfAny 供外部流程调用:若 1Panel 网关在运行则停用(禁自启)
func StopP1GatewayIfAny(w io.Writer) {
	if gw := p1OpenRestyContainer(); gw != "" {
		_, _ = util.Run("docker", "update", "--restart=no", gw)
		if out, err := util.Run("docker", "stop", gw); err == nil {
			fmt.Fprintf(w, "   已停用 1Panel OpenResty(%s)\n", tailOf(out, 40))
		}
	}
}

var _ = os.Getenv
var _ = exec.Command
