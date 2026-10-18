package server

// 数据库账号管理 + 主从同步的 API(仅 MySQL/MariaDB)。
// 主从:v1 主库=本机面板实例,从库=任意可达的 MySQL/MariaDB(需 root 密码)。

import (
	"io"
	"net/http"
	"strings"

	"kunpanel/internal/dbmgr"
	"kunpanel/internal/taskx"
)

func (s *Server) handleDbUsers(w http.ResponseWriter, r *http.Request) {
	users, err := dbmgr.ListUsers(r.PathValue("key"))
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if users == nil {
		users = []dbmgr.DBUser{}
	}
	OK(w, users)
}

// handleDbUserDBMap 账号↔库 授权映射(库卡片显示授权用户、账号列表显示权限范围)
func (s *Server) handleDbUserDBMap(w http.ResponseWriter, r *http.Request) {
	rows, err := dbmgr.UserDBGrants(r.PathValue("key"))
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if rows == nil {
		rows = []dbmgr.UserDBGrant{}
	}
	OK(w, map[string]any{"rows": rows})
}

func (s *Server) handleDbUserCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User     string `json:"user"`
		Host     string `json:"host"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil || req.User == "" || req.Password == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := dbmgr.CreateUser(r.PathValue("key"), req.User, req.Host, req.Password); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, "账号已创建")
}

func (s *Server) handleDbUserDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User string `json:"user"`
		Host string `json:"host"`
	}
	if err := readJSON(r, &req); err != nil || req.User == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := dbmgr.DropUser(r.PathValue("key"), req.User, req.Host); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, "账号已删除")
}

func (s *Server) handleDbUserPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User     string `json:"user"`
		Host     string `json:"host"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil || req.User == "" || req.Password == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := dbmgr.SetUserPassword(r.PathValue("key"), req.User, req.Host, req.Password); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, "密码已修改")
}

func (s *Server) handleDbUserGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User  string   `json:"user"`
		Host  string   `json:"host"`
		DB    string   `json:"db"`
		Privs []string `json:"privs"`
	}
	if err := readJSON(r, &req); err != nil || req.User == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := dbmgr.Grant(r.PathValue("key"), req.User, req.Host, req.DB, req.Privs); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, "授权成功")
}

func (s *Server) handleDbUserRevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User string `json:"user"`
		Host string `json:"host"`
		DB   string `json:"db"`
	}
	if err := readJSON(r, &req); err != nil || req.User == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := dbmgr.Revoke(r.PathValue("key"), req.User, req.Host, req.DB); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, "已收回该库权限")
}

func (s *Server) handleDbUserGrants(w http.ResponseWriter, r *http.Request) {
	user, host := r.URL.Query().Get("user"), r.URL.Query().Get("host")
	if user == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	grants, err := dbmgr.ShowGrants(r.PathValue("key"), user, host)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, map[string]string{"grants": grants})
}

// ---------- 主从同步 ----------

func (s *Server) handleReplStatus(w http.ResponseWriter, r *http.Request) {
	st, err := dbmgr.ReplStatus()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, st)
}

func (s *Server) handleReplSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key        string   `json:"key"`
		SlaveHost  string   `json:"slave_host"`
		SlavePort  string   `json:"slave_port"`
		MasterHost string   `json:"master_host"` // 从库回连主库的地址
		MasterPort string   `json:"master_port"` // 从库回连主库的端口(默认 3306)
		ReplUser   string   `json:"repl_user"`
		ReplPass   string   `json:"repl_pass"`
		SlavePass  string   `json:"slave_pass"`
		InitDump   bool     `json:"init_dump"`
		Dbs        []string `json:"dbs"`
	}
	if err := readJSON(r, &req); err != nil || req.Key == "" || req.SlaveHost == "" || req.SlavePass == "" || req.MasterHost == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if strings.TrimSpace(req.ReplPass) == "" {
		Fail(w, http.StatusBadRequest, "请填写复制账号密码")
		return
	}
	t, err := taskx.M.Start("db-replication", func(w io.Writer) error {
		return dbmgr.ReplSetup(dbmgr.ReplSetupOpts{
			Key: req.Key, SlaveHost: req.SlaveHost, SlavePort: req.SlavePort,
			MasterHost: req.MasterHost, MasterPort: req.MasterPort,
			ReplUser: req.ReplUser, ReplPass: req.ReplPass, SlavePass: req.SlavePass,
			InitDump: req.InitDump, DbList: req.Dbs,
		}, w)
	})
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

func (s *Server) handleReplStop(w http.ResponseWriter, r *http.Request) {
	if err := dbmgr.ReplStopSlave(); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, "复制已暂停")
}

func (s *Server) handleReplStart(w http.ResponseWriter, r *http.Request) {
	if err := dbmgr.ReplStartSlave(); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, "复制已恢复")
}

func (s *Server) handleReplReset(w http.ResponseWriter, r *http.Request) {
	if err := dbmgr.ReplReset(); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, "主从已解除")
}
