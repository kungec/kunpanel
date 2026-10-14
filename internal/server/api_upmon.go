package server

import (
	"net/http"

	"kunpanel/internal/upmon"
)

func (s *Server) handleUpmonList(w http.ResponseWriter, r *http.Request) {
	OK(w, upmon.List())
}

func (s *Server) handleUpmonAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Target string `json:"target"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	m, err := upmon.Add(req.Name, req.Kind, req.Target)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, m)
}

func (s *Server) handleUpmonToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法 ID")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := upmon.Toggle(id, req.Enabled); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleUpmonDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法 ID")
		return
	}
	if err := upmon.Delete(id); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}
