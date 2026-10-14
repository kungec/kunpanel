package server

import (
	"net/http"
	"strconv"

	"kunpanel/internal/notifyx"
)

// handleNotifyList 消息中心列表 + 未读数
func (s *Server) handleNotifyList(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	OK(w, map[string]any{
		"items":  notifyx.List(n),
		"unread": notifyx.Unread(after),
	})
}

// handleNotifyClear 清空消息
func (s *Server) handleNotifyClear(w http.ResponseWriter, r *http.Request) {
	notifyx.Clear()
	OK(w, nil)
}
