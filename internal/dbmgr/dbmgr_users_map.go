package dbmgr

// 数据库账号 ↔ 库 的授权映射(账号管理增强):一次查出库级授权(mysql.db)
// 与全局权限(mysql.user),供前端在库卡片上显示授权用户、账号列表上显示
// 权限范围。查询语句为代码常量,无任何外部输入参与拼接;PostgreSQL 后续支持

import (
	"fmt"
	"strings"
)

// UserDBGrant 账号在某库(或全局 *.*)上的权限
type UserDBGrant struct {
	User  string `json:"user"`
	Host  string `json:"host"`
	Scope string `json:"scope"` // 库名;* 表示全局(*.*)
	Privs string `json:"privs"` // 逗号分隔;空 = 仅 USAGE(未授权)
}

// mysqlDBPrivCols mysql.db / mysql.user 的权限列 → 展示名(与授权模板同一套词)
var mysqlDBPrivCols = [][2]string{
	{"Select_priv", "SELECT"}, {"Insert_priv", "INSERT"}, {"Update_priv", "UPDATE"},
	{"Delete_priv", "DELETE"}, {"Create_priv", "CREATE"}, {"Drop_priv", "DROP"},
	{"Index_priv", "INDEX"}, {"Alter_priv", "ALTER"},
	{"Create_tmp_table_priv", "CREATE TEMPORARY TABLES"}, {"Lock_tables_priv", "LOCK TABLES"},
	{"Create_view_priv", "CREATE VIEW"}, {"Show_view_priv", "SHOW VIEW"},
	{"Create_routine_priv", "CREATE ROUTINE"}, {"Alter_routine_priv", "ALTER ROUTINE"},
	{"Execute_priv", "EXECUTE"}, {"Event_priv", "EVENT"}, {"Trigger_priv", "TRIGGER"},
	{"References_priv", "REFERENCES"},
}

// 固定查询语句:列清单是代码常量,无任何外部输入参与拼接
const userDBQuerySQL = "SELECT User, Host, Db, Select_priv, Insert_priv, Update_priv, Delete_priv, " +
	"Create_priv, Drop_priv, Index_priv, Alter_priv, Create_tmp_table_priv, Lock_tables_priv, " +
	"Create_view_priv, Show_view_priv, Create_routine_priv, Alter_routine_priv, Execute_priv, " +
	"Event_priv, Trigger_priv, References_priv FROM mysql.db ORDER BY User, Db"

const userGlobalQuerySQL = "SELECT User, Host, Select_priv, Insert_priv, Update_priv, Delete_priv, " +
	"Create_priv, Drop_priv, Index_priv, Alter_priv, Create_tmp_table_priv, Lock_tables_priv, " +
	"Create_view_priv, Show_view_priv, Create_routine_priv, Alter_routine_priv, Execute_priv, " +
	"Event_priv, Trigger_priv, References_priv FROM mysql.user ORDER BY User"

// UserDBGrants 授权映射
func UserDBGrants(key string) ([]UserDBGrant, error) {
	if !dbUserSupported(key) {
		return nil, fmt.Errorf("该数据库类型暂不支持(PostgreSQL 后续支持)")
	}
	// 两条查询并行执行(mysql 客户端每次调用约 200ms,串行翻倍)
	type res struct {
		tr  *TableResult
		err error
	}
	chDB, chUser := make(chan res, 1), make(chan res, 1)
	go func() {
		tr, err := TableQuery(key, "mysql", userDBQuerySQL)
		chDB <- res{tr, err}
	}()
	go func() {
		tr, err := TableQuery(key, "mysql", userGlobalQuerySQL)
		chUser <- res{tr, err}
	}()
	rDB, rUser := <-chDB, <-chUser
	if rDB.err != nil {
		return nil, rDB.err
	}
	var out []UserDBGrant
	rowGrants := func(tr *TableResult, row []interface{}) []string {
		var ps []string
		for _, pc := range mysqlDBPrivCols {
			for i, c := range tr.Columns {
				if strings.TrimSpace(c) == pc[0] && i < len(row) && fmt.Sprint(row[i]) == "Y" {
					ps = append(ps, pc[1])
				}
			}
		}
		return ps
	}
	cell := func(tr *TableResult, row []interface{}, name string) string {
		for i, c := range tr.Columns {
			if strings.TrimSpace(c) == name && i < len(row) {
				return strings.TrimSpace(fmt.Sprint(row[i]))
			}
		}
		return ""
	}
	// 库级授权(mysql.db):scope = Db 列
	if rDB.tr != nil {
		for _, row := range rDB.tr.Rows {
			u, h, db := cell(rDB.tr, row, "User"), cell(rDB.tr, row, "Host"), cell(rDB.tr, row, "Db")
			if u == "" || systemDBUsers[u] {
				continue
			}
			out = append(out, UserDBGrant{User: u, Host: h, Scope: db, Privs: strings.Join(rowGrants(rDB.tr, row), ", ")})
		}
	}
	// 全局授权(mysql.user):scope = *;无任何权限(仅 USAGE)的跳过
	if rUser.err == nil && rUser.tr != nil {
		for _, row := range rUser.tr.Rows {
			u, h := cell(rUser.tr, row, "User"), cell(rUser.tr, row, "Host")
			if u == "" || systemDBUsers[u] {
				continue
			}
			ps := rowGrants(rUser.tr, row)
			if len(ps) == 0 {
				continue
			}
			out = append(out, UserDBGrant{User: u, Host: h, Scope: "*", Privs: strings.Join(ps, ", ")})
		}
	}
	return out, nil
}
