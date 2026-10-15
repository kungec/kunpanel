package server

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"kunpanel/internal/fwmgmt"
	"kunpanel/internal/nettools"
	"kunpanel/internal/procmgr"
	"kunpanel/internal/taskx"
	"kunpanel/internal/plugins"
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

// handleWafSiteOff 当前关闭 WAF 的站点列表
func (s *Server) handleWafSiteOff(w http.ResponseWriter, r *http.Request) {
	st := plugins.GetWafSettings()
	if st.SiteOff == nil {
		st.SiteOff = []string{}
	}
	OK(w, map[string]any{"site_off": st.SiteOff})
}

// handleWafSiteToggle 站点级 WAF 开关切换
func (s *Server) handleWafSiteToggle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host    string `json:"host"`
		Enabled bool   `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil || req.Host == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	st := plugins.GetWafSettings()
	off := map[string]bool{}
	for _, h := range st.SiteOff {
		off[h] = true
	}
	if req.Enabled {
		delete(off, req.Host)
	} else {
		off[req.Host] = true
	}
	st.SiteOff = nil
	for h := range off {
		st.SiteOff = append(st.SiteOff, h)
	}
	sort.Strings(st.SiteOff)
	if err := plugins.SaveWafSettings(st); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, map[string]any{"host": req.Host, "enabled": req.Enabled})
}

// handleWafAutoBanStatus 自动封禁状态(阈值/当前清单)
func (s *Server) handleWafAutoBanStatus(w http.ResponseWriter, r *http.Request) {
	st := plugins.GetWafSettings()
	OK(w, map[string]any{
		"enable":    st.AutoBanEnable,
		"threshold": st.AutoBanThreshold,
		"list":      st.AutoBlackList,
	})
}

// handleWafAutoBanRun 手动执行一次自动封禁扫描;body 可带 {threshold} 一并保存
func (s *Server) handleWafAutoBanRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Threshold int `json:"threshold"`
	}
	_ = readJSON(r, &req)
	st := plugins.GetWafSettings()
	if req.Threshold > 0 {
		st.AutoBanEnable = true
		st.AutoBanThreshold = req.Threshold
		if err := plugins.SaveWafSettings(st); err != nil {
			Fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if !st.AutoBanEnable {
		Fail(w, http.StatusBadRequest, "自动封禁未启用")
		return
	}
	added, list := plugins.AutoBlacklistRun(st.AutoBanThreshold)
	st2 := plugins.GetWafSettings()
	st2.AutoBlackList = list
	if err := plugins.SaveWafSettings(st2); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]any{"added": added, "list": list})
}

// handleWafAutoBanClear 清空自动封禁清单
func (s *Server) handleWafAutoBanClear(w http.ResponseWriter, r *http.Request) {
	st := plugins.GetWafSettings()
	st.AutoBlackList = []string{}
	if err := plugins.SaveWafSettings(st); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

// handleFwDockerPorts 容器端口管控:列出发布端口与封禁状态
func (s *Server) handleFwDockerPorts(w http.ResponseWriter, r *http.Request) {
	OK(w, fwmgmt.DockerPorts())
}

// handleFwDockerBlock 封禁/解封容器端口的外部访问(DOCKER-USER 链)
func (s *Server) handleFwDockerBlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port  string `json:"port"`
		Proto string `json:"proto"`
		Block bool   `json:"block"`
	}
	if err := readJSON(r, &req); err != nil || req.Port == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	var err error
	if req.Block {
		err = fwmgmt.DockerBlock(req.Port, req.Proto)
	} else {
		err = fwmgmt.DockerUnblock(req.Port, req.Proto)
	}
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, fwmgmt.DockerPorts())
}

// handleFwPingStatus / handleFwPing ping(ICMP)开关
func (s *Server) handleFwPingStatus(w http.ResponseWriter, r *http.Request) {
	OK(w, map[string]bool{"ping": fwmgmt.PingStatus()})
}

func (s *Server) handleFwPing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := fwmgmt.SetPing(req.On); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, map[string]bool{"ping": req.On})
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
