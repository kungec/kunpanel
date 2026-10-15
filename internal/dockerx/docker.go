package dockerx

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
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

var (
	instMu       sync.Mutex
	instCachedOK bool
	instCachedEn bool
	instAt       time.Time
)

// Installed 检查 docker CLI 与引擎是否可用。结果短缓存:成功 15s、失败 3s
// (失败短缓存避免引擎恢复前的"未安装"假状态持续过久)。无缓存时列表类
// 接口每次请求都会拉起 docker info 子进程,引擎繁忙时请求被拖到秒级以上。
func Installed() (cliOK, engineOK bool) {
	instMu.Lock()
	if time.Since(instAt) < 15*time.Second && (instCachedOK || time.Since(instAt) < 3*time.Second) {
		defer instMu.Unlock()
		return instCachedOK, instCachedEn
	}
	instMu.Unlock()

	cliOK, engineOK = installedReal()

	instMu.Lock()
	instCachedOK, instCachedEn = cliOK, engineOK
	instAt = time.Now()
	instMu.Unlock()
	return cliOK, engineOK
}

func installedReal() (cliOK, engineOK bool) {
	if _, err := exec.LookPath("docker"); err != nil {
		return false, false
	}
	cliOK = true
	out, err := util.RunWithTimeout(8*time.Second, "docker", "info", "--format", "{{.ServerVersion}}")
	engineOK = err == nil && strings.TrimSpace(out) != ""
	return
}

// ContainerStates 一次 docker ps -a 返回全部容器状态(name → docker 状态字符串,
// 如 running/exited/paused)。不在结果中 = 容器不存在(被删除)。
func ContainerStates() map[string]string {
	res := map[string]string{}
	out, err := util.RunWithTimeout(10*time.Second, "docker", "ps", "-a", "--format", "{{.Names}}\t{{.State}}")
	if err != nil {
		return res
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) != 2 {
			continue
		}
		res[parts[0]] = parts[1]
	}
	return res
}

// ContainerExists 容器是否存在(含已停止)
func ContainerExists(name string) bool {
	_, ok := ContainerStates()[name]
	return ok
}

// RunningStates 批量返回容器运行状态(name→running)。查询不存在的容器返回 false;
// 需要区分"已停止"与"已被删除"时请用 ContainerStates。
func RunningStates(names ...string) map[string]bool {
	res := make(map[string]bool, len(names))
	states := ContainerStates()
	for _, n := range names {
		res[n] = states[n] == "running"
	}
	return res
}

func Version() string {
	out, err := util.RunWithTimeout(8*time.Second, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

var (
	infoMu     sync.Mutex
	infoCache  map[string]any
	infoAt     time.Time
	infoBusy   bool // 已有后台刷新在跑
)

// Info Docker 引擎概要。每次计算要拉起 3 个以上 docker CLI 子进程(每个超时 8s),
// 旧实现持锁同步执行:docker 引擎一慢,所有 dashboard 请求串行排队最长 30s+,
// 前端轮询堆积后表现为面板整体卡死。现在改为:缓存有效直接返回;过期但有旧值时
// 立即返回旧值并由后台单飞协程刷新(请求路径零阻塞);仅首次无缓存才同步计算。
func Info() map[string]any {
	infoMu.Lock()
	if infoCache != nil && time.Since(infoAt) < 30*time.Second {
		c := infoCache
		infoMu.Unlock()
		return c
	}
	if infoCache != nil {
		// 有旧值:立即返回旧值,过期数据由后台单飞刷新,请求永不等待子进程
		if !infoBusy {
			infoBusy = true
			go func() {
				res := computeInfo()
				infoMu.Lock()
				infoCache = res
				infoAt = time.Now()
				infoBusy = false
				infoMu.Unlock()
			}()
		}
		c := infoCache
		infoMu.Unlock()
		return c
	}
	infoMu.Unlock()
	// 冷启动首次调用:同步算一次(子进程自带 8s 超时,仅发生一次)
	res := computeInfo()
	infoMu.Lock()
	infoCache = res
	infoAt = time.Now()
	infoMu.Unlock()
	return res
}

// computeInfo 实际执行 docker CLI 采集,不持任何锁调用子进程。
func computeInfo() map[string]any {
	res := map[string]any{"installed": false, "running": false, "version": "", "images": 0, "containers": 0, "runningCount": 0}
	cliOK, engineOK := Installed()
	res["installed"] = cliOK
	if !engineOK {
		// CLI 在但引擎检查失败(如负载下子进程超时):把时间戳回拨,
		// 让下次请求立即触发重刷,避免"未安装"假状态持续 30s
		if cliOK {
			infoMu.Lock()
			infoAt = time.Now().Add(-27 * time.Second)
			infoMu.Unlock()
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
		list = append(list, NetResource{Name: get("Name"), Driver: get("Driver"), Scope: get("Scope")})
	}
	return list, nil
}

// VolumeRemove 删除数据卷。被容器占用的卷 docker 会拒绝,错误原样返回给用户。
func VolumeRemove(name string) error {
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(name) {
		return fmt.Errorf("卷名不合法")
	}
	out, err := util.RunWithTimeout(15*time.Second, "docker", "volume", "rm", name)
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
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

// repoMirror 国内 Docker CE 软件源(顺序即优先级)。repo 文件由各镜像站发布,
// baseurl 本身指向镜像站自身域名,拉取成功即可用。
var repoMirrors = []struct {
	Name string
	Yum  string // CentOS/RHEL 系 docker-ce.repo
	Apt  string // Debian/Ubuntu 系(模板,含 %s=distro)
}{
	{"阿里云", "https://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo", "https://mirrors.aliyun.com/docker-ce/linux/%s/"},
	{"腾讯云", "https://mirrors.tencent.com/docker-ce/linux/centos/docker-ce.repo", "https://mirrors.tencent.com/docker-ce/linux/%s/"},
	{"清华大学", "https://mirrors.tuna.tsinghua.edu.cn/docker-ce/linux/centos/docker-ce.repo", "https://mirrors.tuna.tsinghua.edu.cn/docker-ce/linux/%s/"},
	{"中科大", "https://mirrors.ustc.edu.cn/docker-ce/linux/centos/docker-ce.repo", "https://mirrors.ustc.edu.cn/docker-ce/linux/%s/"},
}

// urlReachable 探测 URL 可达性(6s 超时;HTTP 4xx/5xx 或连接失败均视为不可达)
func urlReachable(url string) bool {
	_, err := util.RunWithTimeout(6*time.Second, "sh", "-c",
		"curl -fsSL -m 5 -o /dev/null "+url)
	return err == nil
}

// osRelease 解析 /etc/os-release,供 apt 源拼接 distro(debian/ubuntu)
func osRelease() map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		out[kv[0]] = strings.Trim(kv[1], `"'`)
	}
	return out
}

// installFromChinaYumRepo 为 yum/dnf 系逐个尝试国内 docker-ce 源并安装。
// 逐源验证 repo 文件拉取与 makecache,全部失败才报错。
func installFromChinaYumRepo(pm string, force string, w io.Writer) error {
	for _, m := range repoMirrors {
		if force != "" && m.Name != force && !(force == "ustc" && m.Name == "中科大") {
			continue
		}
		fmt.Fprintf(w, ">> 尝试国内源[%s]:拉取 docker-ce.repo ... ", m.Name)
		if _, err := util.RunWithTimeout(15*time.Second, "sh", "-c",
			"curl -fsSL -m 12 -o /etc/yum.repos.d/docker-ce.repo "+m.Yum); err != nil {
			fmt.Fprintln(w, "失败(不可达)")
			continue
		}
		fmt.Fprintln(w, "OK")
		// 刷新该源元数据(只启用 docker-ce,避免拖慢)
		_, _ = util.RunWithTimeout(3*time.Minute, "sh", "-c", pm+" makecache -y --disablerepo=* --enablerepo=docker-ce*")
		pkgs := []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-compose-plugin"}
		fmt.Fprintf(w, ">> 从[%s]安装 docker-ce ... \n", m.Name)
		if err := pmInstall(pm, pkgs...); err != nil {
			fmt.Fprintf(w, "[%s] docker-ce 安装失败:%v,尝试下一个源 ...\n", m.Name, err)
			continue
		}
		return nil
	}
	return fmt.Errorf("所有国内源均不可用")
}

// installFromChinaAptRepo 为 apt 系逐个尝试国内 docker-ce 源并安装
func installFromChinaAptRepo(force string, w io.Writer) error {
	osi := osRelease()
	distro := osi["ID"]
	if distro != "debian" && distro != "ubuntu" {
		// ID_LIKE 兜底(如 linuxmint → ubuntu)
		if strings.Contains(osi["ID_LIKE"], "debian") {
			distro = "debian"
		} else if strings.Contains(osi["ID_LIKE"], "ubuntu") || distro == "linuxmint" {
			distro = "ubuntu"
		} else {
			return fmt.Errorf("不支持的发行版: %s", osi["ID"])
		}
	}
	codename := osi["VERSION_CODENAME"]
	if codename == "" && distro == "ubuntu" {
		codename = osi["UBUNTU_CODENAME"]
	}
	if codename == "" {
		return fmt.Errorf("无法识别系统代号(VERSION_CODENAME 为空)")
	}
	_, _ = util.RunWithTimeout(15*time.Second, "sh", "-c", "install -m 0755 -d /etc/apt/keyrings")
	for _, m := range repoMirrors {
		if force != "" && m.Name != force && !(force == "ustc" && m.Name == "中科大") {
			continue
		}
		base := fmt.Sprintf(m.Apt, distro)
		fmt.Fprintf(w, ">> 尝试国内源[%s]:配置 apt 源 ... ", m.Name)
		script := fmt.Sprintf(
			`set -e; curl -fsSL -m 12 %s/gpg -o /etc/apt/keyrings/docker.asc && chmod a+r /etc/apt/keyrings/docker.asc && `+
				`echo "deb [arch=$(dpkg --print-architecture 2>/dev/null || echo amd64) signed-by=/etc/apt/keyrings/docker.asc] %s %s stable" > /etc/apt/sources.list.d/docker.list`,
			base, base, codename)
		if _, err := util.RunWithTimeout(30*time.Second, "sh", "-c", script); err != nil {
			fmt.Fprintln(w, "失败(不可达)")
			continue
		}
		fmt.Fprintln(w, "OK")
		_, _ = util.RunWithTimeout(5*time.Minute, "apt-get", "update")
		if err := pmInstall("apt-get", "docker-ce", "docker-ce-cli", "containerd.io", "docker-compose-plugin"); err != nil {
			fmt.Fprintf(w, "[%s] 安装失败:%v,尝试下一个源 ...\n", m.Name, err)
			continue
		}
		return nil
	}
	return fmt.Errorf("所有国内源均不可用")
}

// applyChinaRegistryMirrors 国内网络下为 docker pull 配置公共镜像加速器。
// 仅在用户未配置 registry-mirrors 时写入,原文件备份为 daemon.json.bak。
func applyChinaRegistryMirrors(w io.Writer) {
	path := "/etc/docker/daemon.json"
	var cfg map[string]any
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &cfg) != nil {
			fmt.Fprintln(w, ">> 跳过镜像加速配置:daemon.json 不是合法 JSON,请手动检查")
			return
		}
		if _, ok := cfg["registry-mirrors"]; ok {
			return // 用户已有配置,不覆盖
		}
		_ = os.WriteFile(path+".bak", b, 0o644)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["registry-mirrors"] = []string{
		"https://docker.m.daocloud.io",
		"https://docker.1panel.live",
		"https://docker.1ms.run",
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	if err := util.AtomicWrite(path, out, 0o644); err != nil {
		fmt.Fprintln(w, ">> 镜像加速配置写入失败:"+err.Error())
		return
	}
	fmt.Fprintln(w, ">> 已配置 Docker 镜像拉取加速(国内加速器,原配置备份于 daemon.json.bak)")
	// 已在运行的引擎需要重启加载
	if ContainerRunningDockerd() {
		_, _ = util.RunWithTimeout(60*time.Second, "sh", "-c",
			"systemctl restart docker 2>/dev/null || service docker restart 2>/dev/null || true")
	}
}

// InstallEngine 一键安装 Docker 引擎(仅 Linux)。
// mirror: ""=强制官方源;auto=探测后自动选择(官方可达走官方,不通自动切国内源);
// aliyun/ustc=强制指定国内源。官方脚本失败或官方源不可达时,逐个尝试国内
// docker-ce 软件源(阿里云→腾讯云→清华→中科大),不再依赖官方脚本可达。
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
	force := ""
	if mirror == "aliyun" {
		force = "阿里云"
	} else if mirror == "ustc" {
		force = "中科大"
	}

	// 官方源连通性:download.docker.com 在国内大量机房被重置,探测通过才走官方
	officialOK := force == "" && urlReachable("https://download.docker.com/linux/centos/docker-ce.repo")

	if officialOK {
		fmt.Fprintln(w, ">> 通过官方脚本 get.docker.com 安装 Docker ...")
		if err := stream("sh", []string{"-c", "curl -fsSL https://get.docker.com | sh"}, w, 30*time.Minute); err == nil {
			return ensureEngineRunning(w)
		}
		fmt.Fprintln(w, "官方脚本安装失败,尝试系统包管理器回退 ...")
	} else {
		if force == "" {
			fmt.Fprintln(w, ">> 检测到 Docker 官方源不可达(国内机房常见),自动切换国内镜像源 ...")
		}
		// 1) 官方一键脚本 + --mirror Aliyun(脚本域名 get.docker.com 与软件源域名不同,可能单独可达)
		if getScript := urlReachable("https://get.docker.com"); getScript {
			fmt.Fprintln(w, ">> 使用阿里云镜像模式执行官方脚本 ...")
			cmd := exec.Command("sh", "-c", "curl -fsSL https://get.docker.com | sh -s -- --mirror Aliyun")
			cmd.Stdout = w
			cmd.Stderr = w
			if err := cmd.Run(); err == nil {
				applyChinaRegistryMirrors(w)
				return ensureEngineRunning(w)
			}
			fmt.Fprintln(w, "脚本安装失败,改用手动配置国内软件源 ...")
		} else {
			fmt.Fprintln(w, "get.docker.com 也不可达,直接手动配置国内软件源 ...")
		}
		// 2) 手动配置国内 docker-ce 源(不依赖任何官方域名)
		pm := DetectPkgManager()
		var ierr error
		switch pm {
		case "yum", "dnf":
			ierr = installFromChinaYumRepo(pm, force, w)
		case "apt-get":
			ierr = installFromChinaAptRepo(force, w)
		default:
			ierr = fmt.Errorf("未识别的包管理器 %q,无法自动换源", pm)
		}
		if ierr != nil {
			fmt.Fprintln(w, "国内源安装失败:"+ierr.Error()+",尝试系统包管理器回退 ...")
		} else {
			applyChinaRegistryMirrors(w)
			return ensureEngineRunning(w)
		}
	}

	// 3) 包管理器回退(官方源可用但脚本失败 / 国内源全部失败)
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
	if force != "" || !officialOK {
		applyChinaRegistryMirrors(w)
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
