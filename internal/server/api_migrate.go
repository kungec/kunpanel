package server

import (
	"io"
	"net/http"

	"kunpanel/internal/btmigrate"
	"kunpanel/internal/p1migrate"
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
		Crons       []string `json:"crons"` // 勾选迁移的计划任务(echo);空=迁移全部可迁移任务
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
			Crons:       req.Crons,
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

// ---------- 1Panel 迁移 ----------

// handleP1Scan 1Panel 环境预检
func (s *Server) handleP1Scan(w http.ResponseWriter, r *http.Request) {
	res, err := p1migrate.P1Scan()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, res)
}

// handleP1Apply 1Panel → 坤面板 迁移(后台任务)
func (s *Server) handleP1Apply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sites       []string `json:"sites"`
		ImportCerts bool     `json:"import_certs"`
		Databases   []string `json:"databases"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	t, err := taskx.M.Start("p1-migrate", func(w io.Writer) error {
		return p1migrate.Apply(p1migrate.ApplyOptions{
			Sites: req.Sites, ImportCerts: req.ImportCerts, Databases: req.Databases,
		}, w, nil)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

// handleP1ReversePlan 坤面板 → 1Panel 预检
func (s *Server) handleP1ReversePlan(w http.ResponseWriter, r *http.Request) {
	res, err := p1migrate.P1ReverseScan()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, res)
}

// handleP1ReverseApply 坤面板 → 1Panel 导出(后台任务)
func (s *Server) handleP1ReverseApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sites []string `json:"sites"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if len(req.Sites) == 0 {
		Fail(w, http.StatusBadRequest, "请选择要导出的站点")
		return
	}
	t, err := taskx.M.Start("p1-reverse", func(w io.Writer) error {
		return p1migrate.P1ReverseApply(p1migrate.P1ReverseOptions{Sites: req.Sites}, w)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

// handleP1Switch 1Panel → 面板 流量切换(后台任务)
func (s *Server) handleP1Switch(w http.ResponseWriter, r *http.Request) {
	t, err := taskx.M.Start("p1-switch", func(w io.Writer) error {
		return p1migrate.P1SwitchTraffic(w)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

// handleP1Rollback 手动回滚到 1Panel(后台任务)
func (s *Server) handleP1Rollback(w http.ResponseWriter, r *http.Request) {
	t, err := taskx.M.Start("p1-rollback", func(w io.Writer) error {
		return p1migrate.P1RollbackTraffic(w)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

// handleP1Status 流量归属查询
func (s *Server) handleP1Status(w http.ResponseWriter, r *http.Request) {
	OK(w, p1migrate.P1TrafficStatus())
}

// handleP1RemoteScan 远程 1Panel 扫描(SSH 对接,TOFU 指纹回显)
func (s *Server) handleP1RemoteScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host        string `json:"host"`
		Port        int    `json:"port"`
		User        string `json:"user"`
		Pass        string `json:"pass"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	res, fp, err := p1migrate.P1RemoteScan(p1migrate.P1RemoteCred{
		Host: req.Host, Port: req.Port, User: req.User, Pass: req.Pass, Fingerprint: req.Fingerprint,
	})
	if err != nil {
		// 指纹回显也要带回(连接失败时前端展示失败原因即可)
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	res.Warnings = append(res.Warnings, "指纹: "+fp)
	OK(w, res)
}

// handleP1RemoteApply 远程 1Panel 迁移(后台任务)
func (s *Server) handleP1RemoteApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host        string   `json:"host"`
		Port        int      `json:"port"`
		User        string   `json:"user"`
		Pass        string   `json:"pass"`
		Fingerprint string   `json:"fingerprint"`
		Sites       []string `json:"sites"`
		ImportCerts bool     `json:"import_certs"`
		Databases   []string `json:"databases"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if req.Host == "" || req.Pass == "" {
		Fail(w, http.StatusBadRequest, "远程地址与密码不能为空")
		return
	}
	t, err := taskx.M.Start("p1-remote-migrate", func(w io.Writer) error {
		return p1migrate.P1RemoteApply(p1migrate.P1RemoteCred{
			Host: req.Host, Port: req.Port, User: req.User, Pass: req.Pass, Fingerprint: req.Fingerprint,
		}, p1migrate.ApplyOptions{
			Sites: req.Sites, ImportCerts: req.ImportCerts, Databases: req.Databases,
		}, w, nil)
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}
