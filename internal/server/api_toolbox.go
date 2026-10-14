package server

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"kunpanel/internal/fwmgmt"
	"kunpanel/internal/nettools"
	"kunpanel/internal/procmgr"
	"kunpanel/internal/taskx"
)

// ---------- 网络工具箱 ----------

func (s *Server) handleNetPing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host  string `json:"host"`
		Count int    `json:"count"`
	}
	_ = readJSON(r, &req)
	res, err := nettools.Ping(req.Host, req.Count)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, res)
}

func (s *Server) handleNetTrace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
	}
	_ = readJSON(r, &req)
	res, err := nettools.TraceRoute(req.Host)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, res)
}

func (s *Server) handleNetDNS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
	}
	_ = readJSON(r, &req)
	res, err := nettools.DNSLookup(req.Host)
	if err != nil {
		// 部分失败仍返回已有记录
		if res != nil && len(res.Records) > 0 {
			OK(w, res)
			return
		}
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, res)
}

func (s *Server) handleNetPortScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
	}
	_ = readJSON(r, &req)
	res, err := nettools.PortScan(req.Host)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, res)
}

func (s *Server) handleNetHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	_ = readJSON(r, &req)
	res, err := nettools.HTTPCheck(req.URL)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, res)
}

// ---------- 进程管理器 ----------

func (s *Server) handleProcList(w http.ResponseWriter, r *http.Request) {
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "cpu"
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	list, err := procmgr.List(sortBy, limit)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, list)
}

func (s *Server) handleProcKill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PID   int32 `json:"pid"`
		Force bool  `json:"force"`
	}
	_ = readJSON(r, &req)
	if req.PID <= 0 {
		Fail(w, http.StatusBadRequest, "无效 PID")
		return
	}
	if err := procmgr.Kill(req.PID, req.Force); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, map[string]any{"killed": req.PID})
}

// ---------- 防火墙管理 ----------

func (s *Server) handleFwStatus(w http.ResponseWriter, r *http.Request) {
	OK(w, fwmgmt.Detect())
}

func (s *Server) handleFwAllow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port    string `json:"port"`
		Comment string `json:"comment"`
	}
	_ = readJSON(r, &req)
	if err := fwmgmt.Allow(req.Port, req.Comment); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, fwmgmt.Detect())
}

func (s *Server) handleFwDeny(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port string `json:"port"`
	}
	_ = readJSON(r, &req)
	if err := fwmgmt.Deny(req.Port); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, fwmgmt.Detect())
}

func (s *Server) handleFwToggle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}
	_ = readJSON(r, &req)
	if err := fwmgmt.SetActive(req.On); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// firewalld start 后需稍等状态就绪
	time.Sleep(500 * time.Millisecond)
	OK(w, fwmgmt.Detect())
}

// handleFwInstall 一键安装防火墙(异步任务)
func (s *Server) handleFwInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"` // ufw / firewalld
	}
	_ = readJSON(r, &req)
	if req.Kind != "ufw" && req.Kind != "firewalld" {
		Fail(w, http.StatusBadRequest, "kind 仅支持 ufw / firewalld")
		return
	}
	t, err := taskx.M.Start("fw-install-"+req.Kind, func(w io.Writer) error {
		return fwmgmt.Install(req.Kind, w)
	})
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}
