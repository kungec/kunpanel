package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")

	// 首次写入
	if err := AtomicWrite(p, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != `{"a":1}` {
		t.Fatalf("读取不一致: %s %v", b, err)
	}
	// 权限
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("权限错误: %v", fi.Mode().Perm())
	}
	// 覆盖写
	if err := AtomicWrite(p, []byte(`{"a":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(p); string(b) != `{"a":2}` {
		t.Fatalf("覆盖写不一致: %s", b)
	}
	// 不残留临时文件
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Base(p) != e.Name() {
			t.Fatalf("残留临时文件: %s", e.Name())
		}
	}
	// 目录不存在时报错而非 panic
	if err := AtomicWrite(filepath.Join(dir, "no", "such", "f"), []byte("x"), 0o600); err == nil {
		t.Fatal("不存在的目录应报错")
	}
}
