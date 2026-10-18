package websites

import "testing"

func TestListDNSProviders(t *testing.T) {
	ps := ListDNSProviders()
	if len(ps) < 5 {
		t.Fatalf("服务商数量异常: %d", len(ps))
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Key == "" || p.Name == "" || len(p.Fields) == 0 {
			t.Errorf("服务商 %s 定义不完整", p.Key)
		}
		if seen[p.Key] {
			t.Errorf("服务商 key 重复: %s", p.Key)
		}
		seen[p.Key] = true
	}
}

// 每个服务商都能用示例凭据构造 lego provider(构造为本地操作,不调 API)
func TestBuildDNS01Provider(t *testing.T) {
	t.Setenv("KP_DATA_DIR", t.TempDir())
	samples := map[string]map[string]string{
		"alidns":      {"api_key": "ak", "secret_key": "sk"},
		"dnspod":      {"login_token": "1,token"},
		"cloudflare":  {"auth_token": "token"},
		"huaweicloud": {"access_key": "ak", "secret_key": "sk", "region": "cn-north-4"},
		"baiducloud":  {"access_key": "ak", "secret_key": "sk"},
		"godaddy":     {"api_key": "key", "api_secret": "secret"},
		"namesilo":    {"api_key": "key"},
	}
	for key, creds := range samples {
		if err := SetDNSAPI(key, creds); err != nil {
			t.Fatalf("保存 %s 凭据失败: %v", key, err)
		}
		_, err := buildDNS01Provider()
		if key == "huaweicloud" {
			// 华为云构造时在线解析 Region 项目 ID:假凭据返回 401 错误属预期
			// (凭据错误在签发前即暴露,不会等到 TXT 操作才失败)
			if err == nil {
				t.Errorf("huaweicloud 假凭据应在线校验失败")
			}
			continue
		}
		if err != nil {
			t.Errorf("构造 %s provider 失败: %v", key, err)
		}
	}
	// 未配置时报错而非 panic
	t.Setenv("KP_DATA_DIR", t.TempDir())
	if _, err := buildDNS01Provider(); err == nil {
		t.Errorf("未配置时应返回错误")
	}
}
