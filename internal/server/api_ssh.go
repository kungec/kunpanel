package server

import (
	"net/http"
	"strconv"

	"kunpanel/internal/secmgr"
	"kunpanel/internal/websites"
)

// ---------- SSH 安全 ----------

func (s *Server) handleSSHStatus(w http.ResponseWriter, r *http.Request) {
	st, err := secmgr.GetStatus()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, st)
}

func (s *Server) handleSSHPort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port int `json:"port"`
	}
	if err := readJSON(r, &req); err != nil || req.Port <= 0 {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := secmgr.SetPort(req.Port); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := secmgr.GetStatus()
	if err != nil {
		OK(w, nil)
		return
	}
	OK(w, st)
}

func (s *Server) handleSSHHardening(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PermitRoot   string `json:"permit_root"`
		PasswordAuth string `json:"password_auth"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := secmgr.SetHardening(req.PermitRoot, req.PasswordAuth); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := secmgr.GetStatus()
	if err != nil {
		OK(w, nil)
		return
	}
	OK(w, st)
}

func (s *Server) handleSSHKeyAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := readJSON(r, &req); err != nil || req.Key == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := secmgr.AddKey(req.Key); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	keys, _ := secmgr.ListKeys()
	OK(w, keys)
}

func (s *Server) handleSSHKeyRemove(w http.ResponseWriter, r *http.Request) {
	v := r.PathValue("index")
	idx, err := strconv.Atoi(v)
	if err != nil || idx <= 0 {
		Fail(w, http.StatusBadRequest, "非法序号")
		return
	}
	if err := secmgr.RemoveKey(idx); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	keys, _ := secmgr.ListKeys()
	OK(w, keys)
}

// ---------- 站点密码访问 ----------

func (s *Server) handleSiteAuth(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	var req struct {
		Enabled  bool   `json:"enabled"`
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := websites.SetSiteAuth(id, req.Enabled, req.User, req.Password); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}
