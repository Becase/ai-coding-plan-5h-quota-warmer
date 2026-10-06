package main

import (
	"os"
	"testing"
)

func TestWithPathSuffix(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		suffix string
		want   string
	}{
		{"火山 Coding Plan OpenAI Base URL", "https://ark.cn-beijing.volces.com/api/coding/v3", "/chat/completions", "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions"},
		{"火山 Coding Plan Anthropic Base URL", "https://ark.cn-beijing.volces.com/api/coding", "/v1/messages", "https://ark.cn-beijing.volces.com/api/coding/v1/messages"},
		{"已带完整端点不重复拼接", "https://example.com/v1/chat/completions", "/chat/completions", "https://example.com/v1/chat/completions"},
		{"Base URL 带尾斜杠", "https://example.com/api/v3/", "/chat/completions", "https://example.com/api/v3/chat/completions"},
		{"Anthropic 已带 /messages", "https://example.com/v1/messages", "/v1/messages", "https://example.com/v1/messages"},
	}
	for _, c := range cases {
		if got := withPathSuffix(c.url, c.suffix); got != c.want {
			t.Errorf("%s: withPathSuffix(%q, %q) = %q, want %q", c.name, c.url, c.suffix, got, c.want)
		}
	}
}

func validAccount(provider string) Account {
	return Account{
		Provider:  provider,
		APIURL:    "https://example.com/api/coding/v3",
		APIFormat: "openai",
		APIKey:    "sk-test",
		Model:     "m",
		Prompt:    "hello",
	}
}

func TestValidateProviderIsFreeLabel(t *testing.T) {
	cfg := defaultConfig()
	cfg.Accounts = []Account{validAccount("volcengine")}
	if err := Validate(&cfg); err != nil {
		t.Errorf("provider 自由标签不应报错: %v", err)
	}
}

func TestValidateDuplicateProviderRejected(t *testing.T) {
	cfg := defaultConfig()
	cfg.Accounts = []Account{validAccount("volcano"), validAccount("volcano")}
	if err := Validate(&cfg); err == nil {
		t.Error("重复 provider 应报错")
	}
}

func TestParseConfigLegacySingleAccount(t *testing.T) {
	raw := []byte("account:\n  provider: volcano\n  api_url: https://example.com/api/coding/v3\n  api_format: openai\n  api_key: sk-x\n  model: m\n  prompt: hello\n")
	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("旧版单账号配置应可解析: %v", err)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Provider != "volcano" {
		t.Fatalf("旧版 account 应折算为单元素 accounts，实际 %+v", cfg.Accounts)
	}
}

func TestEffectiveSchedules(t *testing.T) {
	global := []Schedule{{ID: "g1", At: "05:00"}}
	personal := []Schedule{{ID: "p1", At: "07:00"}}
	cfg := Config{Schedules: global}
	// 无专属日程 → 回退全局
	if got := effectiveSchedules(cfg, Account{}); len(got) != 1 || got[0].ID != "g1" {
		t.Errorf("无专属日程应回退全局，实际 %+v", got)
	}
	// 有专属日程 → 覆盖全局
	if got := effectiveSchedules(cfg, Account{Schedules: personal}); len(got) != 1 || got[0].ID != "p1" {
		t.Errorf("专属日程应覆盖全局，实际 %+v", got)
	}
}

func TestEnvVarSuffix(t *testing.T) {
	cases := map[string]string{
		"volcano":     "VOLCANO",
		"mock-claude": "MOCK_CLAUDE",
		"a.b_c":       "A_B_C",
	}
	for provider, want := range cases {
		if got := envVarSuffix(provider); got != want {
			t.Errorf("envVarSuffix(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestApplyEnvAPIKeys(t *testing.T) {
	const name = "QUOTA_WARMER_API_KEY_MOCK_CLAUDE"
	os.Setenv(name, "sk-from-env")
	defer os.Unsetenv(name)

	cfg := defaultConfig()
	cfg.Accounts = []Account{validAccount("mock-claude")}
	applyEnvAPIKeys(&cfg)
	if cfg.Accounts[0].APIKey != "sk-from-env" {
		t.Errorf("环境变量应覆盖配置文件密钥，实际 %q", cfg.Accounts[0].APIKey)
	}

	// 未设置环境变量的账号保留原密钥
	other := defaultConfig()
	other.Accounts = []Account{validAccount("volcano")}
	applyEnvAPIKeys(&other)
	if other.Accounts[0].APIKey != "sk-test" {
		t.Errorf("无环境变量时应保留原密钥，实际 %q", other.Accounts[0].APIKey)
	}
}

func TestAliasTrimmed(t *testing.T) {
	cfg := defaultConfig()
	cfg.Accounts = []Account{validAccount("volcano")}
	cfg.Accounts[0].Alias = "  火山主力  "
	if err := Validate(&cfg); err != nil {
		t.Fatalf("别名字段不应导致校验失败: %v", err)
	}
	if cfg.Accounts[0].Alias != "火山主力" {
		t.Errorf("别名应去除首尾空白，实际 %q", cfg.Accounts[0].Alias)
	}
}

func TestValidateAccountSchedules(t *testing.T) {
	cfg := defaultConfig()
	cfg.Accounts = []Account{validAccount("volcano")}
	// 专属日程内重复启用时间应报错
	cfg.Accounts[0].Schedules = []Schedule{{ID: "a", At: "05:00"}, {ID: "b", At: "05:00"}}
	if err := Validate(&cfg); err == nil {
		t.Error("专属日程内重复启用时间应报错")
	}
	// 合法专属日程应通过
	cfg.Accounts[0].Schedules = []Schedule{{ID: "a", At: "05:00"}, {ID: "b", At: "06:00"}}
	if err := Validate(&cfg); err != nil {
		t.Errorf("合法专属日程不应报错: %v", err)
	}
}
