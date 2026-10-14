package server

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/filemgr"
	"kunpanel/internal/taskx"
	"kunpanel/internal/websites"
)

// handleSiteBackup 网站一键备份:打包站点根目录为 tar.gz 下载
func (s *Server) handleSiteBackup(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	site, err := websites.GetSite(id)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	root := site.Root
	if _, err := os.Stat(root); err != nil {
		Fail(w, http.StatusBadRequest, "站点目录不存在: "+root)
		return
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("site_backup_%s_%d.tar.gz", site.Alias, time.Now().Unix()))
	if err := compressToGz(root, tmp); err != nil {
		Fail(w, http.StatusInternalServerError, "打包失败: "+err.Error())
		return
	}
	defer os.Remove(tmp)

	f, err := os.Open(tmp)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s_backup.tar.gz", site.Alias))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	io.Copy(w, f)
}

// handleFileSearch 递归搜索文件名
func (s *Server) handleFileSearch(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if dir == "" || q == "" {
		Fail(w, http.StatusBadRequest, "缺少 path 或 q 参数")
		return
	}
	var results []map[string]any
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(results) >= 200 {
			return filepath.SkipAll
		}
		if !d.IsDir() && strings.Contains(strings.ToLower(d.Name()), strings.ToLower(q)) {
			fi, _ := d.Info()
			results = append(results, map[string]any{
				"path": path, "name": d.Name(), "size": fi.Size(),
				"is_dir": false, "mod_time": fi.ModTime().Format("2006-01-02 15:04"),
			})
		}
		return nil
	})
	if results == nil {
		results = []map[string]any{}
	}
	OK(w, results)
}

func compressToGz(src, dst string) error {
	cmd := exec.Command("tar", "-czf", dst, "-C", filepath.Dir(src), filepath.Base(src))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------- 站点备份管理:服务端留存/列表/恢复 ----------

func siteBackupDir(id string) string {
	return filepath.Join(config.DataDir(), "backups", "sites", id)
}

// SiteBackupInfo 站点备份条目
type SiteBackupInfo struct {
	File string `json:"file"`
	Size int64  `json:"size"`
	Time string `json:"time"`
}

// handleSiteBackupList 站点备份列表
func (s *Server) handleSiteBackupList(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	dir := siteBackupDir(id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		OK(w, []SiteBackupInfo{})
		return
	}
	out := []SiteBackupInfo{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, SiteBackupInfo{File: e.Name(), Size: fi.Size(), Time: fi.ModTime().Format("2006-01-02 15:04")})
	}
	// 新的在前
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	OK(w, out)
}

// handleSiteBackupCreate 立即备份到服务端(保留最近 10 份)
func (s *Server) handleSiteBackupCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	site, err := websites.GetSite(id)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(site.Root); err != nil {
		Fail(w, http.StatusBadRequest, "站点目录不存在: "+site.Root)
		return
	}
	dir := siteBackupDir(id)
	_ = os.MkdirAll(dir, 0o755)
	name := fmt.Sprintf("%s_%s.tar.gz", site.Alias, time.Now().Format("20060102_150405"))
	dst := filepath.Join(dir, name)
	if err := compressToGz(site.Root, dst); err != nil {
		os.Remove(dst)
		Fail(w, http.StatusInternalServerError, "备份失败: "+err.Error())
		return
	}
	// 只保留同前缀最近 10 份
	if entries, e := os.ReadDir(dir); e == nil {
		var mine []string
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() && strings.HasPrefix(n, site.Alias+"_") && strings.HasSuffix(n, ".tar.gz") {
				mine = append(mine, n)
			}
		}
		sort.Strings(mine)
		for len(mine) > 10 {
			os.Remove(filepath.Join(dir, mine[0]))
			mine = mine[1:]
		}
	}
	OK(w, map[string]string{"file": name})
}

// handleSiteBackupDelete 删除指定备份
func (s *Server) handleSiteBackupDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	file, ok := safeID(r.PathValue("file"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法文件名")
		return
	}
	if !strings.HasSuffix(file, ".tar.gz") {
		Fail(w, http.StatusBadRequest, "非法备份文件")
		return
	}
	if err := os.Remove(filepath.Join(siteBackupDir(id), file)); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

// handleSiteBackupRestore 从服务端备份包恢复站点(覆盖式解压)
func (s *Server) handleSiteBackupRestore(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	var req struct {
		File string `json:"file"`
	}
	if err := readJSON(r, &req); err != nil || req.File == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if !strings.HasSuffix(req.File, ".tar.gz") || strings.Contains(req.File, "/") || strings.Contains(req.File, "..") {
		Fail(w, http.StatusBadRequest, "非法备份文件")
		return
	}
	site, err := websites.GetSite(id)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	src := filepath.Join(siteBackupDir(id), req.File)
	if _, err := os.Stat(src); err != nil {
		Fail(w, http.StatusBadRequest, "备份文件不存在")
		return
	}
	t, err := taskx.M.Start("site-restore-"+id, func(w io.Writer) error {
		// 原子化恢复:先解压到临时目录并归位,全部成功后才替换原目录。
		// 旧流程先删原目录再解压,归档损坏会留下空站点(第三轮审计 P1)。
		tmpExtract := site.Root + ".restore_tmp"
		_ = os.RemoveAll(tmpExtract)
		if err := os.MkdirAll(tmpExtract, 0o755); err != nil {
			return err
		}
		fmt.Fprintln(w, "解压归档到临时目录 ...")
		if err := filemgr.Extract(src, tmpExtract); err != nil {
			os.RemoveAll(tmpExtract)
			return fmt.Errorf("解压失败(原站点未受影响): %v", err)
		}
		// 归位:归档可能带站点目录前缀(compressToGz 以父目录打包)
		entries, _ := os.ReadDir(tmpExtract)
		if len(entries) == 1 && entries[0].IsDir() && entries[0].Name() == filepath.Base(site.Root) {
			inner := filepath.Join(tmpExtract, entries[0].Name())
			if subs, e := os.ReadDir(inner); e == nil {
				for _, s2 := range subs {
					os.Rename(filepath.Join(inner, s2.Name()), filepath.Join(tmpExtract, s2.Name()))
				}
			}
			_ = os.Remove(filepath.Join(tmpExtract, entries[0].Name()))
		}
		// 交换:原目录挪到 .restore_old,新内容就位,成功后清理备份
		oldDir := site.Root + ".restore_old"
		_ = os.RemoveAll(oldDir)
		fmt.Fprintln(w, "替换站点目录 ...")
		if err := os.Rename(site.Root, oldDir); err != nil {
			os.RemoveAll(tmpExtract)
			return fmt.Errorf("备份原目录失败: %v", err)
		}
		if err := os.Rename(tmpExtract, site.Root); err != nil {
			_ = os.Rename(oldDir, site.Root) // 回滚
			return fmt.Errorf("恢复失败(已回滚): %v", err)
		}
		_ = os.RemoveAll(oldDir)
		fmt.Fprintln(w, "✅ 站点已恢复,建议检查网站内容后重新开放")
		return nil
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"task_id": t.ID})
}

// handleSiteBackupUpload 上传备份包到服务端留存
func (s *Server) handleSiteBackupUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	site, err := websites.GetSite(id)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	f, fh, err := r.FormFile("file")
	if err != nil {
		Fail(w, http.StatusBadRequest, "未选择备份文件")
		return
	}
	defer f.Close()
	if !strings.HasSuffix(strings.ToLower(fh.Filename), ".tar.gz") && !strings.HasSuffix(strings.ToLower(fh.Filename), ".tgz") {
		Fail(w, http.StatusBadRequest, "仅支持 .tar.gz 备份包")
		return
	}
	dir := siteBackupDir(id)
	_ = os.MkdirAll(dir, 0o755)
	dst := filepath.Join(dir, fmt.Sprintf("%s_%s", site.Alias, time.Now().Format("20060102_150405"))+".tar.gz")
	out, err := os.Create(dst)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		os.Remove(dst)
		Fail(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	out.Close()
	OK(w, map[string]string{"file": filepath.Base(dst)})
}

// handleSiteBackupDownloadStored 下载服务端留存的备份
func (s *Server) handleSiteBackupDownloadStored(w http.ResponseWriter, r *http.Request) {
	id, ok := safeID(r.PathValue("id"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法站点 ID")
		return
	}
	file, ok := safeID(r.PathValue("file"))
	if !ok || !strings.HasSuffix(file, ".tar.gz") {
		Fail(w, http.StatusBadRequest, "非法备份文件")
		return
	}
	p := filepath.Join(siteBackupDir(id), file)
	f, err := os.Open(p)
	if err != nil {
		Fail(w, http.StatusNotFound, "备份不存在")
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(file)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	io.Copy(w, f)
}
