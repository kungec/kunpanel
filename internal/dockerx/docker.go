package dockerx

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/util"
)

type Container struct {
	ID      string `json:"id"`
	Names   string `json:"names"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Ports   string `json:"ports"`
	Created string `json:"created"`
}

type Image struct {
	ID        string `json:"id"`
	Repo      string `json:"repository"`
	Tag       string `json:"tag"`
	Size      string `json:"size"`
	CreatedAt string `json:"created_at"`
}

type NetResource struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
	Scope  string `json:"scope"`
	Detail string `json:"detail"`
}

// Installed 检查 docker CLI 与引擎是否可用
func Installed() (cliOK, engineOK bool) {
	if _, err := exec.LookPath("docker"); err != nil {
		return false, false
	}
	cliOK = true
	out, err := util.RunWithTimeout(8*time.Second, "docker", "info", "--format", "{{.ServerVersion}}")
	engineOK = err == nil && strings.TrimSpace(out) != ""
	return
}

func Version() string {
	out, err := util.RunWithTimeout(8*time.Second, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

var (
	infoMu    sync.Mutex
	infoCache map[string]any
	infoAt    time.Time
)

// Info Docker 引擎概要。dashboard 被前端每 5 秒轮询,而每次计算要拉起
// 3 个以上 docker CLI 子进程,故加 15 秒 TTL 缓存;容器增删后最多延迟
// 15 秒反映到概览数字。
func Info() map[string]any {
	infoMu.Lock()
	defer infoMu.Unlock()
	if infoCache != nil && time.Since(infoAt) < 30*time.Second {
		return infoCache
	}
	res := map[string]any{"installed": false, "running": false, "version": "", "images": 0, "containers": 0, "runningCount": 0}
	cliOK, engineOK := Installed()
	res["installed"] = cliOK
	if !engineOK {
		// CLI 在但引擎检查失败(如负载下子进程超时):只短缓存 3s,避免"未安装"假状态持续 30s
		if !cliOK {
			infoCache = res
			infoAt = time.Now()
		} else {
			infoCache = res
			infoAt = time.Now().Add(-27 * time.Second)
		}
		return res
	}
	res["running"] = true
	res["version"] = Version()
	if out, err := util.RunWithTimeout(8*time.Second, "docker", "images", "-q"); err == nil {
		res["images"] = len(nonEmptyLines(out))
	}
	// 一次 ps -a 同时取容器总数与运行数(原为两次调用)
	if out, err := util.RunWithTimeout(8*time.Second, "docker", "ps", "-a", "--format", "{{.State}}"); err == nil {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		total, running := 0, 0
		for _, ln := range lines {
			if strings.TrimSpace(ln) == "" {
				continue
			}
			total++
			if strings.HasPrefix(ln, "running") {
				running++
			}
		}
		res["containers"] = total
		res["runningCount"] = running
	}
	infoCache = res
	infoAt = time.Now()
	return res
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func Containers() ([]Container, error) {
	out, err := util.RunWithTimeout(15*time.Second, "docker", "ps", "-a", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker ps 失败: %v %s", err, out)
	}
	var list []Container
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// Docker 29 的 json 输出含嵌套对象字段(如 Platform),须用 any 解析,
		// map[string]string 会整体反序列化失败导致列表恒为空
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		get := func(k string) string {
			if v, ok := raw[k].(string); ok {
				return v
			}
			return ""
		}
		list = append(list, Container{
			ID:      cutID(get("ID")),
			Names:   strings.TrimPrefix(get("Names"), "/"),
			Image:   get("Image"),
			State:   get("State"),
			Status:  get("Status"),
			Ports:   get("Ports"),
			Created: get("CreatedAt"),
		})
	}
	return list, nil
}

func cutID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func ContainerAction(id, action string) error {
	switch action {
	case "start", "stop", "restart", "pause", "unpause":
	case "kill":
	default:
		return fmt.Errorf("不支持的操作: %s", action)
	}
	out, err := util.RunWithTimeout(30*time.Second, "docker", action, id)
	if err != nil {
		return fmt.Errorf("%s 失败: %s", action, strings.TrimSpace(out))
	}
	return nil
}

func ContainerRemove(id string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, id)
	out, err := util.RunWithTimeout(30*time.Second, "docker", args...)
	if err != nil {
		return fmt.Errorf("删除容器失败: %s", strings.TrimSpace(out))
	}
	return nil
}

func Logs(id string, tail int) (string, error) {
	if tail <= 0 || tail > 5000 {
		tail = 200
	}
	out, err := util.RunWithTimeout(20*time.Second, "docker", "logs", "--tail", strconv.Itoa(tail), id)
	if err != nil && out == "" {
		return "", err
	}
	return out, nil
}

func Images() ([]Image, error) {
	out, err := util.RunWithTimeout(15*time.Second, "docker", "images", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker images 失败: %s", out)
	}
	var list []Image
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		var raw map[string]string
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			continue
		}
		list = append(list, Image{
			ID: cutID(raw["ID"]), Repo: raw["Repository"], Tag: raw["Tag"],
			Size: raw["Size"], CreatedAt: raw["CreatedAt"],
		})
	}
	return list, nil
}

func ImageRemove(id string) error {
	out, err := util.RunWithTimeout(30*time.Second, "docker", "rmi", "-f", id)
	if err != nil {
		return fmt.Errorf("删除镜像失败: %s", strings.TrimSpace(out))
	}
	return nil
}

func ImagePull(repo string, w io.Writer) error {
	return stream("docker", []string{"pull", repo}, w, 30*time.Minute)
}

func Networks() ([]NetResource, error) {
	out, err := util.RunWithTimeout(15*time.Second, "docker", "network", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var list []NetResource
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			continue
		}
		get := func(k string) string {
			if v, ok := raw[k].(string); ok {
				return v
			}
			return ""
		}
		list = append(list, NetResource{Name: get("Name"), Driver: get("Driver"), Scope: get("Scope")})
	}
	return list, nil
}

func Volumes() ([]NetResource, error) {
	out, err := util.RunWithTimeout(15*time.Second, "docker", "volume", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var list []NetResource
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			continue
		}
		get := func(k string) string {
			if v, ok := raw[k].(string); ok {
				return v
			}
			return ""
		}
		list = append(list, NetResource{Name: get("Name"), Driver: get("Driver")})
	}
	return list, nil
}

// ComposeBin 查找可用的 compose 命令(docker compose v2 优先,回退 docker-compose)
func ComposeBin() ([]string, error) {
	if _, err := exec.LookPath("docker"); err == nil {
		if err := exec.Command("docker", "compose", "version").Run(); err == nil {
			return []string{"docker", "compose"}, nil
		}
	}
	if _, err := exec.LookPath("docker-compose"); err == nil {
		return []string{"docker-compose"}, nil
	}
	return nil, fmt.Errorf("未找到 docker compose,请先安装 Docker(含 compose 插件)")
}

// Compose 在指定目录执行 compose 子命令
func Compose(dir string, args ...string) (string, error) {
	bin, err := ComposeBin()
	if err != nil {
		return "", err
	}
	full := append(bin, "-f", dirComposeFile(dir))
	full = append(full, "--project-directory", dir)
	full = append(full, args...)
	out, err := util.RunWithTimeout(20*time.Minute, full[0], full[1:]...)
	return out, err
}

func dirComposeFile(dir string) string {
	return dir + string(os.PathSeparator) + "docker-compose.yml"
}

// ContainerRunning 判断指定名称的容器是否在运行
func ContainerRunning(name string) bool {
	out, err := util.RunWithTimeout(8*time.Second, "docker", "ps", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
	return err == nil && strings.TrimSpace(out) != ""
}

// ReloadOpenResty 在面板管理的 OpenResty 容器内平滑重载 nginx
func ReloadOpenResty() (string, error) {
	return util.RunWithTimeout(30*time.Second, "docker", "exec", "kun-openresty", "nginx", "-s", "reload")
}

// PullImage 拉取镜像(流式输出到 w)
func PullImage(image string, w io.Writer) error {
	return stream("docker", []string{"pull", image}, w, 30*time.Minute)
}

// PruneDanglingImages 清理悬空镜像
func PruneDanglingImages() (string, error) {
	return util.RunWithTimeout(10*time.Minute, "docker", "image", "prune", "-f")
}

// ComposeValidate 用 docker compose config 校验 compose 文件
func ComposeValidate(composeFile string) (string, error) {
	return util.RunWithTimeout(60*time.Second, "docker", "compose", "-f", composeFile, "config", "--quiet")
}

// ContainerStat 单容器资源占用(docker stats 快照)
type ContainerStat struct {
	Name     string `json:"name"`
	CPUPerc  string `json:"cpu"`
	MemUsage string `json:"mem"`
	MemPerc  string `json:"mem_perc"`
	NetIO    string `json:"net_io"`
	BlockIO  string `json:"block_io"`
}

// Stats 一次性采集全部运行中容器的资源占用
func Stats() ([]ContainerStat, error) {
	out, err := util.RunWithTimeout(25*time.Second, "docker", "stats", "--no-stream", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker stats 失败: %s", tail(out, 300))
	}
	var list []ContainerStat
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		var raw map[string]string
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			continue
		}
		list = append(list, ContainerStat{
			Name:     raw["Name"],
			CPUPerc:  raw["CPUPerc"],
			MemUsage: raw["MemUsage"],
			MemPerc:  raw["MemPerc"],
			NetIO:    raw["NetIO"],
			BlockIO:  raw["BlockIO"],
		})
	}
	return list, nil
}

// stream 流式执行命令,输出写入 w
func stream(name string, args []string, w io.Writer, timeout time.Duration) error {
	cmd := exec.Command(name, args...)
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			if w != nil {
				fmt.Fprintln(w, sc.Text())
			}
		}
		done <- cmd.Wait()
	}()
	select {
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return fmt.Errorf("命令超时")
	case err := <-done:
		return err
	}
}

// ---------- Docker 引擎一键安装 ----------

// DetectPkgManager 识别发行版包管理器(依据 /etc/os-release)
func DetectPkgManager() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	for _, pm := range []string{"apt-get", "dnf", "yum", "zypper", "pacman", "apk"} {
		if _, err := exec.LookPath(pm); err == nil {
			return pm
		}
	}
	return ""
}

// InstallEngine 一键安装 Docker 引擎(仅 Linux)。优先官方脚本,失败后按发行版回退。
func InstallEngine(mirror string, w io.Writer) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("仅在 Linux 上支持在线安装 Docker")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		if pm := DetectPkgManager(); pm != "" {
			fmt.Fprintln(w, "未检测到 curl,先通过包管理器安装 curl ...")
			_ = pmInstall(pm, "curl")
		}
	}

	// 1) 官方一键脚本(get.docker.com),--mirror aliyun 使用国内源
	if mirror == "aliyun" {
		fmt.Fprintln(w, ">> 使用阿里云镜像源安装 Docker ...")
		cmd := exec.Command("sh", "-c", "curl -fsSL https://get.docker.com | sh -s -- --mirror Aliyun")
		cmd.Stdout = w
		cmd.Stderr = w
		if err := cmd.Run(); err == nil {
			return ensureEngineRunning(w)
		} else {
			fmt.Fprintln(w, "阿里云源安装失败:"+err.Error()+",尝试系统包管理器回退 ...")
		}
	} else {
		fmt.Fprintln(w, ">> 通过官方脚本 get.docker.com 安装 Docker ...")
		if err := stream("sh", []string{"-c", "curl -fsSL https://get.docker.com | sh"}, w, 30*time.Minute); err == nil {
			return ensureEngineRunning(w)
		} else {
			fmt.Fprintln(w, "官方脚本安装失败:"+err.Error()+",尝试系统包管理器回退 ...")
		}
	}

	// 2) 包管理器回退
	pm := DetectPkgManager()
	if pm == "" {
		return fmt.Errorf("未识别的发行版,请手动安装 Docker 后重试")
	}
	switch pm {
	case "apt-get":
		if err := pmInstall(pm, "docker.io", "docker-compose-v2"); err != nil {
			return fmt.Errorf("apt 安装失败: %v", err)
		}
	case "dnf", "yum":
		if err := pmInstall(pm, "docker"); err != nil {
			_ = pmInstall(pm, "moby-engine", "docker-cli")
		}
		// 尝试添加 docker-ce 官方源
		_ = pmInstall(pm, "docker-ce", "docker-ce-cli", "containerd.io", "docker-compose-plugin")
	case "apk":
		if err := pmInstall(pm, "docker", "docker-cli-compose"); err != nil {
			return fmt.Errorf("apk 安装失败: %v", err)
		}
	case "pacman":
		if err := pmInstall(pm, "docker", "docker-compose"); err != nil {
			return fmt.Errorf("pacman 安装失败: %v", err)
		}
	case "zypper":
		if err := pmInstall(pm, "docker", "docker-compose"); err != nil {
			return fmt.Errorf("zypper 安装失败: %v", err)
		}
	}
	return ensureEngineRunning(w)
}

func pmInstall(pm string, pkgs ...string) error {
	var cmd *exec.Cmd
	switch pm {
	case "apt-get":
		_, _ = util.RunWithTimeout(10*time.Minute, "apt-get", "update")
		cmd = exec.Command("sh", "-c", "DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "+strings.Join(pkgs, " "))
	case "dnf", "yum":
		cmd = exec.Command(pm, append([]string{"install", "-y"}, pkgs...)...)
	case "apk":
		cmd = exec.Command("apk", append([]string{"add", "--no-cache"}, pkgs...)...)
	case "pacman":
		cmd = exec.Command("pacman", append([]string{"-Sy", "--noconfirm"}, pkgs...)...)
	case "zypper":
		cmd = exec.Command("zypper", append([]string{"--non-interactive", "install"}, pkgs...)...)
	default:
		return fmt.Errorf("未知包管理器 %s", pm)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, tail(string(out), 400))
	}
	return nil
}

func ensureEngineRunning(w io.Writer) error {
	fmt.Fprintln(w, ">> 启动 Docker 服务 ...")
	// systemd
	if _, err := exec.LookPath("systemctl"); err == nil {
		_, _ = util.RunWithTimeout(60*time.Second, "systemctl", "enable", "--now", "docker")
	} else {
		// OpenRC (Alpine 等)
		_, _ = util.RunWithTimeout(30*time.Second, "rc-update", "add", "docker", "boot")
		_, _ = util.RunWithTimeout(60*time.Second, "rc-service", "docker", "start")
		if !ContainerRunningDockerd() {
			_, _ = util.Run("service", "docker", "start")
		}
	}
	// 等待引擎就绪
	for i := 0; i < 20; i++ {
		if _, ok := Installed(); ok {
			v := Version()
			if v != "" {
				fmt.Fprintf(w, ">> Docker %s 安装并启动成功\n", v)
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("Docker 已安装但引擎未就绪,请手动检查服务状态")
}

func ContainerRunningDockerd() bool {
	out, err := util.RunWithTimeout(5*time.Second, "pgrep", "-x", "dockerd")
	return err == nil && strings.TrimSpace(out) != ""
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ComposeStream 流式执行 docker compose,输出实时写入 w(任务日志可见)
func ComposeStream(dir string, w io.Writer, args ...string) error {
	bin, err := ComposeBin()
	if err != nil {
		return err
	}
	full := append(bin, "-f", dirComposeFile(dir))
	full = append(full, "--project-directory", dir)
	full = append(full, args...)
	return util.RunWithTimeoutWriter(20*time.Minute, w, full[0], full[1:]...)
}

// ExecIn 在容器内执行命令并返回输出
func ExecIn(container string, cmd ...string) (string, error) {
	full := append([]string{"exec", container}, cmd...)
	return util.RunWithTimeout(30*time.Second, "docker", full...)
}
