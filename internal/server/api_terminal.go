package server

import (
	"net/http"

	"kunpanel/internal/authx"
	"kunpanel/internal/config"
	"kunpanel/internal/terminalx"
)

// handleTerminal WebSocket 终端:一次性票据或访问令牌鉴权
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if tk := r.URL.Query().Get("ticket"); tk != "" && terminalx.ConsumeTicket(tk) {
		terminalx.Handle(w, r)
		return
	}
	cfg := config.M.Get()
	if authx.VerifyToken(cfg.JWTSecret, BearerToken(r), cfg.Username) {
		terminalx.Handle(w, r)
		return
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// handleTerminalTicket 签发一次性终端连接票据(需已登录)
func (s *Server) handleTerminalTicket(w http.ResponseWriter, r *http.Request) {
	ticket := terminalx.IssueTicket()
	if ticket == "" {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	OK(w, map[string]string{"ticket": ticket})
}
