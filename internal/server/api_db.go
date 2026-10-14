package server

import (
	"net/http"

	"kunpanel/internal/alertx"
	"kunpanel/internal/dbmgr"
)

func (s *Server) handleDbInstances(w http.ResponseWriter, r *http.Request) {
	OK(w, dbmgr.Instances())
}

type dbQueryReq struct {
	Key      string `json:"key"`
	Database string `json:"database"`
	SQL      string `json:"sql"`
}

func (s *Server) handleDbQuery(w http.ResponseWriter, r *http.Request) {
	var req dbQueryReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	out, err := dbmgr.Query(req.Key, req.Database, req.SQL)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, map[string]any{"output": out})
}

func (s *Server) handleDbDatabases(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	dbs, err := dbmgr.Databases(key)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, dbs)
}

type dbBackupReq struct {
	Key      string `json:"key"`
	Database string `json:"database"`
}

func (s *Server) handleDbBackup(w http.ResponseWriter, r *http.Request) {
	var req dbBackupReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	file, err := dbmgr.Backup(req.Key, req.Database)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]any{"file": file})
}

func (s *Server) handleDbBackupList(w http.ResponseWriter, r *http.Request) {
	OK(w, dbmgr.ListBackups(r.PathValue("key")))
}

func (s *Server) handleDbBackupDelete(w http.ResponseWriter, r *http.Request) {
	if err := dbmgr.DeleteBackup(r.PathValue("key"), r.PathValue("file")); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handleDbBackupDownload 下载备份(文件名防穿越,token 走查询参数)
func (s *Server) handleDbBackupDownload(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	file := r.URL.Query().Get("file")
	p, err := dbmgr.BackupPath(key, file)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, p)
}

// ---------- 告警 ----------

func (s *Server) handleAlertGet(w http.ResponseWriter, r *http.Request) {
	OK(w, alertx.Get())
}

func (s *Server) handleAlertSave(w http.ResponseWriter, r *http.Request) {
	var st alertx.Settings
	if err := readJSON(r, &st); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := alertx.Save(st); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// 邮件渠道一并校验保存
	if err := alertx.SaveSMTP(st.SMTPSettings); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleAlertTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Webhook string `json:"webhook"`
		Channel string `json:"channel"` // webhook / mail
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if req.Channel == "mail" {
		if err := alertx.TestMail(); err != nil {
			Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		OK(w, nil)
		return
	}
	if req.Webhook == "" {
		Fail(w, http.StatusBadRequest, "请填写 Webhook 地址")
		return
	}
	if err := alertx.Test(req.Webhook); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handleDbSelect 结构化 SELECT(返回列名+行数据)
func (s *Server) handleDbSelect(w http.ResponseWriter, r *http.Request) {
	var req dbQueryReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	result, err := dbmgr.TableQuery(req.Key, req.Database, req.SQL)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, result)
}

// handleDbRootPassword 修改 MySQL/MariaDB root 密码
func (s *Server) handleDbRootPassword(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := readJSON(r, &req); err != nil || req.NewPassword == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := dbmgr.ChangeRootPassword(key, req.NewPassword); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}
