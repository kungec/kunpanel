package server

import (
	"net/http"

	"kunpanel/internal/cronx"
)

type cronReq struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Spec      string `json:"spec"`
	Command   string `json:"command"`
	BackupSrc string `json:"backup_src"`
	BackupDir string `json:"backup_dir"`
	DBKey     string `json:"db_key"`
	DBName    string `json:"db_name"`
	Remote    bool   `json:"remote"`
	Keep      int    `json:"keep"`
	Enabled   bool   `json:"enabled"`
}

func (s *Server) handleCronList(w http.ResponseWriter, r *http.Request) {
	OK(w, cronx.List())
}

func (s *Server) handleCronAdd(w http.ResponseWriter, r *http.Request) {
	var req cronReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	j, err := cronx.Add(cronx.Job{
		Name: req.Name, Type: req.Type, Spec: req.Spec,
		Command: req.Command, BackupSrc: req.BackupSrc, BackupDir: req.BackupDir,
		DBKey: req.DBKey, DBName: req.DBName, Remote: req.Remote, Keep: req.Keep,
	})
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, j)
}

func (s *Server) handleCronUpdate(w http.ResponseWriter, r *http.Request) {
	var req cronReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := cronx.Update(cronx.Job{
		ID: req.ID, Name: req.Name, Type: req.Type, Spec: req.Spec, Enabled: req.Enabled,
		Command: req.Command, BackupSrc: req.BackupSrc, BackupDir: req.BackupDir,
		DBKey: req.DBKey, DBName: req.DBName, Remote: req.Remote, Keep: req.Keep,
	}); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleCronDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法任务 ID")
		return
	}
	if err := cronx.Delete(id); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleCronRun(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法任务 ID")
		return
	}
	if err := cronx.RunNow(id); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleCronToggle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法任务 ID")
		return
	}
	if err := cronx.Toggle(id, req.Enabled); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleCronLog(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法任务 ID")
		return
	}
	OK(w, cronx.ReadLog(id))
}

// handleCronLogClear 清空任务执行日志
func (s *Server) handleCronLogClear(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法任务 ID")
		return
	}
	if err := cronx.ClearLog(id); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}
