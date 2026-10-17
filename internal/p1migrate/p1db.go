// p1db —— 数据库导入(面板 MySQL)
package p1migrate

import (
	"fmt"
	"strings"

	"kunpanel/internal/dbmgr"
)

func dbmgrCreateAndImport(name, sqlFile string) error {
	if err := dbmgr.CreateDatabase("mysql", name); err != nil &&
		!strings.Contains(err.Error(), "已存在") && !strings.Contains(err.Error(), "exists") {
		return fmt.Errorf("面板建库失败: %v", err)
	}
	out, err := dbmgr.Import("mysql", name, sqlFile)
	if err != nil {
		return err
	}
	_ = out
	return nil
}
