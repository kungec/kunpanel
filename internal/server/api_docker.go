package server

import (
	"io"
	"net/http"
	"strings"

	"encoding/json"
	"fmt"
	"os"
	"time"

	"kunpanel/internal/dockerx"
	"kunpanel/internal/taskx"
	"kunpanel/internal/util"
)

func (s *Server) handleDockerStatus(w http.ResponseWriter, r *http.Request) {
	OK(w, dockerx.Info())
}

// handleDockerStats 全部运行中容器的资源占用快照
func (s *Server) handleDockerStats(w http.ResponseWriter, r *http.Request) {
	list, err := dockerx.Stats()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []dockerx.ContainerStat{}
	}
	OK(w, list)
}

func (s *Server) handleDockerInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mirror string `json:"mirror"` // aliyun / 空
	}
	_ = readJSON(r, &req)
	if _, engineOK := dockerx.Installed(); engineOK {
		Fail(w, http.StatusConflict, "Docker 已安装且引擎运行中,无需重复安装")
		return
	}
	t, err := taskx.M.Start("docker-install", func(w io.Writer) error {
		return dockerx.InstallEngine(req.Mirror, w)
	})
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "already exists") || strings.Contains(msg, "installed") {
			Fail(w, http.StatusConflict, "Docker 已安装,无需重复安装;如面板未检测到,请重启面板后刷新页面")
			return
		}
		Fail(w, http.StatusConflict, msg)
		return
	}
	OK(w, t)
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	list, err := dockerx.Containers()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []dockerx.Container{}
	}
	OK(w, list)
}

func (s *Server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法容器 ID")
		return
	}
	action := r.PathValue("action")
	if action == "update" {
		s.handleContainerUpdate(w, r)
		return
	}
	if action == "logclear" {
		s.handleContainerLogClear(w, r)
		return
	}
	if err := dockerx.ContainerAction(id, action); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleContainerRemove(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法容器 ID")
		return
	}
	if err := dockerx.ContainerRemove(id, true); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法容器 ID")
		return
	}
	tail := atoiDefault(r.URL.Query().Get("tail"), 200)
	logs, err := dockerx.Logs(id, tail)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, strings.TrimRight(logs, "\n"))
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	list, err := dockerx.Images()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []dockerx.Image{}
	}
	OK(w, list)
}

func (s *Server) handleImageRemove(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法镜像 ID")
		return
	}
	if err := dockerx.ImageRemove(id); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	list, err := dockerx.Networks()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, list)
}

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	list, err := dockerx.Volumes()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []dockerx.NetResource{}
	}
	OK(w, list)
}

// handleVolumeRemove 删除数据卷(被容器占用时 docker 拒绝,错误透传)
func (s *Server) handleVolumeRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := dockerx.VolumeRemove(strings.TrimSpace(req.Name)); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handleContainerInfo 读取容器详细信息(inspect)
func (s *Server) handleContainerInfoDocker(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法容器 ID")
		return
	}
	out, err := util.RunWithTimeout(15*time.Second, "docker", "inspect", "--format", "{{json .}}", id)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "读取容器信息失败: "+err.Error())
		return
	}
	var raw struct {
		Name    string `json:"Name"`
		Created string `json:"Created"`
		Config  struct {
			Image string `json:"Image"`
		} `json:"Config"`
		State struct {
			Status       string `json:"Status"`
			StartedAt    string `json:"StartedAt"`
			RestartCount int    `json:"RestartCount"`
		} `json:"State"`
		HostConfig struct {
			PortBindings map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
			RestartPolicy struct {
				Name string `json:"Name"`
			} `json:"RestartPolicy"`
			Resources struct {
				NanoCPUs  int64 `json:"NanoCpus"`
				Memory    int64 `json:"Memory"`
			} `json:"Resources"`
		} `json:"HostConfig"`
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		Fail(w, http.StatusInternalServerError, "解析失败: "+err.Error())
		return
	}
	ports := map[string][]string{}
	for cport, binds := range raw.HostConfig.PortBindings {
		for _, b := range binds {
			ports[b.HostPort] = append(ports[b.HostPort], strings.TrimSuffix(cport, "/tcp"))
		}
	}
	mounts := make([]map[string]string, 0)
	for _, m := range raw.Mounts {
		mounts = append(mounts, map[string]string{"host": m.Source, "container": m.Destination})
	}
	// 资源限制(0 = 未限制)
	cpuLimit := float64(raw.HostConfig.Resources.NanoCPUs) / 1e9
	memLimitBytes := raw.HostConfig.Resources.Memory
	OK(w, map[string]any{
		"name":           raw.Name,
		"image":          raw.Config.Image,
		"status":         raw.State.Status,
		"started_at":     strClip(raw.State.StartedAt, 19),
		"restarts":       raw.State.RestartCount,
		"restart_policy": raw.HostConfig.RestartPolicy.Name,
		"cpu_limit":      cpuLimit,
		"mem_limit":      memLimitBytes,
		"ports":          ports,
		"mounts":         mounts,
	})
}

// handleContainerUpdate 编辑容器资源限制与重启策略(docker update)
func (s *Server) handleContainerUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法容器 ID")
		return
	}
	var req struct {
		CPUs          float64 `json:"cpus"`           // CPU 核数,0 = 不修改
		MemoryMB      int     `json:"memory_mb"`      // 内存 MB,0 = 不修改
		RestartPolicy string  `json:"restart_policy"` // no / always / unless-stopped / on-failure
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	var args []string
	if req.CPUs > 0 {
		if req.CPUs > 16 {
			Fail(w, http.StatusBadRequest, "CPU 上限 16 核")
			return
		}
		args = append(args, "--cpus", fmt.Sprintf("%.1f", req.CPUs))
	}
	if req.MemoryMB > 0 {
		if req.MemoryMB < 16 || req.MemoryMB > 65536 {
			Fail(w, http.StatusBadRequest, "内存限制应在 16MB-64GB 之间")
			return
		}
		// memoryswap >= memory 才能 Docker update 成功
		args = append(args, "--memory", fmt.Sprintf("%dm", req.MemoryMB), "--memory-swap", fmt.Sprintf("%dm", req.MemoryMB*2))
	}
	// 取消限制时也要清 memoryswap
	if req.MemoryMB == 0 && req.CPUs == 0 {
		args = append(args, "--memory", "0", "--memory-swap", "0", "--cpus", "0")
	}
	switch req.RestartPolicy {
	case "", "no", "always", "unless-stopped", "on-failure":
		if req.RestartPolicy != "" {
			args = append(args, "--restart", req.RestartPolicy)
		}
	default:
		Fail(w, http.StatusBadRequest, "非法重启策略")
		return
	}
	if len(args) == 0 {
		Fail(w, http.StatusBadRequest, "没有需要修改的项")
		return
	}
	out, err := util.RunWithTimeout(15*time.Second, "docker", append([]string{"update"}, append(args, id)...)...)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "更新失败: "+strings.TrimSpace(out))
		return
	}
	OK(w, nil)
}

// handleContainerLogClear 清空容器日志(truncate json-file 日志)
func (s *Server) handleContainerLogClear(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法容器 ID")
		return
	}
	// 找到容器的 json-file 日志路径并清空(不中断运行)
	out, err := util.RunWithTimeout(10*time.Second, "docker", "inspect", "--format",
		"{{.LogPath}}", id)
	if err != nil || strings.TrimSpace(out) == "" {
		Fail(w, http.StatusInternalServerError, "找不到容器日志路径")
		return
	}
	logPath := strings.TrimSpace(out)
	if _, err := os.Stat(logPath); err != nil {
		Fail(w, http.StatusInternalServerError, "日志文件不存在")
		return
	}
	// truncate 而非删除(容器持有文件句柄)
	if _, err := util.RunWithTimeout(5*time.Second, "truncate", "-s", "0", logPath); err != nil {
		Fail(w, http.StatusInternalServerError, "清理失败: "+err.Error())
		return
	}
	OK(w, nil)
}

// strClip 安全截断字符串(空值/过短不 panic)
func strClip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
