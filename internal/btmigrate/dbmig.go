// dbmig.go —— 数据库双向迁移。
//
// BT → 面板:从宝塔宿主机 MySQL 导出选中库,导入面板 MySQL 容器。
//   - 导出用宝塔自带 mysqldump;凭据优先用 default.db 里每个库自己的账号
//     (databases 表:username/password,权限通常仅限本库),失败再回退
//     config 表的 mysql_root。
//   - 导入用 dbmgr.Import(容器内 stdin 重定向)。
//   - 注意:导入后面板 MySQL 与宿主机 MySQL 是两份独立数据,站点连接串
//     需要从 127.0.0.1:3306 改为面板 MySQL(172.17.0.1:3306 / 容器内 kun-mysql)。
//
// 面板 → BT:面板库 mysqldump 导出 → 导入宿主机 MySQL(宝塔侧),供回迁宝塔使用。
package btmigrate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"kunpanel/internal/dbmgr"
	"kunpanel/internal/util"
)

// btDBAccount 宝塔 databases 表中的一条库账号
type btDBAccount struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
	Ps       string `json:"ps"`
}

// btDBList 读宝塔数据库清单(账号+备注)。
// databases 表 11.x 在 data/db/database.db(default.db 里是空表)——btTableDB 探测。
func btDBList() ([]btDBAccount, error) {
	db, err := btTableDB("databases")
	if err != nil {
		return nil, err
	}
	out, err := pythonJSON(fmt.Sprintf(
		`import sqlite3,json;c=sqlite3.connect('%s');c.row_factory=sqlite3.Row;print(json.dumps([dict(r) for r in c.execute('select name,username,password,ps from databases')]))`, db))
	if err != nil {
		return nil, err
	}
	var list []btDBAccount
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, err
	}
	return list, nil
}

// btMySQLRoot 宝塔记录的 MySQL root 密码(config 表;11.x 在 data/db/panel.db)
func btMySQLRoot() (string, error) {
	db, err := btTableDB("config")
	if err != nil {
		return "", err
	}
	out, err := pythonJSON(fmt.Sprintf(
		`import sqlite3,json;c=sqlite3.connect('%s');print(json.dumps([dict(r) for r in c.execute('select mysql_root from config limit 1')]))`, db))
	if err != nil {
		return "", err
	}
	var rows []map[string]any
	if json.Unmarshal([]byte(out), &rows) != nil || len(rows) == 0 {
		return "", fmt.Errorf("config 表无记录")
	}
	pwd := strings.TrimSpace(fmt.Sprint(rows[0]["mysql_root"]))
	if pwd == "" {
		return "", fmt.Errorf("config 表 mysql_root 为空")
	}
	return pwd, nil
}

// btMySQLEnv 执行宝塔 mysql 客户端命令的环境变量(密码不进 ps)
func btMySQLEnv(user, password string) []string {
	return []string{"MYSQL_PWD=" + password}
}

// dumpBTDatabase 导出宝塔库到临时文件,返回文件路径
func dumpBTDatabase(db btDBAccount, w io.Writer) (string, error) {
	dumpBin := filepath.Join(btMySQLBin, "mysqldump")
	if _, err := os.Stat(dumpBin); err != nil {
		// 宿主机自带 mysqldump 兜底
		if p, err := exec.LookPath("mysqldump"); err == nil {
			dumpBin = p
		} else {
			return "", fmt.Errorf("找不到 mysqldump(宝塔与系统都没有)")
		}
	}
	mysqlBin := filepath.Join(btMySQLBin, "mysql")
	if _, err := os.Stat(mysqlBin); err != nil {
		if p, err := exec.LookPath("mysql"); err == nil {
			mysqlBin = p
		}
	}
	// 凭据:库自带账号 → root
	user, password := db.Username, db.Password
	if user == "" || password == "" {
		root, rerr := btMySQLRoot()
		if rerr != nil {
			return "", fmt.Errorf("拿不到数据库 %s 的连接凭据(宝塔未记录账号,root 密码也不可用: %v)", db.Name, rerr)
		}
		user, password = "root", root
	}
	tmp := filepath.Join(util.DataDir(), "btmig_"+db.Name+".sql.gz")
	env := btMySQLEnv(user, password)
	// 连接测试
	if out, err := runWithEnv(env, mysqlBin, "-h", "127.0.0.1", "-P", "3306", "-u", user, "-e", "select 1"); err != nil {
		return "", fmt.Errorf("连接宝塔 MySQL 失败(账号 %s): %s", user, tailStr(string(out), 150))
	}
	fmt.Fprintf(w, "   导出 %s(mysqldump)...\n", db.Name)
	dump := exec.Command(dumpBin, "-h", "127.0.0.1", "-P", "3306", "-u", user,
		"--default-character-set=utf8mb4", "--single-transaction", "--quick", db.Name)
	dump.Env = append(dump.Environ(), env...)
	gz := exec.Command("gzip")
	dumpOut, _ := dump.StdoutPipe()
	gz.Stdin = dumpOut
	gzf, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	gz.Stdout = gzf
	var dumpErr, gzErr strings.Builder
	dump.Stderr = &dumpErr
	gz.Stderr = &gzErr
	if err := gz.Start(); err != nil {
		gzf.Close()
		return "", err
	}
	if err := dump.Start(); err != nil {
		gz.Process.Kill()
		gz.Wait()
		gzf.Close()
		return "", err
	}
	if err := dump.Wait(); err != nil {
		gz.Wait()
		gzf.Close()
		return "", fmt.Errorf("mysqldump %s 失败: %s", db.Name, tailStr(dumpErr.String(), 200))
	}
	gz.Wait()
	gzf.Close()
	return tmp, nil
}

// MigrateBTDatabase 把宝塔的一个库导入面板 MySQL(mysql 实例)
func MigrateBTDatabase(w io.Writer, name string) error {
	list, err := btDBList()
	if err != nil {
		return fmt.Errorf("读取宝塔数据库清单失败: %v", err)
	}
	var acct *btDBAccount
	for i := range list {
		if list[i].Name == name {
			acct = &list[i]
			break
		}
	}
	if acct == nil {
		return fmt.Errorf("宝塔中不存在数据库 %s", name)
	}
	dump, err := dumpBTDatabase(*acct, w)
	if err != nil {
		return err
	}
	defer os.Remove(dump)
	// 面板侧建库 + 导入
	if err := dbmgr.CreateDatabase("mysql", name); err != nil && !strings.Contains(err.Error(), "已存在") && !strings.Contains(err.Error(), "exists") {
		return fmt.Errorf("面板建库失败: %v", err)
	}
	fmt.Fprintf(w, "   导入面板 MySQL(%s)...\n", name)
	if _, err := dbmgr.Import("mysql", name, dump); err != nil {
		return fmt.Errorf("导入失败: %v", err)
	}
	fmt.Fprintf(w, "   %s 已导入面板 MySQL。注意:站点连接串需改指面板 MySQL(172.17.0.1:3306 或数据库页查看)\n", name)
	return nil
}

// MigratePanelDatabase 把面板 MySQL 的一个库导入宿主机 MySQL(宝塔侧)
func MigratePanelDatabase(w io.Writer, name string) error {
	// 1) 面板 mysqldump 导出
	file, err := dbmgr.Backup("mysql", name)
	if err != nil {
		return fmt.Errorf("面板导出 %s 失败: %v", name, err)
	}
	root, err := btMySQLRoot()
	if err != nil {
		return fmt.Errorf("宝塔未记录 MySQL root 密码(%v),无法写入宿主机 MySQL", err)
	}
	mysqlBin := filepath.Join(btMySQLBin, "mysql")
	if _, err := os.Stat(mysqlBin); err != nil {
		if p, e := exec.LookPath("mysql"); e == nil {
			mysqlBin = p
		} else {
			return fmt.Errorf("找不到 mysql 客户端")
		}
	}
	env := btMySQLEnv("root", root)
	// 2) 建库(沿用面板库名)
	fmt.Fprintf(w, "   在宿主机 MySQL 建库 %s 并导入...\n", name)
	if out, err := runWithEnv(env, mysqlBin, "-h", "127.0.0.1", "-u", "root",
		"-e", fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` DEFAULT CHARACTER SET utf8mb4;", sanitizeIdent(name))); err != nil {
		return fmt.Errorf("宿主机建库失败: %s", tailStr(string(out), 200))
	}
	// 3) 导入(gunzip | mysql)
	gz := exec.Command("gunzip", "-c", file)
	sql := exec.Command(mysqlBin, "-h", "127.0.0.1", "-u", "root", name)
	sql.Env = append(sql.Environ(), env...)
	p1, _ := gz.StdoutPipe()
	sql.Stdin = p1
	var errBuf strings.Builder
	sql.Stderr = &errBuf
	if err := sql.Start(); err != nil {
		return err
	}
	if err := gz.Run(); err != nil {
		sql.Process.Kill()
		sql.Wait()
		return fmt.Errorf("解压备份失败: %v", err)
	}
	if err := sql.Wait(); err != nil {
		return fmt.Errorf("导入宿主机 MySQL 失败: %s", tailStr(errBuf.String(), 200))
	}
	fmt.Fprintf(w, "   %s 已导入宿主机 MySQL(宝塔)\n", name)
	return nil
}

// sanitizeIdent 库名白名单(反引号内防注入)
func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// runWithEnv 带环境变量执行命令
func runWithEnv(env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(cmd.Environ(), env...)
	return cmd.CombinedOutput()
}
