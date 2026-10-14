package server

import (
	"net/http"

	"kunpanel/internal/system"
)

func (s *Server) handleServicesList(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"available": system.SystemdAvailable()}
	if !system.SystemdAvailable() {
		resp["services"] = []system.ServiceUnit{}
		OK(w, resp)
		return
	}
	list, err := system.ListServices()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range list {
		list[i].Enabled = system.IsServiceEnabled(list[i].Name)
	}
	if list == nil {
		list = []system.ServiceUnit{}
	}
	resp["services"] = list
	OK(w, resp)
}

func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	action := r.PathValue("action")
	out, err := system.ServiceAction(name, action)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, map[string]any{"output": out})
}
