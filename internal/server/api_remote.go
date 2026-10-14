package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"kunpanel/internal/remotex"
	"kunpanel/internal/taskx"
)

// handleRemoteGet 读取 SFTP 远程备份配置
func (s *Server) handleRemoteGet(w http.ResponseWriter, r *http.Request) {
	OK(w, remotex.Get())
}

// handleRemoteSave 保存配置
func (s *Server) handleRemoteSave(w http.ResponseWriter, r *http.Request) {
	var c remotex.Config
	if err := readJSON(r, &c); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if c.Host == "" && (c.User != "" || c.Password != "") {
		Fail(w, http.StatusBadRequest, "缺少主机地址")
		return
	}
	if err := remotex.Save(c); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

// handleRemoteTest 测试 SFTP 连接
func (s *Server) handleRemoteTest(w http.ResponseWriter, r *http.Request) {
	var c remotex.Config
	if err := readJSON(r, &c); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if c.Password == "" {
		c.Password = remotex.LoadPlain().Password
		c.HostKeyFingerprint = remotex.LoadPlain().HostKeyFingerprint
	}
	if err := remotex.Test(c); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, "连接成功,远程目录可用")
}

// handlePanelBackupRemote 生成面板备份并上传到 SFTP 远程(后台任务)
func (s *Server) handlePanelBackupRemote(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	_ = readJSON(r, &req)
	t, err := taskx.M.Start("panel-backup-remote", func(w io.Writer) error {
		tmp, err := buildPanelArchive()
		if err != nil {
			return err
		}
		defer os.Remove(tmp)
		fmt.Fprintln(w, "[1/2] 备份包已生成:", filepath.Base(tmp))
		fmt.Fprintln(w, "[2/2] 上传到远程 ...")
		remote, err := remotex.Upload(remotex.LoadPlain(), tmp)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, "✅ 已上传到远程:", remote)
		return nil
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, t)
}
