package dbmgr

// 数据库账号管理(MySQL/MariaDB):账号列表、创建、授权、改密、删除。
// PostgreSQL 账号体系差异较大(角色/库级 schema 权限),后续单独支持。

import (
	"fmt"
	"regexp"
	"strings"
)

// DBUser 数据库账号(用户@主机)
type DBUser struct {
	User string `json:"user"`
	Host string `json:"host"`
}

var (
	dbUserNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	// 主机允许:% localhost IP/IP 段/主机名(禁引号反斜杠防注入)
	dbUserHostRe = regexp.MustCompile(`^[A-Za-z0-9._:%-]{1,60}$`)
	dbUserPassRe = regexp.MustCompile(`^[A-Za-z0-9!@#$%^&*()_+=-]{6,64}$`)
)

// 系统内置账号(user 名匹配),列表隐藏且禁止操作
var systemDBUsers = map[string]bool{
	"mysql.session": true, "mysql.sys": true, "mysql.infoschema": true,
	"debian-sys-maint": true, "mariadb.sys": true, "mariadb.backup": true,
	"mysql": true, "healthchecker": true, "mysqlhealthchecker": true,
}

// dbUserSupported 用户管理仅 MySQL/MariaDB
func dbUserSupported(key string) bool {
	return key == "mysql" || key == "mariadb"
}

// ListUsers 账号列表(过滤系统内置账号)
func ListUsers(key string) ([]DBUser, error) {
	if !dbUserSupported(key) {
		return nil, fmt.Errorf("该数据库类型暂不支持账号管理(PostgreSQL 后续支持)")
	}
	out, err := Query(key, "", "SELECT user, host FROM mysql.user ORDER BY user, host")
	if err != nil {
		return nil, err
	}
	var users []DBUser
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" || strings.HasPrefix(l, "user\t") || strings.EqualFold(strings.SplitN(l, "\t", 2)[0], "user") {
			continue
		}
		parts := strings.SplitN(l, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		u, h := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if u == "" || systemDBUsers[u] {
			continue
		}
		users = append(users, DBUser{User: u, Host: h})
	}
	return users, nil
}

func validateUserHost(user, host string) error {
	if !dbUserNameRe.MatchString(user) {
		return fmt.Errorf("用户名仅支持字母数字下划线连字符(1-32 位)")
	}
	if host != "" && !dbUserHostRe.MatchString(host) {
		return fmt.Errorf("主机格式不正确(允许 %%、localhost、IP、IP 段、主机名)")
	}
	if host == "" {
		host = "%"
	}
	return nil
}

func validateUserPassword(password string) error {
	if !dbUserPassRe.MatchString(password) {
		return fmt.Errorf("密码需 6-64 位,仅允许字母数字与 !@#$%%^&*()_+=-")
	}
	return nil
}

// quoteUser 账号的 SQL 引用形式('user'@'host')
func quoteUser(user, host string) string {
	if host == "" {
		host = "%"
	}
	return "'" + user + "'@'" + host + "'"
}

// CreateUser 创建账号(host 空 = %)
func CreateUser(key, user, host, password string) error {
	if !dbUserSupported(key) {
		return fmt.Errorf("该数据库类型暂不支持账号管理")
	}
	if err := validateUserHost(user, host); err != nil {
		return err
	}
	if err := validateUserPassword(password); err != nil {
		return err
	}
	_, err := Query(key, "", "CREATE USER "+quoteUser(user, host)+" IDENTIFIED BY '"+password+"'")
	return err
}

// DropUser 删除账号(root 与系统账号保护)
func DropUser(key, user, host string) error {
	if !dbUserSupported(key) {
		return fmt.Errorf("该数据库类型暂不支持账号管理")
	}
	if err := validateUserHost(user, host); err != nil {
		return err
	}
	if user == "root" || systemDBUsers[user] {
		return fmt.Errorf("系统账号禁止删除")
	}
	_, err := Query(key, "", "DROP USER "+quoteUser(user, host))
	return err
}

// SetUserPassword 修改账号密码
func SetUserPassword(key, user, host, password string) error {
	if !dbUserSupported(key) {
		return fmt.Errorf("该数据库类型暂不支持账号管理")
	}
	if err := validateUserHost(user, host); err != nil {
		return err
	}
	if err := validateUserPassword(password); err != nil {
		return err
	}
	_, err := Query(key, "", "ALTER USER "+quoteUser(user, host)+" IDENTIFIED BY '"+password+"'")
	return err
}

// 可授权的库级权限白名单(自定义勾选用)
var grantablePrivs = []string{
	"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "INDEX", "ALTER",
	"CREATE VIEW", "SHOW VIEW", "CREATE TEMPORARY TABLES", "LOCK TABLES",
	"REFERENCES", "EXECUTE", "EVENT", "TRIGGER",
}

func validatePrivs(privs []string) (string, error) {
	if len(privs) == 0 {
		return "", fmt.Errorf("请至少选择一项权限")
	}
	allow := map[string]bool{}
	for _, p := range grantablePrivs {
		allow[p] = true
	}
	var clean []string
	for _, p := range privs {
		p = strings.ToUpper(strings.TrimSpace(p))
		if !allow[p] {
			return "", fmt.Errorf("不支持的权限: " + p)
		}
		clean = append(clean, p)
	}
	return strings.Join(clean, ", "), nil
}

// grantTarget 库范围 SQL 形式(db 为空或 * 表示全部库)
func grantTarget(db string) (string, error) {
	db = strings.TrimSpace(db)
	if db == "" || db == "*" {
		return "*.*", nil
	}
	if !ValidIdentifier(db) {
		return "", fmt.Errorf("库名格式不正确")
	}
	return "`" + db + "`.*", nil
}

// Grant 授权:privs 仅含一项 "ALL" 表示全部权限
func Grant(key, user, host, db string, privs []string) error {
	if !dbUserSupported(key) {
		return fmt.Errorf("该数据库类型暂不支持账号管理")
	}
	if err := validateUserHost(user, host); err != nil {
		return err
	}
	target, err := grantTarget(db)
	if err != nil {
		return err
	}
	var p string
	if len(privs) == 1 && strings.EqualFold(privs[0], "ALL") {
		p = "ALL PRIVILEGES"
	} else {
		if p, err = validatePrivs(privs); err != nil {
			return err
		}
	}
	_, err = Query(key, "", "GRANT "+p+" ON "+target+" TO "+quoteUser(user, host))
	return err
}

// Revoke 收回账号在某库的全部权限(db 空或 * 表示全部)
func Revoke(key, user, host, db string) error {
	if !dbUserSupported(key) {
		return fmt.Errorf("该数据库类型暂不支持账号管理")
	}
	if err := validateUserHost(user, host); err != nil {
		return err
	}
	target, err := grantTarget(db)
	if err != nil {
		return err
	}
	_, err = Query(key, "", "REVOKE ALL PRIVILEGES, GRANT OPTION ON "+target+" FROM "+quoteUser(user, host))
	return err
}

// ShowGrants 查看账号权限明细
func ShowGrants(key, user, host string) (string, error) {
	if !dbUserSupported(key) {
		return "", fmt.Errorf("该数据库类型暂不支持账号管理")
	}
	if err := validateUserHost(user, host); err != nil {
		return "", err
	}
	return Query(key, "", "SHOW GRANTS FOR "+quoteUser(user, host))
}
