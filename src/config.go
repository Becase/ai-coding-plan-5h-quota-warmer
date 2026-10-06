package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	maxLogsDefault = 200
	maxLogsLimit   = 2000
	listenDefault  = ":1688"
	// DefaultStateFile 默认状态文件路径（相对工作目录）
	DefaultStateFile = "data/state.json"
)

// Schedule 每日固定时间预热计划，格式与 codex-5h-quota-warmer 保持一致。
type Schedule struct {
	ID      string `yaml:"id"       json:"id"`
	At      string `yaml:"at"       json:"at"`
	Enabled *bool  `yaml:"enabled"  json:"enabled,omitempty"`
}

// Account 预热目标账号。provider 为扩展点，未来可支持其他 coding plan。
// Alias 非空时管理页标签页显示别名；Schedules 非空时覆盖全局兜底日程。
type Account struct {
	Provider  string     `yaml:"provider"          json:"provider"`
	Alias     string     `yaml:"alias,omitempty"   json:"alias,omitempty"`
	APIURL    string     `yaml:"api_url"           json:"api_url"`
	APIFormat string     `yaml:"api_format"        json:"api_format"`
	APIKey    string     `yaml:"api_key"           json:"api_key"`
	Model     string     `yaml:"model"             json:"model"`
	Prompt    string     `yaml:"prompt"            json:"prompt"`
	Schedules []Schedule `yaml:"schedules,omitempty" json:"schedules,omitempty"`
}

// apiKeyEnvPrefix 账号密钥环境变量前缀：QUOTA_WARMER_API_KEY_<PROVIDER>。
const apiKeyEnvPrefix = "QUOTA_WARMER_API_KEY_"

// applyEnvAPIKeys 用环境变量覆盖账号密钥（启动加载时生效，优先于配置文件）。
// provider 转大写，非字母数字字符替换为下划线，如 mock-claude → MOCK_CLAUDE。
func applyEnvAPIKeys(cfg *Config) {
	for index := range cfg.Accounts {
		name := apiKeyEnvPrefix + envVarSuffix(cfg.Accounts[index].Provider)
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			cfg.Accounts[index].APIKey = value
		}
	}
}

func envVarSuffix(provider string) string {
	var builder strings.Builder
	for _, r := range strings.ToUpper(provider) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('_')
		}
	}
	return builder.String()
}

// Config 服务配置。ManagementKey 标记 json:"-"，任何 API 响应都不会携带它。
type Config struct {
	Listen        string     `yaml:"listen"          json:"listen"`
	Timezone      string     `yaml:"timezone"        json:"timezone"`
	MaxLogEntries int        `yaml:"max_log_entries" json:"max_log_entries"`
	StateFile     string     `yaml:"state_file"      json:"state_file"`
	ManagementKey string     `yaml:"management_key"  json:"-"`
	Schedules     []Schedule `yaml:"schedules"       json:"schedules"`
	Accounts      []Account  `yaml:"accounts"        json:"accounts"`
}

func defaultConfig() Config {
	return Config{
		Listen:        listenDefault,
		Timezone:      "Asia/Shanghai",
		MaxLogEntries: maxLogsDefault,
		StateFile:     DefaultStateFile,
		Accounts: []Account{{
			Provider:  "volcano",
			APIURL:    "https://ark.cn-beijing.volces.com/api/coding/v3",
			APIFormat: "openai",
		}},
		Schedules: []Schedule{
			{ID: "morning-05", At: "05:00"},
			{ID: "noon-10", At: "10:00"},
			{ID: "afternoon-15", At: "15:00"},
		},
	}
}

// legacyAccount 兼容旧版单账号写法（account:），读取时折算为 accounts 列表。
type legacyAccount struct {
	Account  *Account   `yaml:"account"`
	Accounts []Account `yaml:"accounts"`
}


func scheduleEnabled(item Schedule) bool {
	return item.Enabled == nil || *item.Enabled
}

// effectiveSchedules 返回账号实际生效的日程：账号自带日程时覆盖全局，否则回退全局兜底。
func effectiveSchedules(cfg Config, acct Account) []Schedule {
	if len(acct.Schedules) > 0 {
		return acct.Schedules
	}
	return cfg.Schedules
}

// validateSchedules 校验一组日程（id 必填唯一、HH:MM 格式、启用行时间不重复）。
func validateSchedules(list []Schedule, label string) error {
	seen := make(map[string]struct{}, len(list))
	seenTimes := make(map[string]struct{}, len(list))
	for index := range list {
		item := &list[index]
		item.ID = strings.TrimSpace(item.ID)
		item.At = strings.TrimSpace(item.At)
		if item.ID == "" {
			return fmt.Errorf("%s[%d].id is required", label, index)
		}
		if item.At == "" {
			return fmt.Errorf("%s[%d].at is required", label, index)
		}
		if _, errParse := time.Parse("15:04", item.At); errParse != nil {
			return fmt.Errorf("%s[%d].at must use HH:MM: %w", label, index, errParse)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return fmt.Errorf("%s id %q is duplicated", label, item.ID)
		}
		seen[item.ID] = struct{}{}
		if scheduleEnabled(*item) {
			if _, duplicate := seenTimes[item.At]; duplicate {
				return fmt.Errorf("more than one enabled %s uses %s", label, item.At)
			}
			seenTimes[item.At] = struct{}{}
		}
	}
	return nil
}

// LoadConfigFile 从 YAML 文件加载配置；文件不存在时返回默认配置。
func LoadConfigFile(path string) (Config, error) {
	raw, errRead := os.ReadFile(path)
	if errors.Is(errRead, os.ErrNotExist) {
		cfg := defaultConfig()
		applyEnvAPIKeys(&cfg)
		return cfg, Validate(&cfg)
	}
	if errRead != nil {
		return Config{}, fmt.Errorf("read configuration: %w", errRead)
	}
	return ParseConfig(raw)
}

// ParseConfig 解析 YAML 配置并校验，缺省字段保留默认值。
func ParseConfig(raw []byte) (Config, error) {
	cfg := defaultConfig()
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return Config{}, fmt.Errorf("decode configuration: %w", errUnmarshal)
		}
		// 兼容旧版单账号写法：YAML 只有 account 没有 accounts 时折算为列表
		var probe legacyAccount
		if errProbe := yaml.Unmarshal(raw, &probe); errProbe == nil {
			if len(probe.Accounts) == 0 && probe.Account != nil {
				cfg.Accounts = []Account{*probe.Account}
			}
		}
	}
	// 环境变量密钥优先于配置文件（仅启动加载时应用）
	applyEnvAPIKeys(&cfg)
	if errValidate := Validate(&cfg); errValidate != nil {
		return Config{}, errValidate
	}
	return cfg, nil
}

// Validate 校验配置（YAML 与管理 API 的 JSON 修改共用同一套规则）。
func Validate(cfg *Config) error {
	cfg.Listen = strings.TrimSpace(cfg.Listen)
	if cfg.Listen == "" {
		cfg.Listen = listenDefault
	}
	cfg.Timezone = strings.TrimSpace(cfg.Timezone)
	if cfg.Timezone == "" {
		cfg.Timezone = "Asia/Shanghai"
	}
	if _, errLocation := time.LoadLocation(cfg.Timezone); errLocation != nil {
		return fmt.Errorf("timezone %q is invalid: %w", cfg.Timezone, errLocation)
	}
	if cfg.MaxLogEntries <= 0 {
		cfg.MaxLogEntries = maxLogsDefault
	}
	if cfg.MaxLogEntries > maxLogsLimit {
		return fmt.Errorf("max_log_entries must not exceed %d", maxLogsLimit)
	}
	cfg.StateFile = strings.TrimSpace(cfg.StateFile)
	if cfg.StateFile == "" {
		cfg.StateFile = DefaultStateFile
	}
	stateFile, errPath := filepath.Abs(cfg.StateFile)
	if errPath != nil {
		return fmt.Errorf("resolve state_file: %w", errPath)
	}
	cfg.StateFile = stateFile
	if errSchedules := validateSchedules(cfg.Schedules, "schedules"); errSchedules != nil {
		return errSchedules
	}
	if len(cfg.Accounts) == 0 {
		return fmt.Errorf("at least one account is required")
	}
	seenProviders := make(map[string]struct{}, len(cfg.Accounts))
	for index := range cfg.Accounts {
		acct := &cfg.Accounts[index]
		acct.Provider = strings.ToLower(strings.TrimSpace(acct.Provider))
		if acct.Provider == "" {
			return fmt.Errorf("accounts[%d].provider is required", index)
		}
		if _, duplicate := seenProviders[acct.Provider]; duplicate {
			return fmt.Errorf("provider %q is duplicated", acct.Provider)
		}
		seenProviders[acct.Provider] = struct{}{}
		acct.Alias = strings.TrimSpace(acct.Alias)
		if len(acct.Schedules) > 0 {
			if errAccount := validateSchedules(acct.Schedules, fmt.Sprintf("accounts[%d].schedules", index)); errAccount != nil {
				return errAccount
			}
		}
		acct.APIURL = strings.TrimSpace(acct.APIURL)
		if acct.APIURL == "" {
			return fmt.Errorf("accounts[%d].api_url is required", index)
		}
		parsedURL, errURL := url.Parse(acct.APIURL)
		if errURL != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
			return fmt.Errorf("accounts[%d].api_url %q is not a valid URL", index, acct.APIURL)
		}
		if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
			return fmt.Errorf("accounts[%d].api_url must use http or https", index)
		}
		acct.APIFormat = strings.ToLower(strings.TrimSpace(acct.APIFormat))
		if acct.APIFormat == "" {
			acct.APIFormat = "openai"
		}
		if _, known := builderFormats()[acct.APIFormat]; !known {
			return fmt.Errorf("accounts[%d].api_format %q is not supported", index, acct.APIFormat)
		}
		acct.APIKey = strings.TrimSpace(acct.APIKey)
		if acct.APIKey == "" {
			return fmt.Errorf("accounts[%d].api_key is required", index)
		}
		acct.Model = strings.TrimSpace(acct.Model)
		if acct.Model == "" {
			return fmt.Errorf("accounts[%d].model is required", index)
		}
		acct.Prompt = strings.TrimSpace(acct.Prompt)
		if acct.Prompt == "" {
			return fmt.Errorf("accounts[%d].prompt is required", index)
		}
	}
	return nil
}

func cloneConfig(cfg Config) Config {
	copyCfg := cfg
	copyCfg.Schedules = append([]Schedule(nil), cfg.Schedules...)
	copyCfg.Accounts = make([]Account, len(cfg.Accounts))
	for index, acct := range cfg.Accounts {
		acct.Schedules = append([]Schedule(nil), acct.Schedules...)
		copyCfg.Accounts[index] = acct
	}
	return copyCfg
}

// SaveConfigFile 原子写回 YAML 配置（临时文件 + 替换），供管理页面保存设置使用。
func SaveConfigFile(path string, cfg Config) error {
	raw, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		return fmt.Errorf("encode configuration: %w", errMarshal)
	}
	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create configuration directory: %w", errMkdir)
	}
	file, errCreate := os.CreateTemp(dir, ".coding-plan-5h-quota-warmer-config-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create temporary configuration: %w", errCreate)
	}
	tempPath := file.Name()
	_, errWrite := file.Write(raw)
	if errWrite == nil {
		errWrite = file.Sync()
	}
	errClose := file.Close()
	if errWrite == nil {
		errWrite = errClose
	}
	if errWrite != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("write temporary configuration: %w", errWrite)
	}
	if errRename := os.Rename(tempPath, path); errRename != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace configuration: %w", errRename)
	}
	return nil
}

// maskedAPIKey 返回掩码后的密钥（保留前 3 位与后 4 位）。
func maskedAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "***"
	}
	return key[:3] + "…" + key[len(key)-4:]
}
