package server

import (
	"net/http"
	"fmt"

	"kunpanel/internal/dbmgr"
)

type dbTableReq struct {
	Database string `json:"database"`
	Table    string `json:"table"`
}

// handleDbTables 列出某库的表
func (s *Server) handleDbTables(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	db := r.URL.Query().Get("db")
	var sql string
	if key == "postgresql" {
		sql = "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename"
	} else {
		sql = "SHOW TABLES"
		if db != "" {
			sql = "SHOW TABLES FROM `" + db + "`"
		}
	}
	result, err := dbmgr.TableQuery(key, db, sql)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var tables []string
	for _, row := range result.Rows {
		if len(row) > 0 && row[0] != nil {
			tables = append(tables, row[0].(string))
		}
	}
	OK(w, tables)
}

// handleDbTableData 浏览表数据(SELECT * LIMIT)
func (s *Server) handleDbTableData(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var req dbTableReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	// 白名单校验表名和库名
	if !dbmgr.ValidIdentifier(req.Table) || (req.Database != "" && !dbmgr.ValidIdentifier(req.Database)) {
		Fail(w, http.StatusBadRequest, "非法表名或库名")
		return
	}
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := 50
	offset := (page - 1) * pageSize
	var sql string
	if key == "postgresql" {
		sql = `SELECT * FROM ` + req.Table + ` LIMIT ` + fmt.Sprintf("%d", pageSize) + ` OFFSET ` + fmt.Sprintf("%d", offset)
	} else {
		sql = "SELECT * FROM `" + req.Table + "` LIMIT " + fmt.Sprintf("%d", pageSize) + " OFFSET " + fmt.Sprintf("%d", offset)
	}
	result, err := dbmgr.TableQuery(key, req.Database, sql)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, result)
}
