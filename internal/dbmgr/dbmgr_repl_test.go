package dbmgr

import "testing"

func TestParseReplCoords(t *testing.T) {
	// MySQL 5.7/8.0 mysqldump 头部
	f, p, err := parseReplCoords("-- CHANGE MASTER TO MASTER_LOG_FILE='mysql-bin.000003', MASTER_LOG_POS=157;")
	if err != nil || f != "mysql-bin.000003" || p != "157" {
		t.Errorf("MySQL 坐标解析: %v %s %s", err, f, p)
	}
	// MySQL 8.4 新语句
	f, p, err = parseReplCoords("-- CHANGE REPLICATION SOURCE TO SOURCE_LOG_FILE='binlog.000009', SOURCE_LOG_POS=1203;")
	if err != nil || f != "binlog.000009" || p != "1203" {
		t.Errorf("8.4 坐标解析: %v %s %s", err, f, p)
	}
	if _, _, err := parseReplCoords("-- 没有坐标行"); err == nil {
		t.Error("无坐标行应报错")
	}
}

func TestParseSlaveStatus(t *testing.T) {
	raw := "Slave_IO_Running\tSlave_SQL_Running\tSeconds_Behind_Master\tLast_IO_Error\n" +
		"Yes\tNo\tNULL\tAccess denied"
	st := parseSlaveStatus(raw)
	if st["Slave_IO_Running"] != "Yes" || st["Slave_SQL_Running"] != "No" || st["Seconds_Behind_Master"] != "NULL" {
		t.Errorf("解析不对: %v", st)
	}
	if len(parseSlaveStatus("only-header\tline")) != 0 {
		t.Error("无数据行应返回空")
	}
}

func TestReplServerID(t *testing.T) {
	if replServerID("mysql") == replServerID("mariadb") {
		t.Error("不同实例的 server-id 应不同")
	}
	if replServerID("mysql") != replServerID("mysql") {
		t.Error("同实例 server-id 应稳定")
	}
}

func TestValidateUserHost(t *testing.T) {
	for _, ok := range []string{"%", "localhost", "192.168.1.10", "10.0.0.%", "db.corp.example.com"} {
		if err := validateUserHost("u1", ok); err != nil {
			t.Errorf("合法主机 %s 被拒: %v", ok, err)
		}
	}
	for _, bad := range []string{"a'b", "x;y", "h\\ost", "' OR '1'='1"} {
		if err := validateUserHost("u1", bad); err == nil {
			t.Errorf("危险主机 %s 未被拒", bad)
		}
	}
	if err := validateUserHost("bad name", "%"); err == nil {
		t.Error("用户名带空格应被拒")
	}
}

func TestValidatePrivs(t *testing.T) {
	p, err := validatePrivs([]string{"select", "INSERT"})
	if err != nil || p != "SELECT, INSERT" {
		t.Errorf("权限归一: %v %v", p, err)
	}
	if _, err := validatePrivs([]string{"DROP; DROP DATABASE x"}); err == nil {
		t.Error("注入权限应被拒")
	}
}

func TestGrantTarget(t *testing.T) {
	if v, _ := grantTarget(""); v != "*.*" {
		t.Errorf("空库应为 *.*: %v", v)
	}
	if v, _ := grantTarget("mydb"); v != "`mydb`.*" {
		t.Errorf("库名应加反引号: %v", v)
	}
	if _, err := grantTarget("db; DROP DATABASE x"); err == nil {
		t.Error("非法库名应被拒")
	}
}
