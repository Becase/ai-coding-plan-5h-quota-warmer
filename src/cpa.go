package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type cpaHost interface {
	Call(method string, request interface{}, result interface{}) error
}

// Deliberately decode only account identity and availability, never credentials.
type cpaAccount struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Type        string `json:"type,omitempty"`
	Label       string `json:"label,omitempty"`
	Email       string `json:"email,omitempty"`
	Status      string `json:"status,omitempty"`
	Disabled    bool   `json:"disabled"`
	Unavailable bool   `json:"unavailable"`
}

func listCPAAccounts(host cpaHost) ([]cpaAccount, error) {
	if host == nil {
		return nil, errors.New("CPA host unavailable")
	}
	var reply struct {
		Files []cpaAccount `json:"files"`
	}
	if err := host.Call("host.auth.list", struct{}{}, &reply); err != nil {
		return nil, errors.New("CPA account list unavailable")
	}
	accounts := []cpaAccount{}
	for _, acct := range reply.Files {
		if acct.Provider == "" {
			acct.Provider = acct.Type
		}
		acct.Provider = strings.ToLower(acct.Provider)
		if acct.ID == "" || (acct.Provider != "codex" && acct.Provider != "antigravity") {
			continue
		}
		if acct.Label != "" {
			acct.Name = acct.Label
		} else if acct.Email != "" {
			acct.Name = acct.Email
		} else if acct.Name == "" {
			acct.Name = acct.ID
		}
		accounts = append(accounts, acct)
	}
	return accounts, nil
}

type cpaModelRequest struct {
	EntryProtocol  string              `json:"entry_protocol"`
	ExitProtocol   string              `json:"exit_protocol"`
	Model          string              `json:"model"`
	Stream         bool                `json:"stream"`
	Body           []byte              `json:"body"`
	Headers        http.Header         `json:"headers"`
	Query          map[string][]string `json:"query"`
	ForcedProvider string              `json:"forced_provider"`
	AuthID         string              `json:"auth_id"`
}

func executeCPAWarm(host cpaHost, acct Account) warmResult {
	started := time.Now()
	result := warmResult{Provider: acct.Provider, Model: acct.Model}
	accounts, err := listCPAAccounts(host)
	if err != nil {
		result.Error = "无法读取 CPA 账号；未发送预热请求"
		return result
	}
	found := false
	for _, candidate := range accounts {
		if candidate.ID == acct.AuthID && candidate.Provider == acct.CPAProvider {
			found = !candidate.Disabled && !candidate.Unavailable && candidate.Status != "disabled" && candidate.Status != "error"
			break
		}
	}
	if !found {
		result.Error = "绑定的 CPA 账号缺失、停用或不可用；未发送预热请求"
		return result
	}

	body, _ := json.Marshal(map[string]interface{}{
		"model": acct.Model, "stream": false,
		"messages": []map[string]string{{"role": "user", "content": acct.Prompt}},
	})
	request := cpaModelRequest{
		EntryProtocol: "openai", ExitProtocol: "openai", Model: acct.Model,
		AuthID: acct.AuthID, ForcedProvider: acct.CPAProvider, Body: body,
		Headers: http.Header{"Content-Type": {"application/json"}}, Query: map[string][]string{},
	}
	var reply struct {
		StatusCode int    `json:"status_code"`
		Body       []byte `json:"body"`
	}
	err = host.Call("host.model.execute", request, &reply)
	result.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		// A host error can contain OAuth tokens. Do not return its text or retry.
		result.Error = "CPA 模型调用失败，执行结果未确认；未自动重试"
		return result
	}
	result.StatusCode = reply.StatusCode
	if reply.StatusCode < 200 || reply.StatusCode >= 300 {
		result.Error = fmt.Sprintf("CPA 模型调用返回 HTTP %d", reply.StatusCode)
		return result
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error json.RawMessage `json:"error"`
	}
	result.Success = json.Unmarshal(reply.Body, &response) == nil && len(response.Choices) > 0 &&
		strings.TrimSpace(response.Choices[0].Message.Content) != "" && (len(response.Error) == 0 || string(response.Error) == "null")
	if !result.Success {
		result.Error = "CPA 返回的内容不是有效的模型响应"
	}
	return result
}

func decodeHostReply(payload []byte, transportFailed bool, result interface{}) error {
	var reply struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(payload, &reply) != nil || !reply.OK || transportFailed {
		return errors.New("CPA callback failed")
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(reply.Result, result)
}
