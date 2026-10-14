package server

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"time"

	xdraw "golang.org/x/image/draw"
	"kunpanel/internal/config"
)

const avatarSize = 256

// handleAvatarGet 输出用户头像(<img> 通过 ?token= 鉴权)
func (s *Server) handleAvatarGet(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(config.AvatarPath())
	if err != nil {
		// 无自定义头像时,返回默认的坤坤鸡 logo
		logo, logoErr := os.ReadFile(filepath.Join(config.DataDir(), "..", "web", "static", "logo.png"))
		if logoErr != nil {
			// 尝试从嵌入式资源读取
			logoErr = os.ErrNotExist
		}
		if logoErr != nil {
			// 从执行文件旁的 static 目录读取
			logo, logoErr = os.ReadFile("/opt/kunpanel/web/static/logo.png")
		}
		if logoErr != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(logo)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-cache")
	if fi, err := os.Stat(config.AvatarPath()); err == nil {
		http.ServeContent(w, r, "avatar.png", fi.ModTime(), bytes.NewReader(b))
		return
	}
	_, _ = w.Write(b)
}

// handleAvatarUpload 上传头像:自动居中裁方、缩放到 256x256 PNG
func (s *Server) handleAvatarUpload(w http.ResponseWriter, r *http.Request) {
	// 请求体硬上限 3MB+开销:防止超大文件占满带宽/磁盘(第二轮渗透修复)
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20+4096)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		Fail(w, http.StatusBadRequest, "上传失败: 文件过大(最大 3MB)或格式错误")
		return
	}
	f, _, err := r.FormFile("avatar")
	if err != nil {
		Fail(w, http.StatusBadRequest, "未选择图片")
		return
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		Fail(w, http.StatusBadRequest, "仅支持 PNG / JPG / GIF 图片")
		return
	}
	if err := saveSquarePNG(src, config.AvatarPath(), avatarSize); err != nil {
		Fail(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	OK(w, map[string]any{"ts": time.Now().Unix()})
}

// handleAvatarRemove 恢复默认头像
func (s *Server) handleAvatarRemove(w http.ResponseWriter, r *http.Request) {
	if err := os.Remove(config.AvatarPath()); err != nil && !os.IsNotExist(err) {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

// saveSquarePNG 居中裁方 + CatmullRom 高质量缩放到 size,编码 PNG 落盘。
// 此前用最近邻逐行跳采样,缩放细密纹理会产生摩尔纹条纹(头像显示错误事故)。
func saveSquarePNG(src image.Image, dst string, size int) error {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	side := w
	if h < side {
		side = h
	}
	x0 := b.Min.X + (w-side)/2
	y0 := b.Min.Y + (h-side)/2

	crop := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			crop.Set(x, y, src.At(x0+x, y0+y))
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(out, out.Bounds(), crop, crop.Bounds(), xdraw.Over, nil)

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}

// avatarState 供设置接口返回头像状态
func avatarState() map[string]any {
	fi, err := os.Stat(config.AvatarPath())
	if err != nil {
		return map[string]any{"has_avatar": false}
	}
	return map[string]any{"has_avatar": true, "avatar_ts": fi.ModTime().Unix()}
}
