package server

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"kunpanel/internal/websites"
)

// handleNginxLogs 查看站点访问/错误日志(容器或宿主机)
func (s *Server) handleNginxLogs(w http.ResponseWriter, r *http.Request) {
	logType := r.URL.Query().Get("type")
	if logType != "access" && logType != "error" {
		Fail(w, http.StatusBadRequest, "type 仅支持 access / error")
		return
	}
	n := atoiDefault(r.URL.Query().Get("tail"), 200)
	if n > 2000 {
		n = 2000
	}
	mode := websites.DetectNginx()
	switch mode.Mode {
	case "container":
		out, err := runContainerTail(mode.Target, containerLogPath(mode.Target, logType), n)
		if err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		OK(w, map[string]any{"source": mode.Target, "content": strings.TrimRight(out, "\n")})
	case "host":
		b, err := os.ReadFile("/var/log/nginx/" + logType + ".log")
		if err != nil {
			Fail(w, http.StatusInternalServerError, "读取日志失败: "+err.Error())
			return
		}
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		OK(w, map[string]any{"source": "宿主机 nginx", "content": strings.Join(lines, "\n")})
	default:
		Fail(w, http.StatusBadRequest, "未检测到 Nginx / OpenResty")
	}
}

func containerLogPath(container, logType string) string {
	if container == "kun-openresty" {
		return "/usr/local/openresty/nginx/logs/" + logType + ".log"
	}
	return "/var/log/nginx/" + logType + ".log"
}

func runContainerTail(container, file string, n int) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("容器日志仅在 Linux 上可用")
	}
	out, err := exec.Command("docker", "exec", container, "tail", "-n", strconv.Itoa(n), file).CombinedOutput()
	if err != nil && strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("读取失败: %v", err)
	}
	return string(out), nil
}
