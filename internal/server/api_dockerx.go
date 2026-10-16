package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/appstore"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/taskx"
	"kunpanel/internal/util"
)

// imageRefRe 镜像引用白名单(仓库名:标签,防注入)
var imageRefRe = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,200}$`)

func validImageRef(s string) bool {
	return imageRefRe.MatchString(s) && !strings.Contains(s, "..")
}

func (s *Server) handleAppComposeGet(w http.ResponseWriter, r *http.Request) {
	key, ok := safeID(r.PathValue("key"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法应用标识")
		return
	}
	content, err := appstore.GetCompose(key)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, map[string]any{"key": key, "content": content})
}

type composeSaveReq struct {
	Content string `json:"content"`
}

func (s *Server) handleAppComposeSave(w http.ResponseWriter, r *http.Request) {
	key, ok := safeID(r.PathValue("key"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法应用标识")
		return
	}
	var req composeSaveReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := appstore.SaveCompose(key, req.Content); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

type dockerPullReq struct {
	Image string `json:"image"`
}

func (s *Server) handleDockerPull(w http.ResponseWriter, r *http.Request) {
	var req dockerPullReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	image := req.Image
	if !validImageRef(image) {
		Fail(w, http.StatusBadRequest, "镜像名称格式非法")
		return
	}
	t, err := taskx.M.Start("docker-pull-"+image, func(w io.Writer) error {
		return dockerx.PullImage(image, w)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, t)
}

type dockerPruneReq struct {
	Type string `json:"type"` // dangling
}

func (s *Server) handleDockerPrune(w http.ResponseWriter, r *http.Request) {
	var req dockerPruneReq
	if err := readJSON(r, &req); err != nil || req.Type != "dangling" {
		Fail(w, http.StatusBadRequest, "仅支持悬空镜像清理")
		return
	}
	out, err := dockerx.PruneDanglingImages()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]any{"output": out})
}

type containerCreateReq struct {
	Image   string            `json:"image"`
	Name    string            `json:"name"`
	Ports   []string          `json:"ports"`   // "8080:80"
	Volumes []string          `json:"volumes"` // "/host:/container"
	Env     map[string]string `json:"env"`
	Restart string            `json:"restart"` // always / unless-stopped / no
}

func (s *Server) handleDockerCreate(w http.ResponseWriter, r *http.Request) {
	var req containerCreateReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if !validImageRef(req.Image) {
		Fail(w, http.StatusBadRequest, "镜像名称格式非法")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		Fail(w, http.StatusBadRequest, "容器名不能为空")
		return
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9._-]{1,100}$`).MatchString(name) {
		Fail(w, http.StatusBadRequest, "容器名仅支持字母数字与 . _ -")
		return
	}
	// 通过 task 后台拉取并启动
	t, err := taskx.M.Start("docker-create-"+name, func(w io.Writer) error {
		fmt.Fprintln(w, "拉取镜像:", req.Image)
		if err := dockerx.PullImage(req.Image, w); err != nil {
			return err
		}
		args := []string{"run", "-d", "--name", name}
		switch req.Restart {
		case "always", "unless-stopped", "no":
			args = append(args, "--restart", req.Restart)
		default:
			args = append(args, "--restart", "unless-stopped")
		}
		for _, p := range req.Ports {
			p = strings.TrimSpace(p)
			if p != "" && !strings.ContainsAny(p, ";|&$") {
				args = append(args, "-p", p)
			}
		}
		for _, v := range req.Volumes {
			v = strings.TrimSpace(v)
			if v != "" && !strings.ContainsAny(v, ";|&$`") {
				args = append(args, "-v", v)
			}
		}
		for k, v := range req.Env {
			if k != "" && !strings.ContainsAny(k, ";|&$ ") && !strings.ContainsAny(v, ";|&$`") {
				args = append(args, "-e", k+"="+v)
			}
		}
		args = append(args, req.Image)
		fmt.Fprintln(w, "启动:", strings.Join(args, " "))
		out, e := util.RunWithTimeout(5*time.Minute, args[0], args[1:]...)
		fmt.Fprintln(w, out)
		return e
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, t)
}

// handleAppContainerInfo 通用容器信息(任意商店应用):状态/资源/端口/挂载
func (s *Server) handleAppContainerInfo(w http.ResponseWriter, r *http.Request) {
	key, ok := safeID(r.PathValue("key"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法应用标识")
		return
	}
	container := "kun-" + key
	if !dockerx.ContainerRunning(container) {
		Fail(w, http.StatusBadRequest, "应用未运行")
		return
	}
	out, err := util.RunWithTimeout(15*time.Second, "docker", "inspect", "--format", "{{json .}}", container)
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
		} `json:"HostConfig"`
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			Type        string `json:"Type"`
		} `json:"Mounts"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		Fail(w, http.StatusInternalServerError, "解析容器信息失败: "+err.Error())
		return
	}
	ports := map[string][]string{}
	for cport, binds := range raw.HostConfig.PortBindings {
		for _, b := range binds {
			ports[b.HostPort] = append(ports[b.HostPort], strings.TrimSuffix(cport, "/tcp"))
		}
	}
	mounts := make([]map[string]string, 0, len(raw.Mounts))
	for _, m := range raw.Mounts {
		if m.Type != "volume" && m.Type != "bind" {
			continue
		}
		mounts = append(mounts, map[string]string{"host": m.Source, "container": m.Destination})
	}
	// 资源占用(单次采样)
	cpu, mem, memLimit := "", "", ""
	st, serr := util.RunWithTimeout(25*time.Second, "docker", "stats", "--no-stream", "--format", "{{.CPUPerc}};{{.MemUsage}}", container)
	// 输出形如 "0.15%;201.2MiB / 1.921GiB"(1 个分号,两段)
	if serr == nil {
		parts := strings.SplitN(strings.TrimSpace(st), ";", 2)
		if len(parts) == 2 {
			cpu = strings.TrimSpace(parts[0])
			memParts := strings.SplitN(parts[1], "/", 2)
			mem = strings.TrimSpace(memParts[0])
			if len(memParts) == 2 {
				memLimit = strings.TrimSpace(memParts[1])
			}
		}
	}
	OK(w, map[string]any{
		"container": container, "image": raw.Config.Image, "status": raw.State.Status,
		"created": raw.Created[:19], "started_at": raw.State.StartedAt[:19],
		"restarts": raw.State.RestartCount,
		"ports":    ports, "mounts": mounts,
		"cpu": cpu, "mem": mem, "mem_limit": memLimit,
	})
}

// handleAppContainerLog 任意商店应用的容器日志(最近 tail 行)
func (s *Server) handleAppContainerLog(w http.ResponseWriter, r *http.Request) {
	key, ok := safeID(r.PathValue("key"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法应用标识")
		return
	}
	container := "kun-" + key
	n := atoiDefault(r.URL.Query().Get("tail"), 150)
	if n < 10 {
		n = 10
	}
	if n > 1000 {
		n = 1000
	}
	out, err := util.RunWithTimeout(20*time.Second, "docker", "logs", "--tail", strconv.Itoa(n), container)
	if err != nil && strings.TrimSpace(out) == "" {
		Fail(w, http.StatusInternalServerError, "读取日志失败: "+err.Error())
		return
	}
	OK(w, map[string]string{"content": out})
}
