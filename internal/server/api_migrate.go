package server

import (
	"io"
	"net/http"

	"kunpanel/internal/btmigrate"
	"kunpanel/internal/taskx"
)

// handleMigrateScan 宝塔环境预检扫描
func (s *Server) handleMigrateScan(w http.ResponseWriter, r *http.Request) {
	res, err := btmigrate.ScanCached()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, res)
}

// handleMigrateApply 执行迁移(后台任务)
func (s *Server) handleMigrateApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sites       []string `json:"sites"`
		ImportCerts bool     `json:"import_certs"`
		MigrateCron bool     `json:"migrate_cron"`
		Databases   []string `json:"databases"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if len(req.Sites) == 0 {
		Fail(w, http.StatusBadRequest, "请选择要迁移的站点")
		return
	}
	t, err := taskx.M.Start("bt-migrate", func(w io.Writer) error {
		return btmigrate.Apply(btmigrate.ApplyOptions{
			Sites:       req.Sites,
			ImportCerts: req.ImportCerts,
			MigrateCron: req.MigrateCron,
			Databases:   req.Databases,
		}, w, nil)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

// handleMigrateReversePlan 面板→宝塔 反向迁移预检
func (s *Server) handleMigrateReversePlan(w http.ResponseWriter, r *http.Request) {
	res, err := btmigrate.ReversePlan()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, res)
}

// handleMigrateReverseApply 面板→宝塔 反向迁移(后台任务)
func (s *Server) handleMigrateReverseApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sites []string `json:"sites"`
		DBs   []string `json:"dbs"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if len(req.Sites) == 0 && len(req.DBs) == 0 {
		Fail(w, http.StatusBadRequest, "请选择要导出的站点或数据库")
		return
	}
	t, err := taskx.M.Start("bt-reverse", func(w io.Writer) error {
		return btmigrate.ReverseApply(btmigrate.ReverseOptions{Sites: req.Sites, DBs: req.DBs}, w)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

// handleMigrateRemoteScan 远程宝塔扫描(输入 IP/密码,SSH 对接)
func (s *Server) handleMigrateRemoteScan(w http.ResponseWriter, r *http.Request) {
	var req btmigrate.RemoteCred
	if err := readJSON(r, &req); err != nil || req.Host == "" {
		Fail(w, http.StatusBadRequest, "请填写服务器 IP")
		return
	}
	res, err := btmigrate.RemoteScan(req)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, res)
}

// handleMigrateRemoteApply 远程宝塔迁移执行(后台任务)
func (s *Server) handleMigrateRemoteApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host        string   `json:"host"`
		Port        int      `json:"port"`
		User        string   `json:"user"`
		Pass        string   `json:"pass"`
		Fingerprint string   `json:"fingerprint"`
		Sites       []string `json:"sites"`
		Databases   []string `json:"databases"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if req.Host == "" || len(req.Sites) == 0 && len(req.Databases) == 0 {
		Fail(w, http.StatusBadRequest, "请先连接检测并勾选要迁移的内容")
		return
	}
	if req.User == "" {
		req.User = "root"
	}
	cred := btmigrate.RemoteCred{Host: req.Host, Port: req.Port, User: req.User, Pass: req.Pass, Fingerprint: req.Fingerprint}
	t, err := taskx.M.Start("bt-remote-migrate", func(w io.Writer) error {
		return btmigrate.RemoteApply(btmigrate.RemoteOptions{Cred: cred, Sites: req.Sites, Databases: req.Databases}, w)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

// handleMigrateSwitch 流量切换(停宝塔 Nginx,面板 Nginx 接管 80/443)
func (s *Server) handleMigrateSwitch(w http.ResponseWriter, r *http.Request) {
	t, err := taskx.M.Start("bt-switch", func(w io.Writer) error {
		return btmigrate.SwitchTraffic(w)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

// handleMigrateRollback 回滚到宝塔 Nginx
func (s *Server) handleMigrateRollback(w http.ResponseWriter, r *http.Request) {
	t, err := taskx.M.Start("bt-rollback", func(w io.Writer) error {
		return btmigrate.RollbackTraffic(w)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

// handleMigrateStatus 流量归属状态
func (s *Server) handleMigrateStatus(w http.ResponseWriter, r *http.Request) {
OK(w, btmigrate.TrafficStatusFunc())
}

