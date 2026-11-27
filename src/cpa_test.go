package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCPAHost struct {
	mu       sync.Mutex
	accounts []cpaAccount
	calls    []cpaModelRequest
	err      error
	entered  chan struct{}
	release  chan struct{}
}

func (h *fakeCPAHost) Call(method string, request interface{}, result interface{}) error {
	h.mu.Lock()
	var value interface{}
	switch method {
	case "host.auth.list":
		value = map[string]interface{}{"files": h.accounts}
	case "host.model.execute":
		h.calls = append(h.calls, request.(cpaModelRequest))
		if h.entered != nil {
			close(h.entered)
			<-h.release
		}
		value = map[string]interface{}{"status_code": 200, "body": []byte(`{"choices":[{"message":{"content":"OK"}}]}`)}
	default:
		h.mu.Unlock()
		return errors.New("unexpected callback")
	}
	err := h.err
	h.mu.Unlock()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, result)
}

func cpaTarget(provider string) Account {
	return Account{Provider: "my-" + provider, Source: "cpa", CPAProvider: provider, AuthID: "selected-" + provider, Model: "test-model", Prompt: "hello"}
}

func TestCPABindsExactAccountAndProvider(t *testing.T) {
	for _, provider := range []string{"codex", "antigravity", "claude", "gemini", "openai-compatible"} {
		t.Run(provider, func(t *testing.T) {
			acct := cpaTarget(provider)
			host := &fakeCPAHost{accounts: []cpaAccount{{ID: "other", Provider: provider}, {ID: acct.AuthID, Provider: provider}}}
			if result := executeCPAWarm(host, acct); !result.Success {
				t.Fatalf("warmup failed: %+v", result)
			}
			if len(host.calls) != 1 || host.calls[0].AuthID != acct.AuthID || host.calls[0].ForcedProvider != provider {
				t.Fatalf("account binding lost: %+v", host.calls)
			}
			request := host.calls[0]
			var body map[string]interface{}
			_ = json.Unmarshal(request.Body, &body)
			if request.EntryProtocol != "openai" || request.ExitProtocol != "openai" || request.Stream || body["model"] != acct.Model {
				t.Fatalf("invalid host request: %+v", request)
			}
		})
	}
}

func TestCPAAccountListAcceptsOtherProviders(t *testing.T) {
	host := &fakeCPAHost{accounts: []cpaAccount{
		{ID: "claude-account", Provider: " Claude ", Label: "Claude account"},
		{ID: "gemini-account", Type: "gemini", Email: "gemini@example.com"},
		{ID: "disabled-account", Provider: "openai-compatible", Disabled: true},
		{ID: "missing-provider"},
		{Provider: "codex"},
	}}
	accounts, err := listCPAAccounts(host)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 3 || accounts[0].Provider != "claude" || accounts[0].Name != "Claude account" || accounts[1].Provider != "gemini" || accounts[1].Name != "gemini@example.com" || !accounts[2].Disabled {
		t.Fatalf("unexpected account identities: %+v", accounts)
	}
}

func TestCPAConfigurationRequiresBindingAndModel(t *testing.T) {
	for _, field := range []string{"auth_id", "cpa_provider", "model"} {
		t.Run(field, func(t *testing.T) {
			acct := cpaTarget("claude")
			switch field {
			case "auth_id":
				acct.AuthID = " "
			case "cpa_provider":
				acct.CPAProvider = " "
			case "model":
				acct.Model = " "
			}
			cfg := defaultConfig()
			cfg.Accounts = []Account{acct}
			if err := validateConfig(&cfg, true); err == nil {
				t.Fatalf("empty %s was accepted", field)
			}
		})
	}
}

func TestCPAUnavailableDoesNotExecuteOrFallback(t *testing.T) {
	acct := cpaTarget("codex")
	for _, account := range []cpaAccount{
		{ID: "other", Provider: "codex"},
		{ID: acct.AuthID, Provider: "antigravity"},
		{ID: acct.AuthID, Provider: "codex", Disabled: true},
		{ID: acct.AuthID, Provider: "codex", Unavailable: true},
	} {
		host := &fakeCPAHost{accounts: []cpaAccount{account}}
		if result := executeCPAWarm(host, acct); result.Success || len(host.calls) != 0 {
			t.Fatalf("unavailable account dispatched: %+v %+v", result, host.calls)
		}
	}
	if result := executeCPAWarm(nil, acct); result.Success {
		t.Fatal("missing CPA host must fail")
	}
}

func TestCPAErrorsNeverExposeHostSecrets(t *testing.T) {
	host := &fakeCPAHost{err: errors.New("access_token=private-oauth-token")}
	result := executeCPAWarm(host, cpaTarget("codex"))
	if result.Success || strings.Contains(result.Error, "private-oauth-token") {
		t.Fatalf("host error exposed: %+v", result)
	}
	if err := decodeHostReply([]byte(`{"ok":false,"error":{"message":"private-oauth-token"}}`), false, nil); err == nil || strings.Contains(err.Error(), "private-oauth-token") {
		t.Fatal("host envelope must suppress upstream error text")
	}
}

func TestCPAConfigurationAndSharedSchedule(t *testing.T) {
	cfg := defaultConfig()
	cfg.Accounts = []Account{cpaTarget("codex"), cpaTarget("antigravity"), validAccount("manual"), cpaTarget("claude")}
	cfg.Accounts[0].APIKey = "must-be-removed"
	cfg.Accounts[0].APIURL = "unused"
	cfg.Accounts[0].APIFormat = "anthropic"
	if err := validateConfig(&cfg, true); err != nil {
		t.Fatal(err)
	}
	if cfg.Accounts[0].APIKey != "" || cfg.Accounts[0].APIURL != "" || cfg.Accounts[0].APIFormat != "" || cfg.Accounts[2].Source != "api_key" {
		t.Fatal("CPA credentials retained or legacy API key configuration broken")
	}
	at := time.Date(2026, 11, 27, 5, 30, 0, 0, time.FixedZone("CST", 8*3600))
	if accounts := accountsScheduledAt(cfg, at); len(accounts) != 4 {
		t.Fatalf("shared schedule dropped accounts: %+v", accounts)
	}
	if err := Validate(&cfg); err == nil {
		t.Fatal("standalone configuration should reject CPA accounts")
	}
}

func TestDefaultScheduleMetadataPreservesSavedSchedules(t *testing.T) {
	app := registerTestPlugin(t, &fakeCPAHost{}, t.TempDir())
	cfg := app.server.warmer.currentConfig()
	cfg.Schedules = []Schedule{{ID: "custom", At: "09:17"}}
	raw, _ := json.Marshal(cfg)
	prefix := "/v0/management/plugins/" + pluginID + "/api/"
	if response := app.handle(managementRequest{Method: "PUT", Path: prefix + "config", Body: raw}); response.StatusCode != 200 {
		t.Fatalf("cannot save custom schedule: %s", response.Body)
	}
	response := app.handle(managementRequest{Method: "GET", Path: prefix + "meta"})
	var meta struct {
		Defaults []Schedule `json:"default_schedules"`
	}
	if err := json.Unmarshal(response.Body, &meta); err != nil {
		t.Fatal(err)
	}
	want := []Schedule{
		{ID: "morning-0530", At: "05:30"}, {ID: "noon-1031", At: "10:31"},
		{ID: "afternoon-1532", At: "15:32"}, {ID: "evening-2033", At: "20:33"},
	}
	if len(meta.Defaults) != len(want) {
		t.Fatalf("wrong default schedules: %+v", meta.Defaults)
	}
	for index, item := range meta.Defaults {
		if item.ID != want[index].ID || item.At != want[index].At || !scheduleEnabled(item) {
			t.Fatalf("wrong default schedule: %+v", item)
		}
	}
	app.Close()
	reloaded := registerTestPlugin(t, &fakeCPAHost{}, app.dir)
	got := reloaded.server.warmer.currentConfig().Schedules
	if len(got) != 1 || got[0].ID != "custom" || got[0].At != "09:17" {
		t.Fatalf("new defaults overwrote saved schedules: %+v", got)
	}
}

func registerTestPlugin(t *testing.T, host cpaHost, dir string) *pluginApp {
	t.Helper()
	app := &pluginApp{host: host}
	raw, _ := json.Marshal(map[string]interface{}{"schema_version": 6, "config_yaml": []byte("state_dir: " + filepath.ToSlash(dir))})
	var reply struct{ OK bool }
	_ = json.Unmarshal(app.Call("plugin.register", raw), &reply)
	if !reply.OK {
		t.Fatal("plugin registration failed")
	}
	t.Cleanup(app.Close)
	return app
}

func TestPluginMixedAccountsPersistAndReload(t *testing.T) {
	manualCalls := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Error("manual API key not sent")
		}
		manualCalls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer endpoint.Close()
	acct := cpaTarget("codex")
	host := &fakeCPAHost{accounts: []cpaAccount{{ID: acct.AuthID, Provider: "codex"}}}
	dir := t.TempDir()
	app := registerTestPlugin(t, host, dir)
	cfg := app.server.warmer.currentConfig()
	manual := validAccount("manual")
	manual.APIURL = endpoint.URL
	cfg.Accounts = []Account{acct, manual}
	raw, _ := json.Marshal(cfg)
	prefix := "/v0/management/plugins/" + pluginID + "/api/"
	response := app.handle(managementRequest{Method: "PUT", Path: prefix + "config", Body: raw})
	if response.StatusCode != 200 || strings.Contains(string(response.Body), "sk-test") {
		t.Fatalf("configuration save failed or leaked key: %s", response.Body)
	}
	response = app.handle(managementRequest{Method: "POST", Path: prefix + "run"})
	var run struct{ Results []warmResult }
	_ = json.Unmarshal(response.Body, &run)
	if response.StatusCode != 200 || len(run.Results) != 2 || !run.Results[0].Success || !run.Results[1].Success || manualCalls != 1 || len(host.calls) != 1 {
		t.Fatalf("mixed warmup failed: %s", response.Body)
	}
	// An empty key on subsequent saves retains the manual credential only.
	cfg.Accounts[1].APIKey = ""
	raw, _ = json.Marshal(cfg)
	response = app.handle(managementRequest{Method: "PUT", Path: prefix + "config", Body: raw})
	if response.StatusCode != 200 || app.server.warmer.currentConfig().Accounts[1].APIKey != "sk-test" {
		t.Fatal("write-only API key was lost")
	}
	app.Close()
	if response := app.handle(managementRequest{Method: "POST", Path: prefix + "run"}); response.StatusCode != 503 {
		t.Fatal("stopped plugin accepted a warmup")
	}
	reloaded := registerTestPlugin(t, host, dir)
	if got := reloaded.server.warmer.currentConfig(); len(got.Accounts) != 2 || got.Accounts[0].AuthID != acct.AuthID || got.Accounts[1].APIKey != "sk-test" {
		t.Fatalf("plugin restart lost configuration: %+v", maskedConfig(got))
	}
	if reloaded.server.warmer.Status().LastRun == nil {
		t.Fatal("plugin restart lost last result")
	}
}

func TestPluginCloseWaitsForHostCallback(t *testing.T) {
	acct := cpaTarget("codex")
	host := &fakeCPAHost{accounts: []cpaAccount{{ID: acct.AuthID, Provider: "codex"}}, entered: make(chan struct{}), release: make(chan struct{})}
	app := registerTestPlugin(t, host, t.TempDir())
	cfg := app.server.warmer.currentConfig()
	cfg.Accounts = []Account{acct}
	app.server.warmer.Configure(cfg)
	runDone := make(chan struct{})
	go func() {
		app.handle(managementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginID + "/api/run"})
		close(runDone)
	}()
	<-host.entered
	closed := make(chan struct{})
	go func() { app.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("plugin closed with a live callback")
	case <-time.After(25 * time.Millisecond):
	}
	close(host.release)
	<-runDone
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("plugin failed to stop after the callback completed")
	}
}
