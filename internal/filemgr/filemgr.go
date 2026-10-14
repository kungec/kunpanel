package filemgr

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
	Symlink string    `json:"symlink,omitempty"`
}

// List 列出目录内容,目录优先、按名称排序
func List(dir string) ([]Entry, int64, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, 0, err
	}
	if !fi.IsDir() {
		return nil, 0, fmt.Errorf("%s 不是目录", dir)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	var out []Entry
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		entry := Entry{
			Name:    e.Name(),
			Path:    filepath.Join(dir, e.Name()),
			IsDir:   e.IsDir() || info.Mode()&os.ModeSymlink != 0 && info.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().Perm().String(),
			ModTime: info.ModTime(),
		}
		if lt, err := os.Readlink(entry.Path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			entry.Symlink = lt
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].IsDir != out[b].IsDir {
			return out[a].IsDir
		}
		return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name)
	})
	return out, fi.Size(), nil
}

// Read 读取文本文件(限制 2MB,避免撑爆浏览器)
func Read(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.Size() > 2<<20 {
		return "", fmt.Errorf("文件超过 2MB,请在终端中查看")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func Write(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func Mkdir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func Delete(path string) error {
	return os.RemoveAll(path)
}

func Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func Copy(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			lt, err := os.Readlink(p)
			if err == nil {
				_ = os.Symlink(lt, target)
			}
			return nil
		}
		return copyFile(p, target)
	})
}

func Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

// Compress 打包文件/目录为 zip 或 tar.gz(按 dst 后缀)
func Compress(dst string, sources []string) error {
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	if strings.HasSuffix(strings.ToLower(dst), ".zip") {
		return makeZip(dst, sources)
	}
	return makeTarGz(dst, sources)
}

func makeZip(dst string, sources []string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	for _, src := range sources {
		err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			rel, relErr := filepath.Rel(filepath.Dir(src), p)
			if relErr != nil {
				rel = filepath.Base(p)
			}
			hdr, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			hdr.Name = rel
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, in)
			in.Close()
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func makeTarGz(dst string, sources []string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for _, src := range sources {
		base := filepath.Dir(src)
		err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(base, p)
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = rel
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if info.IsDir() || !info.Mode().IsRegular() {
				return nil
			}
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			_, err = io.Copy(tw, in)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Extract 解压 zip / tar.gz / tar 到 dest
func Extract(src, dest string) error {
	lower := strings.ToLower(src)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return unzip(src, dest)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"), strings.HasSuffix(lower, ".tar"):
		return untar(src, dest)
	default:
		return fmt.Errorf("不支持的压缩格式,支持 zip / tar.gz / tar")
	}
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		target := safeJoin(dest, f.Name)
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func untar(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	var tr *tar.Reader
	if strings.HasSuffix(strings.ToLower(src), ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr = tar.NewReader(gz)
	} else {
		tr = tar.NewReader(f)
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := safeJoin(dest, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Symlink(hdr.Linkname, target)
		}
	}
	return nil
}

// safeJoin 防止压缩包路径穿越(zip slip)
func safeJoin(dest, name string) string {
	target := filepath.Join(dest, filepath.Clean("/"+name))
	if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) && filepath.Clean(dest) != target {
		return filepath.Join(dest, filepath.Base(name))
	}
	return target
}
