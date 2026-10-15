package server

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"kunpanel/internal/filemgr"
	"kunpanel/internal/taskx"
	"kunpanel/internal/util"
)

// homeDirForUI 文件管理默认根目录
func homeDirForUI() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	return "/"
}

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = homeDirForUI()
	}
	entries, _, err := filemgr.List(path)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if entries == nil {
		entries = []filemgr.Entry{}
	}
	OK(w, map[string]any{
		"path":    filepath.Clean(path),
		"parent":  filepath.Dir(filepath.Clean(path)),
		"entries": entries,
	})
}

func (s *Server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		Fail(w, http.StatusBadRequest, "缺少 path 参数")
		return
	}
	content, err := filemgr.Read(path)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, map[string]any{"path": path, "content": content})
}

type fileOpReq struct {
	Path    string   `json:"path"`
	NewPath string   `json:"new_path"`
	OldPath string   `json:"old_path"`
	Src     string   `json:"src"`
	Dst     string   `json:"dst"`
	Sources []string `json:"sources"`
	Content string   `json:"content"`
	Mode    uint32   `json:"mode"`
	Dest    string   `json:"dest"`
}

func (s *Server) handleFileSave(w http.ResponseWriter, r *http.Request) {
	var req fileOpReq
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	// 保存前留存历史版本(宝塔式,可在编辑器中回滚)
	filemgr.Snapshot(req.Path)
	if err := filemgr.Write(req.Path, req.Content); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

// handleFileHistory 列出文件编辑历史版本
func (s *Server) handleFileHistory(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		Fail(w, http.StatusBadRequest, "缺少 path")
		return
	}
	OK(w, filemgr.History(path))
}

// handleFileHistoryContent 读取历史版本内容
func (s *Server) handleFileHistoryContent(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	ts := r.URL.Query().Get("ts")
	if path == "" || ts == "" {
		Fail(w, http.StatusBadRequest, "缺少参数")
		return
	}
	data, err := filemgr.HistoryContent(path, ts)
	if err != nil {
		Fail(w, http.StatusNotFound, "历史版本不存在")
		return
	}
	OK(w, map[string]string{"content": string(data)})
}

// handleFileHistoryRestore 恢复历史版本
func (s *Server) handleFileHistoryRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		TS   string `json:"ts"`
	}
	if err := readJSON(r, &req); err != nil || req.Path == "" || req.TS == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := filemgr.RestoreHistory(req.Path, req.TS); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleFileMkdir(w http.ResponseWriter, r *http.Request) {
	var req fileOpReq
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := filemgr.Mkdir(req.Path); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleFileRename(w http.ResponseWriter, r *http.Request) {
	var req fileOpReq
	if err := readJSON(r, &req); err != nil || req.OldPath == "" || req.NewPath == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := filemgr.Rename(req.OldPath, req.NewPath); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path      string `json:"path"`
		Permanent bool   `json:"permanent"` // true 跳过回收站直接删除
	}
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if req.Path == "/" || strings.EqualFold(req.Path, `C:\`) {
		Fail(w, http.StatusBadRequest, "拒绝操作:不允许删除根目录")
		return
	}
	if req.Permanent {
		if err := filemgr.Delete(req.Path); err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		OK(w, map[string]string{"mode": "permanent"})
		return
	}
	item, err := filemgr.ToTrash(req.Path)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"mode": "trash", "id": item.ID})
}

// ---------- 回收站 ----------

func (s *Server) handleTrashList(w http.ResponseWriter, r *http.Request) {
	items, err := filemgr.TrashList()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, items)
}

func (s *Server) handleTrashRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil || req.ID == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := filemgr.TrashRestore(req.ID); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleTrashDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil || req.ID == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := filemgr.TrashDeleteItem(req.ID); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleTrashClear(w http.ResponseWriter, r *http.Request) {
	if err := filemgr.TrashClear(); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleFileCopy(w http.ResponseWriter, r *http.Request) {
	var req fileOpReq
	if err := readJSON(r, &req); err != nil || req.Src == "" || req.Dst == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := filemgr.Copy(req.Src, req.Dst); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleFileMove(w http.ResponseWriter, r *http.Request) {
	var req fileOpReq
	if err := readJSON(r, &req); err != nil || req.Src == "" || req.Dst == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := filemgr.Rename(req.Src, req.Dst); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleFileChmod(w http.ResponseWriter, r *http.Request) {
	var req fileOpReq
	if err := readJSON(r, &req); err != nil || req.Path == "" || req.Mode == 0 {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := filemgr.Chmod(req.Path, os.FileMode(req.Mode)); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleFileCompress(w http.ResponseWriter, r *http.Request) {
	var req fileOpReq
	if err := readJSON(r, &req); err != nil || req.Dst == "" || len(req.Sources) == 0 {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if err := filemgr.Compress(req.Dst, req.Sources); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleFileExtract(w http.ResponseWriter, r *http.Request) {
	var req fileOpReq
	if err := readJSON(r, &req); err != nil || req.Src == "" {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	dest := req.Dest
	if dest == "" {
		ext := filepath.Ext(req.Src)
		name := strings.TrimSuffix(filepath.Base(req.Src), ext)
		if strings.HasSuffix(strings.ToLower(name), ".tar") {
			name = strings.TrimSuffix(name, ".tar")
		}
		dest = filepath.Join(filepath.Dir(req.Src), name+"_extracted")
	}
	if err := filemgr.Extract(req.Src, dest); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"dest": dest})
}

// handleFileDownload 下载文件
func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" || util.IsDir(path) {
		Fail(w, http.StatusBadRequest, "文件不存在或为目录")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	name := filepath.Base(path)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	_, _ = io.Copy(w, f)
}

// handleFileUpload 上传文件(multipart,单请求上限 1GB)
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		Fail(w, http.StatusBadRequest, "上传解析失败(超过 1GB 上限): "+err.Error())
		return
	}
	dir := r.FormValue("path")
	if dir == "" {
		Fail(w, http.StatusBadRequest, "缺少目标目录")
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		Fail(w, http.StatusBadRequest, "未选择文件")
		return
	}
	for _, fh := range files {
		if err := saveUpload(fh, dir); err != nil {
			Fail(w, http.StatusInternalServerError, fmt.Sprintf("保存 %s 失败: %v", fh.Filename, err))
			return
		}
	}
	OK(w, map[string]int{"count": len(files)})
}

func saveUpload(fh *multipart.FileHeader, dir string) error {
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst := filepath.Join(dir, filepath.Base(fh.Filename))
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return err
}

// handleFileRemoteDownload 从 URL 远程下载文件到指定目录(后台任务,支持进度)
func (s *Server) handleFileRemoteDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL  string `json:"url"`
		Dir  string `json:"dir"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	u := strings.TrimSpace(req.URL)
	dir := filepath.Clean(strings.TrimSpace(req.Dir))
	name := filepath.Base(strings.TrimSpace(req.Name))
	// 校验:仅 http(s);目录必须已存在;文件名不得带路径
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		Fail(w, http.StatusBadRequest, "URL 必须以 http:// 或 https:// 开头")
		return
	}
	// SSRF 防护:拒绝解析到环回/私网/链路本地的目标
	if host := func() string {
		if uu, err := url.Parse(u); err == nil {
			return uu.Hostname()
		}
		return ""
	}(); host != "" {
		if ips, err := net.LookupIP(host); err == nil {
			for _, ip := range ips {
				if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
					Fail(w, http.StatusBadRequest, "拒绝下载: 目标指向内网地址("+ip.String()+")")
					return
				}
			}
		}
	}
	if len(u) > 1024 {
		Fail(w, http.StatusBadRequest, "URL 过长")
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		Fail(w, http.StatusBadRequest, "目标目录不存在")
		return
	}
	if name == "" || name == "." || name == "/" || strings.Contains(name, "/") {
		// 未填名称时从 URL 推断
		name = path.Base(strings.Split(u, "?")[0])
		if name == "" || name == "/" || name == "." {
			name = fmt.Sprintf("download_%d.bin", time.Now().Unix())
		}
	}
	dst := filepath.Join(dir, name)
	if !strings.HasPrefix(dst, dir+string(os.PathSeparator)) {
		Fail(w, http.StatusBadRequest, "非法目标路径")
		return
	}
	if _, err := os.Stat(dst); err == nil {
		Fail(w, http.StatusBadRequest, "目标文件已存在: "+name)
		return
	}
	t, err := taskx.M.Start("remote-download-"+name, func(w io.Writer) error {
		fmt.Fprintln(w, "下载: "+u)
		fmt.Fprintln(w, "保存到: "+dst)
		fmt.Fprintln(w, "(大文件耗时较长,可切后台运行)")
		// Go 下载器:每一跳重定向都重新校验私网地址(curl -L 无法校验重定向目标)
		if err := util.SecureDownload(u, dst, w, 30*time.Minute, 2<<30); err != nil {
			_ = os.Remove(dst)
			return fmt.Errorf("下载失败: %v", err)
		}
		if fi, e := os.Stat(dst); e == nil {
			fmt.Fprintf(w, "✅ 下载完成: %s (%d 字节)\n", dst, fi.Size())
		}
		return nil
	})
	if err != nil {
		Fail(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, t)
}

// handleFileImagePreview 读取图片文件返回 base64 data URL(供 <img> 预览)
func (s *Server) handleFileImagePreview(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" || util.IsDir(path) {
		Fail(w, http.StatusBadRequest, "文件不存在或为目录")
		return
	}
	ext := strings.ToLower(filepath.Ext(path))
	var mime string
	switch ext {
	case ".png":
		mime = "image/png"
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".gif":
		mime = "image/gif"
	case ".webp":
		mime = "image/webp"
	case ".svg":
		mime = "image/svg+xml"
	case ".bmp":
		mime = "image/bmp"
	case ".ico":
		mime = "image/x-icon"
	default:
		Fail(w, http.StatusBadRequest, "不支持的图片格式: "+ext)
		return
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		Fail(w, http.StatusBadRequest, "文件不存在")
		return
	}
	if fi.Size() > 20<<20 {
		Fail(w, http.StatusBadRequest, "图片超过 20MB,请下载后查看")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{
		"data": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
	})
}
