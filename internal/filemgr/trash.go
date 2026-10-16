// filemgr/trash.go —— 文件回收站:删除先移入回收站,可恢复/彻底删除/清空
package filemgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kunpanel/internal/config"
)

// TrashItem 回收站条目
type TrashItem struct {
	ID        string    `json:"id"` // 回收站内的目录名(时间戳_原名)
	OrigPath  string    `json:"orig_path"`
	Name      string    `json:"name"`
	IsDir     bool      `json:"is_dir"`
	Size      int64     `json:"size"`
	DeletedAt time.Time `json:"deleted_at"`
}

func TrashDir() string { return filepath.Join(config.DataDir(), "trash") }

func trashMetaPath(id string) string { return filepath.Join(TrashDir(), id+".json") }

// ToTrash 将文件/目录移入回收站,返回条目信息
func ToTrash(path string) (*TrashItem, error) {
	path = filepath.Clean(path)
	if path == "/" || strings.EqualFold(path, `C:\`) {
		return nil, fmt.Errorf("拒绝操作:不允许删除根目录")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	_ = os.MkdirAll(TrashDir(), 0o755)
	id := fmt.Sprintf("%d_%s", time.Now().UnixNano(), sanitizeName(filepath.Base(path)))
	dst := filepath.Join(TrashDir(), id)
	if err := os.Rename(path, dst); err != nil {
		// 跨设备:复制后删除
		if err2 := Copy(path, dst); err2 != nil {
			return nil, fmt.Errorf("移入回收站失败: %v", err)
		}
		if err := os.RemoveAll(path); err != nil {
			_ = os.RemoveAll(dst)
			return nil, err
		}
	}
	size := int64(0)
	if !fi.IsDir() {
		size = fi.Size()
	}
	item := &TrashItem{
		ID: id, OrigPath: path, Name: filepath.Base(path),
		IsDir: fi.IsDir(), Size: size, DeletedAt: time.Now(),
	}
	b, _ := json.MarshalIndent(item, "", "  ")
	if err := os.WriteFile(trashMetaPath(id), b, 0o600); err != nil {
		return nil, err
	}
	return item, nil
}

// TrashList 列出回收站内容(按删除时间倒序)
func TrashList() ([]TrashItem, error) {
	ents, err := os.ReadDir(TrashDir())
	if err != nil {
		return []TrashItem{}, nil
	}
	out := []TrashItem{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(TrashDir(), e.Name()))
		if err != nil {
			continue
		}
		var it TrashItem
		if json.Unmarshal(b, &it) == nil && it.ID != "" {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeletedAt.After(out[j].DeletedAt) })
	return out, nil
}

// TrashRestore 从回收站恢复到原路径
func TrashRestore(id string) error {
	if !safeTrashID(id) {
		return fmt.Errorf("非法条目 ID")
	}
	var item TrashItem
	b, err := os.ReadFile(trashMetaPath(id))
	if err != nil {
		return fmt.Errorf("条目不存在或元数据损坏")
	}
	if err := json.Unmarshal(b, &item); err != nil || item.OrigPath == "" {
		return fmt.Errorf("元数据损坏,无法恢复")
	}
	src := filepath.Join(TrashDir(), id)
	if _, err := os.Lstat(src); err != nil {
		return fmt.Errorf("回收站数据已不存在")
	}
	if _, err := os.Lstat(item.OrigPath); err == nil {
		return fmt.Errorf("原位置已存在同名文件: %s", item.OrigPath)
	}
	if err := os.MkdirAll(filepath.Dir(item.OrigPath), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, item.OrigPath); err != nil {
		// 跨设备挂载点(如 /tmp 是 tmpfs 而 /opt 在磁盘):回退为复制后清理。
		// 入回收站(ToTrash)有同样的回退,恢复也必须有,否则跨盘删除的文件无法恢复
		if err2 := Copy(src, item.OrigPath); err2 != nil {
			return fmt.Errorf("恢复失败: %v", err)
		}
		_ = os.RemoveAll(src)
	}
	_ = os.Remove(trashMetaPath(id))
	return nil
}

// TrashDeleteItem 彻底删除单个条目
func TrashDeleteItem(id string) error {
	if !safeTrashID(id) {
		return fmt.Errorf("非法条目 ID")
	}
	if err := os.RemoveAll(filepath.Join(TrashDir(), id)); err != nil {
		return err
	}
	_ = os.Remove(trashMetaPath(id))
	return nil
}

// TrashClear 清空回收站
func TrashClear() error {
	items, _ := TrashList()
	var firstErr error
	for _, it := range items {
		if err := TrashDeleteItem(it.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// safeTrashID 防路径穿越:只允许字母数字下划线点与连字符
func safeTrashID(id string) bool {
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return false
	}
	return true
}

// sanitizeName 去掉文件名中的路径分隔符
func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, string(os.PathSeparator), "_")
	return strings.ReplaceAll(name, "/", "_")
}

// TrashCleanOld 清理删除时间早于 before 的回收站条目,返回清理数量
func TrashCleanOld(before time.Time) (int, error) {
	entries, err := os.ReadDir(TrashDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		b, err := os.ReadFile(trashMetaPath(id))
		if err != nil {
			continue
		}
		var it TrashItem
		if json.Unmarshal(b, &it) != nil {
			continue
		}
		if it.DeletedAt.Before(before) {
			if err := TrashDeleteItem(id); err == nil {
				n++
			}
		}
	}
	return n, nil
}
