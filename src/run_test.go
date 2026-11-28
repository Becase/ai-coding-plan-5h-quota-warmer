package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunProviderSelection(t *testing.T) {
	for _, pluginMode := range []bool{false, true} {
		name := "standalone"
		if pluginMode {
			name = "plugin"
		}
		t.Run(name, func(t *testing.T) {
			var manualCalls atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				manualCalls.Add(1)
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
			}))
			defer endpoint.Close()
			first, second := validAccount("first"), validAccount("second")
			first.APIURL, second.APIURL = endpoint.URL, endpoint.URL
			host := &fakeCPAHost{}
			if pluginMode {
				second = cpaTarget("claude")
				host.accounts = []cpaAccount{{ID: second.AuthID, Provider: second.CPAProvider}}
			}
			cfg := defaultConfig()
			cfg.Accounts, cfg.Schedules = []Account{first, second}, nil
			cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
			w := newWarmer(endpoint.Client())
			w.host = host
			w.Configure(cfg)
			defer w.Stop()
			s := newServer(w, "test-management-key", "")
			var run func(string) (int, []byte)
			if pluginMode {
				app := &pluginApp{host: host, server: s}
				run = func(body string) (int, []byte) {
					response := app.handle(managementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginID + "/api/run", Body: []byte(body)})
					return response.StatusCode, response.Body
				}
			} else {
				run = func(body string) (int, []byte) {
					r := httptest.NewRequest("POST", "/api/run", strings.NewReader(body))
					r.Header.Set("Authorization", "Bearer test-management-key")
					recorder := httptest.NewRecorder()
					s.routes().ServeHTTP(recorder, r)
					return recorder.Code, recorder.Body.Bytes()
				}
			}
			for _, test := range []struct {
				body string
				code int
			}{
				{`{"provider":""}`, 400}, {`{"provider":"  "}`, 400},
				{`{"provider":null}`, 400}, {`{"provider":123}`, 400},
				{`{"provider":"missing"}`, 404}, {`{`, 400}, {`[]`, 400},
				{`null`, 400}, {`{} {}`, 400}, {strings.Repeat(" ", maxBodyBytes) + "{}", 400},
			} {
				if code, _ := run(test.body); code != test.code {
					t.Fatalf("body %.80q: got %d, want %d", test.body, code, test.code)
				}
			}
			if manualCalls.Load() != 0 || len(host.calls) != 0 {
				t.Fatal("invalid selector executed accounts")
			}
			code, body := run(`{"provider":" FIRST "}`)
			var response struct {
				Results []warmResult `json:"results"`
				Status  Status       `json:"status"`
			}
			if err := json.Unmarshal(body, &response); err != nil || code != 200 || len(response.Results) != 1 || response.Results[0].Provider != first.Provider || !response.Results[0].Success {
				t.Fatalf("selected warmup failed: %d %s", code, body)
			}
			if manualCalls.Load() != 1 || len(host.calls) != 0 {
				t.Fatal("selected manual warmup executed another provider")
			}
			if response.Results[0].FinishedAt == nil || response.Results[0].FinishedAt.IsZero() || response.Results[0].FinishedAt.Location() != time.UTC {
				t.Fatal("missing UTC completion timestamp")
			}
			firstTime := *response.Results[0].FinishedAt
			code, body = run(`{"provider":"` + second.Provider + `"}`)
			_ = json.Unmarshal(body, &response)
			if code != 200 || len(response.Results) != 1 || !response.Results[0].Success || len(response.Status.LastResults) != 2 || response.Status.LastResults[0].Provider != first.Provider || !response.Status.LastResults[0].FinishedAt.Equal(firstTime) {
				t.Fatalf("other provider's latest result was lost: %s", body)
			}
			if pluginMode && (manualCalls.Load() != 1 || len(host.calls) != 1 || host.calls[0].AuthID != second.AuthID) {
				t.Fatal("selected CPA warmup executed another account")
			}
			for _, body := range []string{"", "{}"} {
				code, raw := run(body)
				_ = json.Unmarshal(raw, &response)
				if code != 200 || len(response.Results) != 2 {
					t.Fatalf("all-provider compatibility failed: %s", raw)
				}
			}
		})
	}
}

func TestRunRejectsConcurrentProviderWarmup(t *testing.T) {
	acct := cpaTarget("codex")
	host := &fakeCPAHost{accounts: []cpaAccount{{ID: acct.AuthID, Provider: "codex"}}, entered: make(chan struct{}), release: make(chan struct{})}
	app := registerTestPlugin(t, host, t.TempDir())
	cfg := app.server.warmer.currentConfig()
	cfg.Accounts, cfg.Schedules = []Account{acct}, nil
	app.server.warmer.Configure(cfg)
	finished := make(chan struct{})
	go func() {
		app.handle(managementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginID + "/api/run"})
		close(finished)
	}()
	<-host.entered
	for _, body := range []string{"{}", `{"provider":"` + acct.Provider + `"}`} {
		response := app.handle(managementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginID + "/api/run", Body: []byte(body)})
		if response.StatusCode != 409 {
			t.Errorf("concurrent warmup returned %d", response.StatusCode)
		}
	}
	close(host.release)
	<-finished
}

func TestLatestResultsSurviveScheduleRestartRenameAndRemoval(t *testing.T) {
	first, second := cpaTarget("codex"), cpaTarget("claude")
	host := &fakeCPAHost{accounts: []cpaAccount{{ID: first.AuthID, Provider: first.CPAProvider}, {ID: second.AuthID, Provider: second.CPAProvider}}}
	cfg := defaultConfig()
	cfg.Accounts, cfg.Schedules = []Account{first, second}, nil
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	legacyTime := time.Date(2026, 11, 27, 21, 30, 0, 0, time.UTC)
	if err := writeRunState(cfg.StateFile, runState{LastRun: legacyTime, LastResults: []warmResult{{Provider: second.Provider, Success: true}, {Provider: first.Provider, Success: true}}}); err != nil {
		t.Fatal(err)
	}
	w := newWarmer(http.DefaultClient)
	w.host = host
	w.Configure(cfg)
	defer w.Stop()
	if got := w.Status().LastResults; len(got) != 2 || got[0].Provider != first.Provider || got[0].FinishedAt == nil || !got[0].FinishedAt.Equal(legacyTime) {
		t.Fatalf("legacy result migration failed: %+v", got)
	}
	if _, err := w.Run("schedule:test", []Account{second}); err != nil {
		t.Fatal(err)
	}
	results := w.Status().LastResults
	if len(results) != 2 || !results[0].FinishedAt.Equal(legacyTime) || !results[1].FinishedAt.After(legacyTime) {
		t.Fatalf("scheduled warmup overwrote unrelated result: %+v", results)
	}
	w.Stop()
	reloaded := newWarmer(http.DefaultClient)
	reloaded.Configure(cfg)
	defer reloaded.Stop()
	got := reloaded.Status().LastResults
	if len(got) != 2 || !got[0].FinishedAt.Equal(*results[0].FinishedAt) || !got[1].FinishedAt.Equal(*results[1].FinishedAt) {
		t.Fatal("restart lost per-provider timestamps")
	}
	reloaded.RenameProviders(map[string]string{first.Provider: "renamed"})
	cfg = reloaded.currentConfig()
	cfg.Accounts[0].Provider = "renamed"
	reloaded.Configure(cfg)
	if got = reloaded.Status().LastResults; len(got) != 2 || got[0].Provider != "renamed" || !got[0].FinishedAt.Equal(legacyTime) {
		t.Fatal("rename lost existing result")
	}
	cfg.Accounts = cfg.Accounts[1:]
	reloaded.Configure(cfg)
	state, _, err := loadRunState(cfg.StateFile)
	if err != nil || len(state.LastResults) != 1 || state.LastResults[0].Provider != second.Provider {
		t.Fatal("deleted provider remained in persisted results")
	}
}

func TestPageVersionAvailableBeforeAuthentication(t *testing.T) {
	recorder := httptest.NewRecorder()
	newServer(newWarmer(http.DefaultClient), "", "").routes().ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	body := recorder.Body.String()
	if recorder.Code != 200 || !strings.Contains(body, ">v"+serviceVersion+"</small>") || strings.Contains(body, "{{APP_VERSION}}") {
		t.Fatal("public page does not include the compiled service version")
	}
	if strings.Contains(body, "management-key-title") {
		t.Fatal("duplicate management key title remains")
	}
}
