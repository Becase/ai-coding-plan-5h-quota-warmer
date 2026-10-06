package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const maxBodyBytes = 64 << 10 // 管理请求请求体上限 64KB

// server 承载管理页面与受密钥保护的管理 API。
type server struct {
	warmer     *warmer
	keyHash    [sha256.Size]byte // 有效管理密钥的 SHA-256；全零表示管理 API 停用
	hasKey     bool
	configPath string
}

func newServer(w *warmer, managementKey, configPath string) *server {
	s := &server{warmer: w, configPath: configPath}
	if managementKey != "" {
		s.keyHash = sha256.Sum256([]byte(managementKey))
		s.hasKey = true
	}
	return s
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/api/meta", s.handleMeta)
	mux.HandleFunc("/api/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/api/config", s.requireAuth(s.handleConfig))
	mux.HandleFunc("/api/run", s.requireAuth(s.handleRun))
	mux.HandleFunc("/api/logs", s.requireAuth(s.handleLogs))
	return mux
}

// requireAuth 校验 Authorization: Bearer <密钥>；未配置密钥时管理 API 一律 404。
func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hasKey {
			writeErr(w, http.StatusNotFound, "management_disabled", "服务端未配置管理密钥，管理接口已停用")
			return
		}
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) || strings.TrimSpace(header[len(prefix):]) == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "缺少管理密钥")
			return
		}
		tokenHash := sha256.Sum256([]byte(header[len(prefix):]))
		if subtle.ConstantTimeCompare(tokenHash[:], s.keyHash[:]) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "管理密钥不正确")
			return
		}
		next(w, r)
	}
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexHTML)
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

func (s *server) handleMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":            serviceVersion,
		"management_enabled": s.hasKey,
	})
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	status := s.warmer.Status()
	status.Config = maskedConfig(status.Config)
	writeJSON(w, http.StatusOK, status)
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, maskedConfig(s.warmer.currentConfig()))
	case http.MethodPut:
		s.putConfig(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "支持 GET 与 PUT")
	}
}

// putConfig 处理设置保存：合并 write-only 的 api_key，校验后热重载并写回 YAML。
func (s *server) putConfig(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var incoming Config
	if errDecode := json.NewDecoder(r.Body).Decode(&incoming); errDecode != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", "请求体不是有效的 JSON 配置: "+errDecode.Error())
		return
	}
	current := s.warmer.currentConfig()
	// management_key 不参与 JSON 编解码（json:"-"），保存时沿用现有值，
	// 避免写回 YAML 时把用户在配置文件中设置的密钥抹掉。
	incoming.ManagementKey = current.ManagementKey
	// api_key 为 write-only：留空或回传掩码串表示沿用现有密钥。
	// 按 provider 名匹配旧账号；改名时退化为按下标匹配。
	currentByProvider := make(map[string]Account, len(current.Accounts))
	for _, acct := range current.Accounts {
		currentByProvider[acct.Provider] = acct
	}
	for index := range incoming.Accounts {
		incomingKey := strings.TrimSpace(incoming.Accounts[index].APIKey)
		if incomingKey != "" && incomingKey != maskedAPIKey(currentByProvider[incoming.Accounts[index].Provider].APIKey) {
			continue
		}
		reference, found := currentByProvider[incoming.Accounts[index].Provider]
		if !found && index < len(current.Accounts) {
			reference = current.Accounts[index]
			found = true
		}
		if found {
			incoming.Accounts[index].APIKey = reference.APIKey
		}
	}
	// 改名时同步"最近结果"里的旧名称，避免界面出现新旧 provider 混排
	if len(incoming.Accounts) == len(current.Accounts) {
		renames := make(map[string]string)
		for index := range incoming.Accounts {
			oldName, newName := current.Accounts[index].Provider, incoming.Accounts[index].Provider
			if oldName != newName && oldName != "" && newName != "" {
				renames[oldName] = newName
			}
		}
		s.warmer.RenameProviders(renames)
	}
	if errValidate := Validate(&incoming); errValidate != nil {
		writeErr(w, http.StatusBadRequest, "invalid_config", errValidate.Error())
		return
	}
	s.warmer.Configure(incoming)
	response := map[string]interface{}{"config": maskedConfig(incoming)}
	if errSave := SaveConfigFile(s.configPath, incoming); errSave != nil {
		// 内存配置已生效；落盘失败只提示，不回滚
		response["config_persist_error"] = errSave.Error()
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	results, errRun := s.warmer.Run("manual", nil)
	if errRun != nil {
		status := http.StatusInternalServerError
		var httpErr *httpError
		if errors.As(errRun, &httpErr) && httpErr.status > 0 {
			status = httpErr.status
		}
		writeErr(w, status, "run_failed", errRun.Error())
		return
	}
	status := s.warmer.Status()
	status.Config = maskedConfig(status.Config)
	writeJSON(w, http.StatusOK, map[string]interface{}{"results": results, "status": status})
}

func (s *server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"logs": s.warmer.Logs()})
}

// currentConfig 返回当前生效配置的副本（含明文密钥，仅在服务内部使用）。
func (w *warmer) currentConfig() Config {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return cloneConfig(w.config)
}

// maskedConfig 返回脱敏配置副本：所有账号的 api_key 替换为掩码。
func maskedConfig(cfg Config) Config {
	masked := cloneConfig(cfg)
	for index := range masked.Accounts {
		masked.Accounts[index].APIKey = maskedAPIKey(masked.Accounts[index].APIKey)
	}
	return masked
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	body, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		body = []byte(`{"error":"response_encode_failed"}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]interface{}{"error": code, "message": message})
}
