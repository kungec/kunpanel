package taskx

import (
	"io"
	"sync"
	"testing"
	"time"
)

// TestStartNoReentrantDeadlock 回归:Start 曾在持有 Manager.mu 时调用
// cleanupFinished(内部再次加锁),sync.Mutex 不可重入 → 首次 Start 即自死锁,
// 之后所有任务操作(卸载/安装/任务列表)永久挂起。
func TestStartNoReentrantDeadlock(t *testing.T) {
	m := NewManager()
	done := make(chan *Task, 1)
	go func() {
		tp, err := m.Start("regression-task", func(w io.Writer) error { return nil })
		if err != nil {
			t.Errorf("Start 返回错误: %v", err)
			done <- nil
			return
		}
		done <- tp
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start 5 秒未返回:存在锁内重入死锁")
	}
}

// TestConcurrentOperations 并发 Start/Get/List 无死锁、无竞态崩溃
func TestConcurrentOperations(t *testing.T) {
	m := NewManager()
	var wg sync.WaitGroup
	deadline := time.After(10 * time.Second)
	ok := make(chan bool, 1)
	go func() {
		for i := 0; i < 50; i++ {
			wg.Add(3)
			go func() { defer wg.Done(); _, _ = m.Start("t-"+time.Now().Format("150405.000000000"), func(w io.Writer) error { return nil }) }()
			go func() { defer wg.Done(); m.List() }()
			go func() { defer wg.Done(); m.Get("nonexistent") }()
		}
		wg.Wait()
		ok <- true
	}()
	select {
	case <-ok:
	case <-deadline:
		t.Fatal("并发操作 10 秒未完成:疑似死锁")
	}
}
