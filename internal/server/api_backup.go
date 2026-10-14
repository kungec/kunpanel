package server

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/panelbackup"
	"kunpanel/internal/util"
)

// buildPanelArchive 打包面板关键配置到临时文件(复用 panelbackup 包)
func buildPanelArchive() (string, error) {
	return panelbackup.Build()
}

// handlePanelBackup 打包面板配置(不含应用数据)供下载
func (s *Server) handlePanelBackup(w http.ResponseWriter, r *http.Request) {
	tmp, err := buildPanelArchive()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.Remove(tmp)

	// 重新打开供下载
	f2, err := os.Open(tmp)
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f2.Close()
	fi, _ := f2.Stat()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''kunpanel_backup_"+time.Now().Format("20060102")+".tar.gz")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	io.Copy(w, f2)
}

// handlePanelRestore 从上传的备份包恢复面板配置
func (s *Server) handlePanelRestore(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		Fail(w, http.StatusBadRequest, "上传解析失败")
		return
	}
	f, _, err := r.FormFile("backup")
	if err != nil {
		Fail(w, http.StatusBadRequest, "未选择文件")
		return
	}
	defer f.Close()

	dir := config.DataDir()
	gr, err := gzip.NewReader(f)
	if err != nil {
		Fail(w, http.StatusBadRequest, "无效的备份文件(非 gzip)")
		return
	}
	tr := tar.NewReader(gr)
	restored := 0
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		name := filepath.Clean(hdr.Name)
		// 白名单:只恢复已知配置文件
		if !isValidRestoreFile(name) {
			continue
		}
		dst := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			continue
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			continue
		}
		restored++
	}
	if restored == 0 {
		Fail(w, http.StatusBadRequest, "未恢复任何文件,请检查备份包格式")
		return
	}
	OK(w, map[string]any{"restored": restored, "message": fmt.Sprintf("已恢复 %d 个配置文件,重启面板后生效", restored)})
}

func isValidRestoreFile(name string) bool {
	switch name {
	case "config.json", "alert.json":
		return true
	}
	return strings.HasPrefix(name, "ssl/") ||
		strings.HasPrefix(name, "websites/conf.d/") ||
		strings.HasPrefix(name, "apps/") && strings.HasSuffix(name, "docker-compose.yml")
}

// handleSystemCleanup 一键系统清理
// dirSize 统计目录大小(忽略不可读项)
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if info, e := d.Info(); e == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (s *Server) handleSystemCleanup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Apt     bool `json:"apt"`
		Journal bool `json:"journal"`
		Docker  bool `json:"docker"`
		Tmp     bool `json:"tmp"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	var results []string

	if req.Apt {
		before := dirSize("/var/cache/apt")
		out, err := util.RunWithTimeout(3*time.Minute, "sh", "-c", "apt-get clean 2>/dev/null; apt-get autoremove -y 2>/dev/null; echo ok")
		freed := before - dirSize("/var/cache/apt")
		if err != nil && !strings.Contains(out, "ok") {
			results = append(results, "APT 缓存清理失败")
		} else if freed > 0 {
			results = append(results, "APT 缓存清理完成,释放 "+util.HumanBytes(uint64(freed)))
		} else {
			results = append(results, "APT 缓存清理完成,缓存本来就是干净的")
		}
	}
	if req.Journal {
		// LANG=C 保证输出可解析(Vacuuming done, freed 128M ...)
		out, err := util.RunWithTimeout(time.Minute, "sh", "-c", "LANG=C journalctl --vacuum-size=50M 2>&1")
		freed := "0B"
		if m := regexp.MustCompile(`(?i)freed\s+([0-9.]+\s*[KMGT]?B)`).FindStringSubmatch(out); m != nil {
			freed = m[1]
		}
		if err != nil {
			results = append(results, "Journal 日志清理失败")
		} else {
			results = append(results, "Journal 日志清理完成,释放 "+freed)
		}
	}
	if req.Docker {
		out, err := util.RunWithTimeout(time.Minute, "docker", "image", "prune", "-f")
		reclaimed := "0B"
		if m := regexp.MustCompile(`Total reclaimed space:\s*(\S+)`).FindStringSubmatch(out); m != nil {
			reclaimed = m[1]
		}
		if err != nil {
			results = append(results, "Docker 清理失败")
		} else {
			results = append(results, "Docker 悬空镜像清理完成,释放 "+reclaimed)
		}
	}
	if req.Tmp {
		var count int64
		var freed int64
		cutoff := time.Now().Add(-7 * 24 * time.Hour)
		_ = filepath.WalkDir("/tmp", func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			info, e := d.Info()
			if e != nil || info.ModTime().After(cutoff) {
				return nil
			}
			size := info.Size()
			if os.Remove(path) == nil {
				count++
				freed += size
			}
			return nil
		})
		if count > 0 {
			results = append(results, fmt.Sprintf("临时文件清理完成,删除 %d 个文件,释放 %s", count, util.HumanBytes(uint64(freed))))
		} else {
			results = append(results, "临时文件清理完成,7 天以上的临时文件为 0")
		}
	}
	if len(results) == 0 {
		results = append(results, "未选择任何清理项")
	}
	OK(w, map[string]any{"results": results})
}
