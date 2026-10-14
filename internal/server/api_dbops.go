package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kunpanel/internal/dbmgr"
	"kunpanel/internal/taskx"
)

type dbOpReq struct {
	Name string `json:"name"`
}

func (s *Server) handleDbCreate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var req dbOpReq
	if err := readJSON(r, &req); err != nil || req.Name == "" {
		Fail(w, http.StatusBadRequest, "请填写库名")
		return
	}
	if err := dbmgr.CreateDatabase(key, req.Name); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleDbDrop(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var req dbOpReq
	if err := readJSON(r, &req); err != nil || req.Name == "" {
		Fail(w, http.StatusBadRequest, "请填写库名")
		return
	}
	if err := dbmgr.DropDatabase(key, req.Name); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

// handleDbImport 上传 SQL 文件(.sql/.sql.gz)导入数据库(后台任务)
func (s *Server) handleDbImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	key := r.FormValue("key")
	database := r.FormValue("database")
	if key == "" || database == "" {
		Fail(w, http.StatusBadRequest, "缺少数据库类型或库名")
		return
	}
	f, fh, err := r.FormFile("file")
	if err != nil {
		Fail(w, http.StatusBadRequest, "未选择 SQL 文件")
		return
	}
	defer f.Close()
	name := fh.Filename
	if !strings.HasSuffix(strings.ToLower(name), ".sql") && !strings.HasSuffix(strings.ToLower(name), ".sql.gz") {
		Fail(w, http.StatusBadRequest, "仅支持 .sql 或 .sql.gz 文件")
		return
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("kunimport_%d_%s", time.Now().UnixNano(), filepath.Base(name)))
	out, err := os.Create(tmp)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		os.Remove(tmp)
		Fail(w, http.StatusInternalServerError, "保存上传文件失败: "+err.Error())
		return
	}
	out.Close()
	t, terr := taskx.M.Start("db-import-"+database, func(w io.Writer) error {
		defer os.Remove(tmp)
		_, err := dbmgr.Import(key, database, tmp)
		return err
	})
	if terr != nil {
		os.Remove(tmp)
		Fail(w, http.StatusConflict, terr.Error())
		return
	}
	OK(w, t)
}

// handleDbImportBackup 从面板已有备份文件一键恢复
func (s *Server) handleDbImportBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key      string `json:"key"`
		Database string `json:"database"`
		File     string `json:"file"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	path, err := dbmgr.BackupPath(req.Key, req.File)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	t, terr := taskx.M.Start("db-restore-"+req.Database, func(w io.Writer) error {
		_, err := dbmgr.Import(req.Key, req.Database, path)
		return err
	})
	if terr != nil {
		Fail(w, http.StatusConflict, terr.Error())
		return
	}
	OK(w, t)
}
