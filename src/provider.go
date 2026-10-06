package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// warmResult 单次预热请求的结果。
type warmResult struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	StatusCode int    `json:"status_code,omitempty"`
	Success    bool   `json:"success"`
	LatencyMS  int64  `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
}

// RequestBuilder 根据账号配置构造一次预热 HTTP 请求。
// 支持新的 coding plan / 请求格式时，实现该接口并注册到 builders 即可，
// 调度器与管理界面无需改动。
type RequestBuilder interface {
	Format() string
	Build(acct Account) (*http.Request, error)
}

var builders = map[string]RequestBuilder{
	"openai":    openAIBuilder{},
	"anthropic": anthropicBuilder{},
}

func builderFormats() map[string]struct{} {
	formats := make(map[string]struct{}, len(builders))
	for format := range builders {
		formats[format] = struct{}{}
	}
	return formats
}

func builderFor(format string) (RequestBuilder, error) {
	builder, ok := builders[format]
	if !ok {
		return nil, fmt.Errorf("unsupported api_format %q", format)
	}
	return builder, nil
}

type openAIBuilder struct{}

func (openAIBuilder) Format() string { return "openai" }

func (openAIBuilder) Build(acct Account) (*http.Request, error) {
	return buildRequest(withPathSuffix(acct.APIURL, "/chat/completions"), map[string]string{
		"Authorization": "Bearer " + acct.APIKey,
	}, map[string]interface{}{
		"model": acct.Model,
		"messages": []map[string]string{{
			"role":    "user",
			"content": acct.Prompt,
		}},
		"stream": false,
	})
}

type anthropicBuilder struct{}

func (anthropicBuilder) Format() string { return "anthropic" }

func (anthropicBuilder) Build(acct Account) (*http.Request, error) {
	return buildRequest(withPathSuffix(acct.APIURL, "/v1/messages"), map[string]string{
		"x-api-key":         acct.APIKey,
		"anthropic-version": "2023-06-01",
	}, map[string]interface{}{
		"model":      acct.Model,
		"max_tokens": 16,
		"messages": []map[string]string{{
			"role":    "user",
			"content": acct.Prompt,
		}},
	})
}

// withPathSuffix 兼容 Base URL 与完整端点两种写法：
// 厂商文档通常给出 Base URL（如 .../api/coding/v3），而实际请求
// 要发到拼上标准路径的完整端点。已带后缀的 URL 原样使用。
func withPathSuffix(rawURL, suffix string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if strings.HasSuffix(trimmed, suffix) {
		return trimmed
	}
	return trimmed + suffix
}

func buildRequest(endpoint string, headers map[string]string, body map[string]interface{}) (*http.Request, error) {
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode warm-up request: %w", errMarshal)
	}
	request, errNew := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	if errNew != nil {
		return nil, fmt.Errorf("build warm-up request: %w", errNew)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return request, nil
}

// executeWarm 对账号配置的端点发送一次预热请求并记录结果。
// api_key 只出现在请求头；错误文本额外做防御性脱敏，确保密钥不进日志/状态文件。
func executeWarm(client *http.Client, acct Account) warmResult {
	result := warmResult{Provider: acct.Provider, Model: acct.Model}
	builder, errBuilder := builderFor(acct.APIFormat)
	if errBuilder != nil {
		result.Error = truncateError(errBuilder.Error(), acct.APIKey)
		return result
	}
	request, errBuild := builder.Build(acct)
	if errBuild != nil {
		result.Error = truncateError(errBuild.Error(), acct.APIKey)
		return result
	}
	started := time.Now()
	response, errDo := client.Do(request)
	result.LatencyMS = time.Since(started).Milliseconds()
	if errDo != nil {
		result.Error = truncateError(errDo.Error(), acct.APIKey)
		return result
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	result.Success = response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
	if !result.Success {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		if snippet != "" {
			result.Error = truncateError(fmt.Sprintf("HTTP %d: %s", response.StatusCode, snippet), acct.APIKey)
		} else {
			result.Error = truncateError(fmt.Sprintf("model execution returned HTTP %d", response.StatusCode), acct.APIKey)
		}
	}
	return result
}

// truncateError 截断错误文本并把其中出现的密钥替换为占位符。
func truncateError(message string, apiKey string) string {
	if apiKey != "" {
		message = strings.ReplaceAll(message, apiKey, "sk-***")
	}
	if len(message) > 300 {
		message = message[:300]
	}
	return message
}
