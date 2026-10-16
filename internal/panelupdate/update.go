// Package panelupdate 面板在线更新:对接 GitHub Releases
// 定时检查新版本 → 消息中心提醒;面板设置页一键拉取并自动重启
package panelupdate

import (
	"encoding/hex"
	"crypto/sha256"
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/notifyx"
	"kunpanel/internal/util"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

const Repo = "kungec/kunpanel"

var (
	mu       sync.Mutex
	lastInfo *Info
	current  string
)

// Info 更新检查结果
type Info struct {
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	HasUpdate   bool   `json:"has_update"`
	Name        string `json:"name"`
	Notes       string `json:"notes"`
	PublishedAt string `json:"published_at"`
	CheckedAt   string `json:"checked_at"`
}

func SetCurrent(v string) { current = v }

func fetchJSON(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		// GitHub 直连失败(网络受限)时走加速代理
		resp2, err2 := client.Get("https://gh-proxy.com/" + url)
		if err2 != nil {
			return nil, err
		}
		resp = resp2
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

type release struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
}

// Check 拉取 GitHub 最新 release 并与当前版本比较
func Check() (*Info, error) {
	// 检查更新:直连 GitHub API,失败回退 gh-proxy(国内服务器直连常被重置)
	b, err := fetchJSON("https://api.github.com/repos/"+Repo+"/releases/latest", 6*time.Second)
	if err != nil {
		b, err = fetchJSON("https://gh-proxy.com/https://api.github.com/repos/"+Repo+"/releases/latest", 10*time.Second)
		if err != nil {
			return nil, fmt.Errorf("连接 GitHub 失败(直连与加速代理均不可用): %v", err)
		}
	}
	var r release
	if err := jsonUnmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}
	if r.TagName == "" {
		return nil, fmt.Errorf("仓库没有 release")
	}
	mu.Lock()
	defer mu.Unlock()
	lastInfo = &Info{
		Current:     current,
		Latest:      r.TagName,
		HasUpdate:   newer(r.TagName, current),
		Name:        r.Name,
		Notes:       r.Body,
		PublishedAt: r.PublishedAt,
		CheckedAt:   time.Now().Format("2006-01-02 15:04:05"),
	}
	return lastInfo, nil
}

// Cached 上次检查结果(可能为 nil)
func Cached() *Info {
	mu.Lock()
	defer mu.Unlock()
	return lastInfo
}

// Start 定时检查:启动 5 分钟后一次,之后每 6 小时一次;发现新版本写消息中心
func Start(stop <-chan struct{}) {
	go func() {
		time.Sleep(5 * time.Minute)
		if info, err := Check(); err == nil && info != nil && info.HasUpdate {
			notifyx.Add("update", "发现面板新版本 "+info.Latest,
				"当前 "+info.Current+",可前往 面板设置 → 关于 → 面板更新 一键在线更新")
		}
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if info, err := Check(); err == nil && info != nil && info.HasUpdate {
					notifyx.Add("update", "发现面板新版本 "+info.Latest,
						"当前 "+info.Current+",可前往 面板设置 → 关于 → 面板更新 一键在线更新")
				}
			}
		}
	}()
}

// ---------- 版本比较 ----------

// newer 判断 latest 是否严格大于 current(v 前缀语义化版本;解析失败退化为不等即新)
func newer(latest, current string) bool {
	l, ok1 := Semver(latest)
	c, ok2 := Semver(current)
	if !ok1 || !ok2 {
		return latest != "" && latest != current
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// Semver 解析 v 前缀语义化版本,非法返回 false
func Semver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 || parts[0] == "" {
		return out, false
	}
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ---------- 下载与安装 ----------

// checksumsURL 校验和清单获取地址:直连 GitHub 优先,失败回退 gh-proxy
// (经代理的校验和只能防传输损坏,供应链信任弱于直连——响应中会标记来源)
func checksumsURLs(tag string) []string {
	u := "https://github.com/kungec/kunpanel/releases/download/" + tag + "/SHA256SUMS"
	return []string{u, "https://gh-proxy.com/" + u}
}

// expectedHash 从 SHA256SUMS 里取目标资产的哈希(行格式:<hash>  ./<name> 或 <hash>  <name>)
func expectedHash(sums, asset string) string {
	for _, ln := range strings.Split(sums, "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && (f[1] == "./"+asset || f[1] == asset || strings.HasSuffix(f[1], "/"+asset)) {
			if len(f[0]) == 64 {
				return strings.ToLower(f[0])
			}
		}
	}
	return ""
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func assetURL(tag string) string {
	name := fmt.Sprintf("kunpanel_%s_%s_%s", tag, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".zip"
	} else {
		name += ".tar.gz"
	}
	return "https://github.com/" + Repo + "/releases/download/" + tag + "/" + name
}

// directGet 直连获取小文本(校验和清单),不经过加速代理
func directGet(url string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func download(url string, dst string, w io.Writer) error {
	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Get(url)
	if err != nil || resp.StatusCode != 200 {
		// 直连失败走加速代理重试一次
		if resp != nil {
			resp.Body.Close()
		}
		fmt.Fprintln(w, "直连失败,尝试加速代理 ...")
		resp, err = client.Get("https://gh-proxy.com/" + url)
		if err != nil {
			return fmt.Errorf("下载失败: %v", err)
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
		}
	}
	defer resp.Body.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return fmt.Errorf("下载中断: %v", err)
	}
	fmt.Fprintf(w, "已下载 %.1f MB\n", float64(n)/1024/1024)
	if n < 1<<20 {
		return fmt.Errorf("下载内容异常(小于 1MB)")
	}
	return nil
}

// Apply 下载指定版本的安装包,替换本机二进制并触发服务重启。
// 重启通过脱离的子进程执行,面板进程先正常返回 HTTP 响应再退出。
func Apply(tag string, w io.Writer) error {
	tag = strings.TrimSpace(tag)
	if _, ok := Semver(tag); !ok {
		return fmt.Errorf("非法版本号: %s", tag)
	}
	fmt.Fprintln(w, "下载 "+tag+" ...")
	archive := filepath.Join(os.TempDir(), fmt.Sprintf("kunpanel_update_%d%s", time.Now().UnixNano(), map[bool]string{true: ".zip", false: ".tar.gz"}[runtime.GOOS == "windows"]))
	if err := download(assetURL(tag), archive, w); err != nil {
		return err
	}
	defer os.Remove(archive)

	// 完整性校验:安装包哈希必须与 GitHub 直连获取的 SHA256SUMS 一致,
	// 代理/发布链路被篡改时在此中止,不落盘不替换
	wantAsset := fmt.Sprintf("kunpanel_%s_%s_%s.tar.gz", tag, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		wantAsset = fmt.Sprintf("kunpanel_%s_windows_amd64.zip", tag)
	}
	fmt.Fprintln(w, "校验安装包完整性(SHA-256)...")
	// 校验和清单:依次尝试直连 GitHub 与 gh-proxy。直连成功=完整供应链信任;
	// 仅代理可用时仍校验(防传输损坏),但提示信任来源为代理
	var sums string
	var sumErr error
	sumVia := "直连 GitHub"
	for i, u := range checksumsURLs(tag) {
		if i > 0 {
			sumVia = "gh-proxy 加速代理"
		}
		sums, sumErr = directGet(u)
		if sumErr == nil {
			break
		}
	}
	if sumErr != nil {
		return fmt.Errorf("获取官方校验和失败(直连与代理均不可用),已中止升级: %v", sumErr)
	}
	if want := expectedHash(sums, wantAsset); want == "" {
		return fmt.Errorf("官方校验和中没有 %s 的记录,已中止升级", wantAsset)
	} else if got, err := fileHash(archive); err != nil {
		return fmt.Errorf("计算安装包哈希失败: %v", err)
	} else if got != want {
		return fmt.Errorf("安装包校验失败(期望 %s... 实际 %s...),已中止升级:发布链路可能被篡改", want[:12], got[:12])
	}
	if sumVia != "直连 GitHub" {
		fmt.Fprintln(w, "注意: 校验和经加速代理获取(仅防传输损坏,供应链信任弱于直连)")
	}
	fmt.Fprintln(w, "校验通过")

	workdir := filepath.Join(os.TempDir(), fmt.Sprintf("kunpanel_update_%d", time.Now().UnixNano()))
	_ = os.MkdirAll(workdir, 0o755)
	defer os.RemoveAll(workdir)

	fmt.Fprintln(w, "解压 ...")
	if runtime.GOOS == "windows" {
		if err := unzip(archive, workdir); err != nil {
			return fmt.Errorf("解压失败: %v", err)
		}
	} else {
		if out, err := util.Run("tar", "-xzf", archive, "-C", workdir); err != nil {
			return fmt.Errorf("解压失败: %v %s", err, strings.TrimSpace(out))
		}
	}

	// 在解压目录里找 kunpanel[.exe]
	var newPath string
	binName := "kunpanel"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	filepath.Walk(workdir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && filepath.Base(p) == binName {
			newPath = p
			return filepath.SkipAll
		}
		return nil
	})
	if newPath == "" {
		return fmt.Errorf("安装包内未找到 %s", binName)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身失败: %v", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	if runtime.GOOS == "windows" {
		// Windows 运行中的 exe 不能覆盖但可以改名:旧→.old,新→原名
		old := self + ".old"
		_ = os.Remove(old)
		if err := os.Rename(self, old); err != nil {
			return fmt.Errorf("备份旧程序失败: %v", err)
		}
		if err := copyFile(newPath, self); err != nil {
			_ = os.Rename(old, self) // 回滚
			return fmt.Errorf("写入新程序失败: %v", err)
		}
	} else {
		tmp := self + ".new"
		if err := copyFile(newPath, tmp); err != nil {
			return fmt.Errorf("写入新程序失败: %v", err)
		}
		if err := os.Chmod(tmp, 0o755); err != nil {
			return fmt.Errorf("chmod 失败: %v", err)
		}
		// 原子替换:旧版保留为 .bak 供回滚
		bak := self + ".bak"
		_ = os.Remove(bak)
		_ = os.Rename(self, bak)
		if err := os.Rename(tmp, self); err != nil {
			_ = os.Rename(bak, self)
			return fmt.Errorf("替换失败: %v", err)
		}
	}
	fmt.Fprintln(w, "✅ 已替换程序,即将重启面板服务 ...")

	// 脱离子进程执行重启(面板进程先返回响应再被重启)
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", "timeout /t 2 /nobreak >nul & sc stop kunpanel & timeout /t 2 /nobreak >nul & sc start kunpanel")
	} else {
		cmd = exec.Command("sh", "-c", "sleep 1; systemctl restart kunpanel 2>/dev/null || rc-service kunpanel restart 2>/dev/null || (pkill -f '"+self+"' ; nohup '"+self+"' >> /opt/kunpanel/logs/panel.log 2>&1 &)")
	}
	cmd.SysProcAttr = detached()
	_ = cmd.Start()
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func unzip(archive, dir string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		p := filepath.Join(dir, f.Name)
		if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(dir)+string(os.PathSeparator)) {
			continue // 防 ZipSlip
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(p, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		src, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			src.Close()
			return err
		}
		_, err = io.Copy(dst, src)
		src.Close()
		dst.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
