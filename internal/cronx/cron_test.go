package cronx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 同一分钟只评估一次:30s tick 与分钟边界不对齐时,相邻两个 tick 会算出
// 同一分钟(如 :37 tick 与下一分钟的 :07 tick),快任务曾因此每次到点跑两遍
func TestCheckDueNoDoubleRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KP_DATA_DIR", dir)

	marker := filepath.Join(dir, "marker")
	job, err := Add(Job{
		Name:    "双跑回归",
		Spec:    "* * * * *",
		Command: "touch " + marker,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = Delete(job.ID) }()

	// 两个 tick::37 与下一分钟的 :07,旧逻辑都会算出 20:00 并各触发一次
	t1 := time.Date(2026, 10, 16, 20, 0, 37, 0, time.Local)
	t2 := time.Date(2026, 10, 16, 20, 1, 7, 0, time.Local)
	checkDue(t1)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	checkDue(t2) // 旧逻辑此处会再次触发
	time.Sleep(500 * time.Millisecond)

	b, _ := os.ReadFile(filepath.Join(dir, "cron", job.ID+".log"))
	if n := strings.Count(string(b), "成功"); n != 1 {
		t.Fatalf("期望执行 1 次,日志中成功记录 %d 次:\n%s", n, b)
	}
}

// 备份目录位于备份源内部应被拒绝(打包边读边写)
func TestValidateBackupDirInsideSrc(t *testing.T) {
	j := Job{Type: "backup", BackupSrc: "/www/site", BackupDir: "/www/site/bak", Keep: 3}
	if err := validateJob(&j); err == nil {
		t.Fatal("备份目录在源内部应报错")
	}
	j.BackupDir = "/www/bak"
	if err := validateJob(&j); err != nil {
		t.Fatalf("正常备份目录不应报错: %v", err)
	}
}

// Next 语义回归:预解析重构后月/周受限等场景与 Match 一致
func TestNextSemantics(t *testing.T) {
	from := time.Date(2026, 10, 16, 20, 0, 0, 0, time.Local)
	cases := []struct {
		expr string
		want string
	}{
		{"30 1 * * *", "2026-10-17 01:30"},          // 每天次日
		{"0 3 15 * *", "2026-11-15 03:00"},           // 每月 15 日(10 月已过)
		{"0 4 * * 0", "2026-10-18 04:00"},            // 周日(10/16 周五)
		{"*/5 * * * *", "2026-10-16 20:05"},          // 每 5 分钟
		{"0 0 31 2 *", ""},                           // 2 月无 31 日:一年内无匹配
	}
	for _, c := range cases {
		got, err := Next(c.expr, from)
		if c.want == "" {
			if err == nil {
				t.Errorf("%s 应无匹配,得到了 %s", c.expr, got.Format("2006-01-02 15:04"))
			}
			continue
		}
		if err != nil || got.Format("2006-01-02 15:04") != c.want {
			t.Errorf("%s → %v (err=%v), want %s", c.expr, got.Format("2006-01-02 15:04"), err, c.want)
		}
	}
}
