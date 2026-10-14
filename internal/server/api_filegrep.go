package server

import (
	"net/http"
	"os/exec"
	"runtime"
	"strings"
)

// handleFileGrep 在目录下递归搜索文件内容(基于 grep -rn)
func (s *Server) handleFileGrep(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	q := r.URL.Query().Get("q")
	if dir == "" || q == "" {
		Fail(w, http.StatusBadRequest, "缺少 path 或 q 参数")
		return
	}
	if len(q) > 200 {
		Fail(w, http.StatusBadRequest, "搜索关键词过长")
		return
	}
	// 防 shell 注入:关键词中不允许的特殊字符
	for _, ch := range []string{";", "|", "&", "`", "$(", "${", "'", "\n"} {
		if strings.Contains(q, ch) {
			Fail(w, http.StatusBadRequest, "搜索关键词包含非法字符")
			return
		}
	}
	ext := r.URL.Query().Get("ext") // 可选:限定文件扩展名如 "php,js,html"
	if ext != "" && len(ext) > 100 {
		ext = ""
	}

	var out []byte
	var err error
	if runtime.GOOS == "windows" {
		Fail(w, http.StatusBadRequest, "内容搜索暂不支持 Windows")
		return
	}
	// grep -rn --include=*.ext -l 或带行号
	args := []string{"grep", "-rn", "--include=*", "-I", "-m", "3", "-h", q, dir}
	if ext != "" {
		args = []string{"grep", "-rn", "-I", "-m", "3", q, dir}
		for _, e := range strings.Split(ext, ",") {
			e = strings.TrimSpace(e)
			if e != "" && !strings.ContainsAny(e, ";|&$`") {
				args = append(args, "--include=*."+e)
			}
		}
	}
	cmd := exec.Command(args[0], args[1:]...)
	out, err = cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		OK(w, []map[string]string{})
		return
	}
	// 解析 grep 输出为结构化结果:file:line:text
	var results []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		results = append(results, map[string]string{
			"file":  parts[0],
			"line":  strings.TrimSpace(parts[1]),
			"text":  strings.TrimSpace(parts[2]),
		})
		if len(results) >= 200 {
			break
		}
	}
	if results == nil {
		results = []map[string]string{}
	}
	OK(w, results)
}
