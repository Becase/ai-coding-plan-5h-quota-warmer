package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"gopkg.in/yaml.v3"
)

const pluginID = serviceName

type pluginApp struct {
	mu     sync.RWMutex
	host   cpaHost
	server *server
	dir    string
	closed bool
}

type managementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

type managementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

func (a *pluginApp) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	if a.server != nil {
		// Handlers hold the read lock through manual runs. Stop also waits for
		// scheduled runs, so no callback remains live when CPA unloads the DLL.
		a.server.warmer.Stop()
	}
}

func (a *pluginApp) configure(raw []byte) error {
	var request struct {
		SchemaVersion uint32 `json:"schema_version"`
		ConfigYAML    []byte `json:"config_yaml"`
	}
	if json.Unmarshal(raw, &request) != nil || request.SchemaVersion < 6 {
		return errors.New("CPA RPC schema 6 or newer required")
	}
	options := struct {
		StateDir string `yaml:"state_dir"`
	}{StateDir: "./plugins/state/" + pluginID}
	if err := yaml.Unmarshal(request.ConfigYAML, &options); err != nil {
		return errors.New("invalid plugin configuration")
	}
	if strings.TrimSpace(options.StateDir) == "" {
		return errors.New("state_dir is required")
	}
	dir, err := filepath.Abs(options.StateDir)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("plugin has stopped; reload required")
	}
	if a.server != nil {
		if dir != a.dir {
			return errors.New("state_dir cannot change while loaded; restart CPA")
		}
		return nil
	}
	configPath := filepath.Join(dir, "config.yaml")
	cfg := defaultConfig()
	cfg.Accounts = []Account{}
	rawConfig, err := os.ReadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(rawConfig) > 0 {
		if err = yaml.Unmarshal(rawConfig, &cfg); err != nil {
			return errors.New("invalid saved plugin configuration")
		}
	}
	cfg.StateFile = filepath.Join(dir, "state.json")
	applyEnvAPIKeys(&cfg)
	if err = validateConfig(&cfg, true); err != nil {
		return err
	}
	w := newWarmer(&http.Client{Timeout: 60 * time.Second})
	w.host = a.host
	w.Configure(cfg)
	a.server = newServer(w, "", configPath)
	a.server.host = a.host
	a.dir = dir
	return nil
}

func (a *pluginApp) Call(method string, raw []byte) []byte {
	var result interface{}
	var err error
	switch method {
	case "plugin.register", "plugin.reconfigure":
		err = a.configure(raw)
		result = map[string]interface{}{
			"schema_version": 6,
			"metadata": map[string]interface{}{
				"Name": "AI Coding Plan 配额预热器", "Version": serviceVersion,
				"Author": "Becase", "GitHubRepository": "https://github.com/Becase/ai-coding-plan-5h-quota-warmer",
				"ConfigFields": []map[string]string{{"Name": "state_dir", "Type": "string", "Description": "预热配置和运行记录持久化目录"}},
			},
			"capabilities": map[string]bool{"management_api": true},
		}
	case "plugin.quiesce", "plugin.shutdown":
		a.Close()
		result = struct{}{}
	case "management.register":
		routes := []map[string]string{}
		for _, route := range []struct{ method, path string }{
			{"GET", "meta"}, {"GET", "status"}, {"GET", "config"}, {"PUT", "config"},
			{"POST", "run"}, {"GET", "logs"}, {"GET", "accounts"},
		} {
			routes = append(routes, map[string]string{"Method": route.method, "Path": "/plugins/" + pluginID + "/api/" + route.path})
		}
		result = map[string]interface{}{"routes": routes, "resources": []map[string]string{
			{"Path": "/status", "Menu": "配额预热", "Description": "CPA 账号和手动 API Key 定时预热"},
			{"Path": "/third-party-notices.txt"},
		}}
	case "management.handle":
		var request managementRequest
		if len(raw) > 2*1024*1024 || json.Unmarshal(raw, &request) != nil {
			err = errors.New("invalid management request")
		} else {
			result = a.handle(request)
		}
	default:
		err = errors.New("unsupported plugin method")
	}
	if err != nil {
		payload, _ := json.Marshal(map[string]interface{}{"ok": false, "error": map[string]string{"code": "plugin_error", "message": err.Error()}})
		return payload
	}
	payload, _ := json.Marshal(map[string]interface{}{"ok": true, "result": result})
	return payload
}

func (a *pluginApp) handle(request managementRequest) managementResponse {
	recorder := httptest.NewRecorder()
	resource := "/v0/resource/plugins/" + pluginID
	if request.Method == "GET" && (request.Path == resource+"/status" || request.Path == resource+"/third-party-notices.txt") {
		recorder.Header().Set("Cache-Control", "no-store")
		recorder.Header().Set("X-Content-Type-Options", "nosniff")
		if request.Path == resource+"/status" {
			recorder.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = recorder.Write(indexHTML)
		} else {
			recorder.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = recorder.Write(thirdPartyNotices)
		}
	} else {
		a.mu.RLock()
		defer a.mu.RUnlock()
		prefix := "/v0/management/plugins/" + pluginID
		if !strings.HasPrefix(request.Path, prefix+"/api/") {
			writeErr(recorder, 404, "not_found", "接口不存在")
		} else if a.closed || a.server == nil {
			writeErr(recorder, 503, "unavailable", "插件已停止或尚未注册")
		} else {
			path := strings.TrimPrefix(request.Path, prefix)
			r := httptest.NewRequest(request.Method, path, bytes.NewReader(request.Body))
			// These routes are invoked only by CPA's authenticated management RPC.
			handlers := map[string]http.HandlerFunc{
				"/api/meta": a.server.handleMeta, "/api/status": a.server.handleStatus,
				"/api/config": a.server.handleConfig, "/api/run": a.server.handleRun,
				"/api/logs": a.server.handleLogs, "/api/accounts": a.server.handleAccounts,
			}
			if handler, ok := handlers[path]; ok {
				handler(recorder, r)
			} else {
				writeErr(recorder, 404, "not_found", "接口不存在")
			}
		}
	}
	response := recorder.Result()
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	response.Header.Set("Cache-Control", "no-store")
	return managementResponse{response.StatusCode, response.Header, body}
}
