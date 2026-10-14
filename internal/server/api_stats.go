package server

import (
	"net/http"
	"strconv"

	"kunpanel/internal/monhist"
	"kunpanel/internal/webstat"
	"kunpanel/internal/websites"
)

// handleWebStats 网站流量统计(nginx access 日志解析)
func (s *Server) handleWebStats(w http.ResponseWriter, r *http.Request) {
	lines := atoiDefault(r.URL.Query().Get("lines"), 5000)
	if lines > 50000 {
		lines = 50000
	}
	mode := websites.DetectNginx()
	container, logPath := webstat.LogPaths(mode.Mode, mode.Target)

	content, err := webstat.GetLogContent(container, logPath, lines)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "读取日志失败: "+err.Error())
		return
	}
	stats := webstat.Analyze(content, lines)
	if stats.Since == "" {
		stats.Since = "日志范围内"
	}
	OK(w, stats)
}

// handleMonHistory 面板监控历史
func (s *Server) handleMonHistory(w http.ResponseWriter, r *http.Request) {
	n := atoiDefault(r.URL.Query().Get("n"), 1440)
	if n > 1440 {
		n = 1440
	}
	OK(w, monhist.History(n))
}

var _ = strconv.Itoa
