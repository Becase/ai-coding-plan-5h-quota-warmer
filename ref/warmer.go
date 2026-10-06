package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginID       = "codex-5h-quota-warmer"
	pluginName     = "Codex Five-Hour Quota Warmer"
	pluginVersion  = "0.2.0"
	maxLogsDefault = 200
)

var globalWarmer = newWarmer()

type pluginConfig struct {
	Enabled       bool       `yaml:"enabled"`
	Model         string     `yaml:"model"`
	Prompt        string     `yaml:"prompt"`
	Timezone      string     `yaml:"timezone"`
	Schedules     []schedule `yaml:"schedules"`
	MaxLogEntries int        `yaml:"max_log_entries"`
	StateFile     string     `yaml:"state_file"`
}

type schedule struct {
	ID      string `yaml:"id" json:"id"`
	At      string `yaml:"at" json:"at"`
	Enabled *bool  `yaml:"enabled" json:"enabled,omitempty"`
}

type scheduledRun struct {
	ID   string    `json:"id"`
	At   string    `json:"at"`
	Time time.Time `json:"time"`
}

type warmResult struct {
	AuthID     string `json:"auth_id"`
	Label      string `json:"label,omitempty"`
	Model      string `json:"model"`
	StatusCode int    `json:"status_code,omitempty"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

type logEntry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Event   string         `json:"event"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

type warmerStatus struct {
	Plugin        string         `json:"plugin"`
	Version       string         `json:"version"`
	Running       bool           `json:"running"`
	Model         string         `json:"model"`
	Timezone      string         `json:"timezone"`
	Schedules     []scheduledRun `json:"schedules"`
	LastRun       *time.Time     `json:"last_run,omitempty"`
	LastSource    string         `json:"last_source,omitempty"`
	LastResults   []warmResult   `json:"last_results"`
	LogCount      int            `json:"log_count"`
	StateError    string         `json:"state_error,omitempty"`
	Configuration pluginConfig   `json:"configuration"`
}

type authListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type authSelection struct {
	Total       int
	Codex       int
	Disabled    int
	Unavailable int
	Inactive    int
	MissingID   int
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type managementRequest struct {
	Method string          `json:"Method"`
	Path   string          `json:"Path"`
	Body   json.RawMessage `json:"Body"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
}

type managementRegistration struct {
	Routes    []managementRoute    `json:"routes,omitempty"`
	Resources []managementResource `json:"resources,omitempty"`
}

type managementRoute struct {
	Method string `json:"Method"`
	Path   string `json:"Path"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type warmer struct {
	mu          sync.RWMutex
	config      pluginConfig
	stop        chan struct{}
	done        chan struct{}
	planned     scheduledRun
	running     bool
	lastRun     time.Time
	lastSource  string
	lastResults []warmResult
	logs        []logEntry
	stateFile   string
	stateError  string
	stateLocked bool
}

func newWarmer() *warmer {
	return &warmer{config: defaultPluginConfig()}
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Model:         "gpt-5.6-luna",
		Prompt:        "Reply with exactly: quota window activated",
		Timezone:      "Asia/Singapore",
		MaxLogEntries: maxLogsDefault,
		StateFile:     defaultStateFile(),
	}
}

func parsePluginConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
		return pluginConfig{}, fmt.Errorf("decode plugin configuration: %w", errUnmarshal)
	}
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Model == "" {
		return pluginConfig{}, fmt.Errorf("model is required")
	}
	cfg.Prompt = strings.TrimSpace(cfg.Prompt)
	if cfg.Prompt == "" {
		return pluginConfig{}, fmt.Errorf("prompt is required")
	}
	cfg.Timezone = strings.TrimSpace(cfg.Timezone)
	if cfg.Timezone == "" {
		cfg.Timezone = "Asia/Singapore"
	}
	if _, errLocation := time.LoadLocation(cfg.Timezone); errLocation != nil {
		return pluginConfig{}, fmt.Errorf("timezone %q is invalid: %w", cfg.Timezone, errLocation)
	}
	if cfg.MaxLogEntries <= 0 {
		cfg.MaxLogEntries = maxLogsDefault
	}
	if cfg.MaxLogEntries > 2000 {
		return pluginConfig{}, fmt.Errorf("max_log_entries must not exceed 2000")
	}
	cfg.StateFile = strings.TrimSpace(cfg.StateFile)
	if cfg.StateFile == "" {
		cfg.StateFile = defaultStateFile()
	}
	stateFile, errPath := filepath.Abs(cfg.StateFile)
	if errPath != nil {
		return pluginConfig{}, fmt.Errorf("resolve state_file: %w", errPath)
	}
	cfg.StateFile = stateFile
	seen := make(map[string]struct{}, len(cfg.Schedules))
	seenTimes := make(map[string]struct{}, len(cfg.Schedules))
	for index := range cfg.Schedules {
		item := &cfg.Schedules[index]
		item.ID = strings.TrimSpace(item.ID)
		item.At = strings.TrimSpace(item.At)
		if item.ID == "" {
			return pluginConfig{}, fmt.Errorf("schedules[%d].id is required", index)
		}
		if item.At == "" {
			return pluginConfig{}, fmt.Errorf("schedules[%d].at is required", index)
		}
		if _, errParse := time.Parse("15:04", item.At); errParse != nil {
			return pluginConfig{}, fmt.Errorf("schedules[%d].at must use HH:MM: %w", index, errParse)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return pluginConfig{}, fmt.Errorf("schedule id %q is duplicated", item.ID)
		}
		seen[item.ID] = struct{}{}
		if scheduleEnabled(*item) {
			if _, duplicate := seenTimes[item.At]; duplicate {
				return pluginConfig{}, fmt.Errorf("more than one enabled schedule uses %s", item.At)
			}
			seenTimes[item.At] = struct{}{}
		}
	}
	return cfg, nil
}

func scheduleEnabled(item schedule) bool {
	return item.Enabled == nil || *item.Enabled
}

func (w *warmer) Configure(cfg pluginConfig) {
	w.mu.RLock()
	unchanged := w.stateFile == cfg.StateFile && reflect.DeepEqual(w.config, cfg)
	w.mu.RUnlock()
	if unchanged {
		return
	}
	w.Stop()
	w.mu.Lock()
	w.config = cfg
	if w.stateFile != cfg.StateFile {
		state, loaded, errLoad := loadRunState(cfg.StateFile)
		w.stateFile = cfg.StateFile
		w.stateLocked = errLoad != nil
		w.stateError = ""
		if errLoad != nil {
			w.stateError = errLoad.Error()
		} else if loaded {
			w.lastRun = state.LastRun
			w.lastSource = state.LastSource
			w.lastResults = state.LastResults
			w.logs = state.Logs
			if len(w.logs) > cfg.MaxLogEntries {
				w.logs = append([]logEntry(nil), w.logs[len(w.logs)-cfg.MaxLogEntries:]...)
			}
		}
	}
	loadError := w.stateError
	if cfg.Enabled {
		w.stop = make(chan struct{})
		w.done = make(chan struct{})
	}
	stop, done := w.stop, w.done
	w.mu.Unlock()
	if loadError != "" {
		w.logStateFailure(loadError)
	}
	w.recordLog("info", "configuration.updated", "Warm-up configuration updated", map[string]any{
		"schedule_count": len(cfg.Schedules),
		"timezone":       cfg.Timezone,
		"model":          cfg.Model,
	})
	if !cfg.Enabled {
		return
	}
	go w.runScheduleLoop(cfg, stop, done)
}

func (w *warmer) Stop() {
	w.mu.Lock()
	stop := w.stop
	done := w.done
	w.stop = nil
	w.done = nil
	w.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	if done != nil {
		<-done
	}
}

func (w *warmer) runScheduleLoop(cfg pluginConfig, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		next, okNext := nextScheduledRun(cfg, time.Now())
		if !okNext {
			select {
			case <-stop:
				return
			case <-time.After(24 * time.Hour):
				continue
			}
		}
		wait := time.Until(next.Time)
		if wait < 0 {
			continue
		}
		w.mu.Lock()
		alreadyPlanned := w.planned.ID == next.ID && w.planned.Time.Equal(next.Time)
		w.planned = next
		w.mu.Unlock()
		if !alreadyPlanned {
			w.recordLog("info", "schedule.planned", "Next Codex warm-up scheduled", map[string]any{
				"schedule_id":  next.ID,
				"scheduled_at": next.Time.Format(time.RFC3339),
			})
		}
		timer := time.NewTimer(wait)
		select {
		case <-stop:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
			source := "schedule:" + next.ID
			w.recordLog("info", "schedule.triggered", "Codex warm-up schedule triggered", map[string]any{
				"source":       source,
				"scheduled_at": next.Time.Format(time.RFC3339),
			})
			if _, errRun := w.Run(source); errRun != nil {
				w.recordLog("error", "schedule.failed", "Scheduled Codex warm-up failed", map[string]any{
					"source": source,
					"error":  errRun.Error(),
				})
			}
		}
	}
}

func nextScheduledRun(cfg pluginConfig, now time.Time) (scheduledRun, bool) {
	location, errLocation := time.LoadLocation(cfg.Timezone)
	if errLocation != nil {
		return scheduledRun{}, false
	}
	current := now.In(location)
	var next scheduledRun
	found := false
	for _, item := range cfg.Schedules {
		if !scheduleEnabled(item) {
			continue
		}
		clock, errParse := time.Parse("15:04", item.At)
		if errParse != nil {
			continue
		}
		candidate := time.Date(current.Year(), current.Month(), current.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
		if !candidate.After(current) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		if !found || candidate.Before(next.Time) {
			next = scheduledRun{ID: item.ID, At: item.At, Time: candidate}
			found = true
		}
	}
	return next, found
}

func (w *warmer) Run(source string) ([]warmResult, error) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil, &hostCallError{code: "warmup_in_progress", message: "a warm-up run is already in progress", httpStatus: http.StatusConflict}
	}
	cfg := cloneConfig(w.config)
	if !cfg.Enabled {
		w.mu.Unlock()
		return nil, &hostCallError{code: "plugin_disabled", message: "the plugin is disabled", httpStatus: http.StatusConflict}
	}
	w.running = true
	w.lastSource = source
	w.mu.Unlock()
	w.recordLog("info", "warmup.started", "Codex credential warm-up started", map[string]any{"source": source, "model": cfg.Model})

	defer func() {
		w.mu.Lock()
		w.running = false
		w.lastRun = time.Now().UTC()
		errPersist := w.persistLocked()
		w.mu.Unlock()
		if errPersist != nil {
			w.logStateFailure(errPersist.Error())
		}
	}()

	auths, selection, errAuths := listCodexAuths()
	if errAuths != nil {
		w.recordLog("error", "warmup.auth_list_failed", "Unable to list Codex credentials", map[string]any{"source": source, "error": errAuths.Error()})
		w.recordLog("error", "warmup.finished", "Codex credential warm-up failed", map[string]any{"source": source, "credential_count": 0, "success_count": 0, "failure_count": 0, "error": errAuths.Error()})
		return nil, errAuths
	}
	w.recordLog("info", "warmup.selection", "Codex credentials selected for warm-up", map[string]any{
		"source":               source,
		"auth_total":           selection.Total,
		"codex_total":          selection.Codex,
		"selected_count":       len(auths),
		"skipped_disabled":     selection.Disabled,
		"reported_unavailable": selection.Unavailable,
		"reported_inactive":    selection.Inactive,
		"skipped_missing_id":   selection.MissingID,
	})
	if len(auths) == 0 {
		w.recordLog("warn", "warmup.no_credentials", "No eligible Codex credentials for warm-up", map[string]any{"source": source})
	}
	results := make([]warmResult, 0, len(auths))
	successCount := 0
	for _, auth := range auths {
		result := warmOneCredential(cfg, auth)
		results = append(results, result)
		level := "info"
		message := "Codex credential warm-up completed"
		if !result.Success {
			level = "warn"
			message = "Codex credential warm-up failed"
		} else {
			successCount++
		}
		w.recordLog(level, "warmup.credential", message, map[string]any{
			"source":      source,
			"auth_id":     result.AuthID,
			"label":       result.Label,
			"model":       result.Model,
			"status_code": result.StatusCode,
			"error":       result.Error,
		})
	}
	w.mu.Lock()
	w.lastResults = append([]warmResult(nil), results...)
	w.mu.Unlock()
	level := "info"
	if len(results) == 0 || successCount != len(results) {
		level = "warn"
	}
	w.recordLog(level, "warmup.finished", "Codex credential warm-up finished", map[string]any{
		"source":           source,
		"credential_count": len(results),
		"success_count":    successCount,
		"failure_count":    len(results) - successCount,
	})
	return results, nil
}

func listCodexAuths() ([]pluginapi.HostAuthFileEntry, authSelection, error) {
	raw, errCall := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, authSelection{}, errCall
	}
	var response authListResponse
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		return nil, authSelection{}, fmt.Errorf("decode host.auth.list response: %w", errUnmarshal)
	}
	selection := authSelection{Total: len(response.Files)}
	auths := make([]pluginapi.HostAuthFileEntry, 0, len(response.Files))
	for _, auth := range response.Files {
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		selection.Codex++
		switch {
		case auth.Disabled:
			selection.Disabled++
			continue
		case strings.TrimSpace(auth.ID) == "":
			selection.MissingID++
			continue
		}
		if auth.Unavailable {
			selection.Unavailable++
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Status), "active") {
			selection.Inactive++
		}
		auths = append(auths, auth)
	}
	sort.Slice(auths, func(i, j int) bool {
		return strings.ToLower(auths[i].ID) < strings.ToLower(auths[j].ID)
	})
	return auths, selection, nil
}

func warmOneCredential(cfg pluginConfig, auth pluginapi.HostAuthFileEntry) warmResult {
	result := warmResult{AuthID: auth.ID, Label: auth.Label, Model: cfg.Model}
	body, errBody := json.Marshal(map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{{
			"role":    "user",
			"content": cfg.Prompt,
		}},
		"stream": false,
	})
	if errBody != nil {
		result.Error = errBody.Error()
		return result
	}
	raw, errCall := callHost(pluginabi.MethodHostModelExecute, pluginapi.HostModelExecutionRequest{
		EntryProtocol:  "openai",
		ExitProtocol:   "openai",
		Model:          cfg.Model,
		Stream:         false,
		Body:           body,
		Headers:        http.Header{"Content-Type": []string{"application/json"}},
		ForcedProvider: "codex",
		AuthID:         auth.ID,
	})
	if errCall != nil {
		result.Error = errCall.Error()
		var statusCoder interface{ StatusCode() int }
		if errors.As(errCall, &statusCoder) && statusCoder != nil {
			result.StatusCode = statusCoder.StatusCode()
		}
		return result
	}
	var response pluginapi.HostModelExecutionResponse
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		result.Error = fmt.Sprintf("decode host.model.execute response: %v", errUnmarshal)
		return result
	}
	result.StatusCode = response.StatusCode
	result.Success = response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
	if !result.Success {
		result.Error = fmt.Sprintf("model execution returned HTTP %d", response.StatusCode)
	}
	return result
}

func (w *warmer) Status() warmerStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	cfg := cloneConfig(w.config)
	next := nextRuns(cfg, time.Now())
	var lastRun *time.Time
	if !w.lastRun.IsZero() {
		value := w.lastRun
		lastRun = &value
	}
	return warmerStatus{
		Plugin:        pluginID,
		Version:       pluginVersion,
		Running:       w.running,
		Model:         cfg.Model,
		Timezone:      cfg.Timezone,
		Schedules:     next,
		LastRun:       lastRun,
		LastSource:    w.lastSource,
		LastResults:   append([]warmResult(nil), w.lastResults...),
		LogCount:      len(w.logs),
		StateError:    w.stateError,
		Configuration: cfg,
	}
}

func nextRuns(cfg pluginConfig, now time.Time) []scheduledRun {
	location, errLocation := time.LoadLocation(cfg.Timezone)
	if errLocation != nil {
		return []scheduledRun{}
	}
	current := now.In(location)
	runs := make([]scheduledRun, 0, len(cfg.Schedules))
	for _, item := range cfg.Schedules {
		if !scheduleEnabled(item) {
			continue
		}
		clock, errParse := time.Parse("15:04", item.At)
		if errParse != nil {
			continue
		}
		candidate := time.Date(current.Year(), current.Month(), current.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
		if !candidate.After(current) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		runs = append(runs, scheduledRun{ID: item.ID, At: item.At, Time: candidate})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Time.Before(runs[j].Time) })
	return runs
}

func (w *warmer) Logs() []logEntry {
	w.mu.RLock()
	defer w.mu.RUnlock()
	logs := make([]logEntry, len(w.logs))
	copy(logs, w.logs)
	return logs
}

func (w *warmer) recordLog(level, event, message string, fields map[string]any) {
	w.mu.Lock()
	w.appendLogLocked(level, event, message, fields)
	errPersist := w.persistLocked()
	w.mu.Unlock()
	if errPersist != nil {
		w.logStateFailure(errPersist.Error())
	}
	hostFields := sanitizeLogFields(fields)
	if hostFields == nil {
		hostFields = make(map[string]any)
	}
	hostFields["plugin_id"] = pluginID
	_, _ = callHost(pluginabi.MethodHostLog, map[string]any{
		"level":   level,
		"message": hostLogMessage(event, message, fields),
		"fields":  hostFields,
	})
}

func hostLogMessage(event, message string, fields map[string]any) string {
	switch event {
	case "schedule.planned":
		return fmt.Sprintf("%s schedule_id=%q scheduled_at=%v", message, fields["schedule_id"], fields["scheduled_at"])
	case "schedule.triggered":
		return fmt.Sprintf("%s source=%q scheduled_at=%v", message, fields["source"], fields["scheduled_at"])
	case "warmup.selection":
		return fmt.Sprintf("%s source=%q auth_total=%v codex_total=%v selected=%v skipped_disabled=%v reported_unavailable=%v reported_inactive=%v skipped_missing_id=%v", message, fields["source"], fields["auth_total"], fields["codex_total"], fields["selected_count"], fields["skipped_disabled"], fields["reported_unavailable"], fields["reported_inactive"], fields["skipped_missing_id"])
	case "warmup.finished":
		return fmt.Sprintf("%s source=%q credential_count=%v success_count=%v failure_count=%v", message, fields["source"], fields["credential_count"], fields["success_count"], fields["failure_count"])
	case "warmup.started", "warmup.no_credentials", "warmup.auth_list_failed", "warmup.credential", "schedule.failed":
		return fmt.Sprintf("%s source=%q", message, fields["source"])
	default:
		return message
	}
}

func (w *warmer) appendLogLocked(level, event, message string, fields map[string]any) {
	entry := logEntry{
		Time:    time.Now().UTC(),
		Level:   level,
		Event:   event,
		Message: message,
		Fields:  sanitizeLogFields(fields),
	}
	w.logs = append(w.logs, entry)
	limit := w.config.MaxLogEntries
	if limit <= 0 {
		limit = maxLogsDefault
	}
	if len(w.logs) > limit {
		w.logs = append([]logEntry(nil), w.logs[len(w.logs)-limit:]...)
	}
}

func sanitizeLogFields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	copyFields := make(map[string]any, len(fields))
	for key, value := range fields {
		if strings.Contains(strings.ToLower(key), "token") || strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "authorization") {
			continue
		}
		copyFields[key] = value
	}
	return copyFields
}

func cloneConfig(cfg pluginConfig) pluginConfig {
	copyCfg := cfg
	copyCfg.Schedules = append([]schedule(nil), cfg.Schedules...)
	return copyCfg
}

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var request lifecycleRequest
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return nil, fmt.Errorf("decode plugin lifecycle request: %w", errUnmarshal)
		}
		cfg, errConfig := parsePluginConfig(request.ConfigYAML)
		if errConfig != nil {
			return nil, errConfig
		}
		globalWarmer.Configure(cfg)
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		globalWarmer.Stop()
		return okEnvelope(map[string]any{})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{
			Routes: []managementRoute{
				{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/status"},
				{Method: http.MethodPost, Path: "/plugins/" + pluginID + "/run"},
				{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/logs"},
			},
			Resources: []managementResource{{
				Path:        "/status",
				Menu:        "Codex Quota Warmer",
				Description: "Schedules and runs Codex credential warm-up requests.",
			}},
		})
	case pluginabi.MethodManagementHandle:
		return handleManagement(raw)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, 0), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "Windyskr",
			GitHubRepository: "https://github.com/Windyskr/codex-5h-quota-warmer",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "model", Type: pluginapi.ConfigFieldTypeString, Description: "Codex model used for each warm-up request."},
				{Name: "prompt", Type: pluginapi.ConfigFieldTypeString, Description: "Message sent to every enabled Codex credential."},
				{Name: "timezone", Type: pluginapi.ConfigFieldTypeString, Description: "IANA timezone used by the daily schedules."},
				{Name: "schedules", Type: pluginapi.ConfigFieldTypeArray, Description: "Daily schedule entries with id, at (HH:MM), and enabled."},
				{Name: "max_log_entries", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum number of retained warm-up log entries."},
				{Name: "state_file", Type: pluginapi.ConfigFieldTypeString, Description: "Path to the persistent warm-up history JSON file."},
			},
		},
		Capabilities: registrationCapabilities{ManagementAPI: true},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var request managementRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
	}
	path := strings.TrimRight(strings.TrimSpace(request.Path), "/")
	switch {
	case strings.HasSuffix(path, "/status"):
		if strings.HasPrefix(path, "/v0/resource/") {
			return okEnvelope(htmlResponse(http.StatusOK, []byte(statusPageHTML)))
		}
		return okEnvelope(jsonResponse(http.StatusOK, globalWarmer.Status()))
	case strings.HasSuffix(path, "/logs"):
		return okEnvelope(jsonResponse(http.StatusOK, map[string]any{"logs": globalWarmer.Logs()}))
	case strings.HasSuffix(path, "/run"):
		results, errRun := globalWarmer.Run("manual")
		if errRun != nil {
			status := http.StatusInternalServerError
			var statusCoder interface{ StatusCode() int }
			if errors.As(errRun, &statusCoder) && statusCoder != nil && statusCoder.StatusCode() > 0 {
				status = statusCoder.StatusCode()
			}
			return okEnvelope(jsonResponse(status, map[string]any{"error": errRun.Error()}))
		}
		return okEnvelope(jsonResponse(http.StatusOK, map[string]any{"results": results, "status": globalWarmer.Status()}))
	default:
		return okEnvelope(jsonResponse(http.StatusNotFound, map[string]any{"error": "route_not_found"}))
	}
}

func jsonResponse(status int, value any) managementResponse {
	body, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		body = []byte(`{"error":"response_encode_failed"}`)
		status = http.StatusInternalServerError
	}
	return managementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func htmlResponse(status int, body []byte) managementResponse {
	return managementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       body,
	}
}

