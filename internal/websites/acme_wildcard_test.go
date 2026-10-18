package websites

import (
	"reflect"
	"testing"
)

// 泛域名组合:主域+泛域成对,被泛域名覆盖的子域裁掉(其验证名不再参与,
// 主域一条 CNAME 即可),主域本身必须保留(LE 通配符不匹配裸域)
func TestExpandWildcardDomains(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{[]string{"x.cn", "www.x.cn"}, []string{"x.cn", "*.x.cn"}},
		{[]string{"www.x.cn", "x.cn"}, []string{"x.cn", "*.x.cn"}},
		{[]string{"x.cn"}, []string{"x.cn", "*.x.cn"}},
		{[]string{"a.b.x.cn", "x.cn"}, []string{"x.cn", "*.x.cn"}},
		{[]string{"x.cn", "*.x.cn"}, []string{"x.cn", "*.x.cn"}},
		{[]string{"*.x.cn"}, []string{"x.cn", "*.x.cn"}},
		{[]string{"x.cn", "y.cn"}, []string{"x.cn", "*.x.cn", "y.cn", "*.y.cn"}},
		{[]string{"sub.x.cn"}, []string{"sub.x.cn", "*.sub.x.cn"}},
		{[]string{"www.x.cn", "x.cn", "m.y.cn", "y.cn"}, []string{"x.cn", "*.x.cn", "y.cn", "*.y.cn"}},
	}
	for _, c := range cases {
		if got := expandWildcardDomains(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("expandWildcardDomains(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// 域名勾选白名单:只能取站点已绑定域名的子集,防 API 直调签任意域名
func TestPickSiteDomains(t *testing.T) {
	site := []string{"x.cn", "www.x.cn"}
	if got := pickSiteDomains(site, []string{"x.cn"}); !reflect.DeepEqual(got, []string{"x.cn"}) {
		t.Errorf("勾选子集应生效: %v", got)
	}
	if got := pickSiteDomains(site, []string{"evil.com"}); !reflect.DeepEqual(got, site) {
		t.Errorf("未绑定域名应被拒绝并回退全部: %v", got)
	}
	if got := pickSiteDomains(site, []string{"x.cn", "evil.com"}); !reflect.DeepEqual(got, []string{"x.cn"}) {
		t.Errorf("混合勾选应只保留合法项: %v", got)
	}
	if got := pickSiteDomains(site, nil); !reflect.DeepEqual(got, site) {
		t.Errorf("空勾选应回退全部: %v", got)
	}
}
