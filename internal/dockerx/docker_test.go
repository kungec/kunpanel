package dockerx

import (
	"testing"
)

// Docker 29 镜像 json 含嵌套对象字段:map[string]string 解析会让该行整体失败,
// 镜像列表缺行、与统计卡数字不一致(用户报:实际 5 个显示 6 个)
func TestParseImagesJSONWithNestedFields(t *testing.T) {
	out := `{"Containers":"0","CreatedAt":"2026-10-14 15:01:38 +0800 CST","CreatedSince":"3 days ago","ID":"a33cf8303b7e","Labels":{"maintainer":"x"},"Repository":"kunpanel/nginx-lua","Tag":"1.27.1.2","Size":"277MB"}
{"Containers":"0","CreatedAt":"2026-10-10 04:08:02 +0800 CST","ID":"0d0945c57f75","Repository":"mariadb","Tag":"10.11","Size":"453MB","Descriptor":{"mediaType":"x"}}
{"Containers":"0","ID":"901b0dbc","Repository":"php","Tag":"8.4-apache","Size":"722MB"}
`
	list := parseImagesJSON(out)
	if len(list) != 3 {
		t.Fatalf("解析条数 = %d, want 3", len(list))
	}
	if list[0].Repo != "kunpanel/nginx-lua" || list[0].Tag != "1.27.1.2" {
		t.Errorf("首条解析错误: %+v", list[0])
	}
	if list[1].Repo != "mariadb" || list[1].Tag != "10.11" {
		t.Errorf("含嵌套字段的 mariadb 行应解析成功: %+v", list[1])
	}
	if list[2].Tag != "8.4-apache" {
		t.Errorf("第三条解析错误: %+v", list[2])
	}
	// 空行与垃圾行不产生条目
	if n := len(parseImagesJSON("")); n != 0 {
		t.Errorf("空输入应返回空列表, got %d", n)
	}
}
