package main

import (
	"fmt"
	"log"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	serviceName    = "coding-plan-5h-quota-warmer"
	serviceVersion = "0.1.0"
)

// httpError 带建议 HTTP 状态码的错误，供管理 API 映射响应。
type httpError struct {
	code    string
	message string
	status  int
}

func (e *httpError) Error() string { return e.message }

type scheduledRun struct {
	ID   string    `json:"id"`
	At   string    `json:"at"`
	Time time.Time `json:"time"`
}

type logEntry struct {
	Time    time.Time          `json:"time"`
	Level   string             `json:"level"`
	Event   string             `json:"event"`
	Message string             `json:"message"`
	Fields  map[string]interface{} `json:"fields,omitempty"`
}

// accountNextRuns 单个账号的下一次预热时间列表。
type accountNextRuns struct {
	Provider string         `json:"provider"`
	Runs     []scheduledRun `json:"runs"`
}

// Status 管理页面展示的完整运行状态。
type Status struct {
	Version      string            `json:"version"`
	Running      bool              `json:"running"`
	AccountCount int               `json:"account_count"`
	Timezone     string            `json:"timezone"`
	Schedules    []accountNextRuns `json:"schedules"`
	LastRun      *time.Time        `json:"last_run,omitempty"`
	LastSource   string            `json:"last_source,omitempty"`
	LastResults []warmResult      `json:"last_results"`
	LogCount     int               `json:"log_count"`
	StateError  string            `json:"state_error,omitempty"`
	Config       Config            `json:"config"`
}

type warmer struct {
	mu             sync.RWMutex
	config         Config
	stop           chan struct{}
	done           chan struct{}
	planned        scheduledRun
	plannedAccount int
	running        bool
	lastRun        time.Time
	lastSource     string
	lastResults    []warmResult
	logs           []logEntry
	stateFile      string
	stateError     string
	stateLocked    bool
	client         *http.Client
}

func newWarmer(client *http.Client) *warmer {
	return &warmer{config: defaultConfig(), client: client}
}

// Configure 应用新配置；配置变化时停止旧调度循环并重新武装。
// 这是热重载入口：管理页面保存设置后无需重启服务。
func (w *warmer) Configure(cfg Config) {
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
	w.stop = make(chan struct{})
	w.done = make(chan struct{})
	stop, done := w.stop, w.done
	w.mu.Unlock()
	if loadError != "" {
		w.logStateFailure(loadError)
	}
	w.recordLog("info", "configuration.updated", "预热配置已更新", map[string]interface{}{
		"schedule_count": len(cfg.Schedules),
		"account_count":  len(cfg.Accounts),
		"timezone":       cfg.Timezone,
	})
	go w.runScheduleLoop(cfg, stop, done)
}

// Stop 停止调度循环并等待其退出。
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

// runScheduleLoop 等待下一次计划时间并触发预热。启动时不执行预热。
// 每个账号使用自己的生效日程（专属日程优先，否则全局兜底），到点只预热对应账号。
func (w *warmer) runScheduleLoop(cfg Config, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		accountIndex, next, okNext := nextAccountScheduledRun(cfg, time.Now())
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
		alreadyPlanned := w.plannedAccount == accountIndex && w.planned.ID == next.ID && w.planned.Time.Equal(next.Time)
		w.plannedAccount = accountIndex
		w.planned = next
		w.mu.Unlock()
		if !alreadyPlanned {
			w.recordLog("info", "schedule.planned", "已安排下一次预热", map[string]interface{}{
				"schedule_id":  next.ID,
				"provider":     cfg.Accounts[accountIndex].Provider,
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
			w.recordLog("info", "schedule.triggered", "预热计划已触发", map[string]interface{}{
				"source":       source,
				"provider":     cfg.Accounts[accountIndex].Provider,
				"scheduled_at": next.Time.Format(time.RFC3339),
			})
			if _, errRun := w.Run(source, cfg.Accounts[accountIndex:accountIndex+1]); errRun != nil {
				w.recordLog("error", "schedule.failed", "计划预热执行失败", map[string]interface{}{
					"source": source,
					"error":  errRun.Error(),
				})
			}
		}
	}
}

// nextAccountScheduledRun 计算所有账号中最早的下一次预热。
func nextAccountScheduledRun(cfg Config, now time.Time) (int, scheduledRun, bool) {
	location, errLocation := time.LoadLocation(cfg.Timezone)
	if errLocation != nil {
		return 0, scheduledRun{}, false
	}
	current := now.In(location)
	bestIndex := -1
	var next scheduledRun
	for accountIndex, acct := range cfg.Accounts {
		for _, item := range effectiveSchedules(cfg, acct) {
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
			if bestIndex < 0 || candidate.Before(next.Time) {
				next = scheduledRun{ID: item.ID, At: item.At, Time: candidate}
				bestIndex = accountIndex
			}
		}
	}
	return bestIndex, next, bestIndex >= 0
}

// nextRuns 每个账号的下一次预热时间列表（用于状态展示）。
func nextRuns(cfg Config, now time.Time) []accountNextRuns {
	location, errLocation := time.LoadLocation(cfg.Timezone)
	if errLocation != nil {
		return []accountNextRuns{}
	}
	current := now.In(location)
	groups := make([]accountNextRuns, 0, len(cfg.Accounts))
	for _, acct := range cfg.Accounts {
		runs := make([]scheduledRun, 0, len(effectiveSchedules(cfg, acct)))
		for _, item := range effectiveSchedules(cfg, acct) {
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
		groups = append(groups, accountNextRuns{Provider: acct.Provider, Runs: runs})
	}
	return groups
}

// Run 执行一次预热。accounts 为空时预热全部账号；
// 调度循环传入单个账号，管理页面手动触发传 nil。运行中再次触发返回 409。
func (w *warmer) Run(source string, accounts []Account) ([]warmResult, error) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil, &httpError{code: "warmup_in_progress", message: "一次预热正在执行中", status: http.StatusConflict}
	}
	cfg := cloneConfig(w.config)
	w.running = true
	w.lastSource = source
	w.mu.Unlock()
	if len(accounts) == 0 {
		accounts = cfg.Accounts
	}
	w.recordLog("info", "warmup.started", "预热开始", map[string]interface{}{"source": source, "account_count": len(accounts)})

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

	results := make([]warmResult, 0, len(accounts))
	successCount := 0
	for _, acct := range accounts {
		result := executeWarm(w.client, acct)
		results = append(results, result)
		level := "info"
		message := "预热请求成功"
		if result.Success {
			successCount++
		} else {
			level = "warn"
			message = "预热请求失败"
		}
		w.recordLog(level, "warmup.credential", message, map[string]interface{}{
			"source":      source,
			"provider":    result.Provider,
			"model":       result.Model,
			"status_code": result.StatusCode,
			"latency_ms":  result.LatencyMS,
			"error":       result.Error,
		})
	}
	w.mu.Lock()
	w.lastResults = results
	w.mu.Unlock()
	finishLevel := "info"
	if successCount != len(results) {
		finishLevel = "warn"
	}
	w.recordLog(finishLevel, "warmup.finished", "预热结束", map[string]interface{}{
		"source":         source,
		"account_count":  len(results),
		"success_count":  successCount,
		"failure_count":  len(results) - successCount,
	})
	return results, nil
}

func (w *warmer) Status() Status {
	w.mu.RLock()
	defer w.mu.RUnlock()
	cfg := cloneConfig(w.config)
	next := nextRuns(cfg, time.Now())
	var lastRun *time.Time
	if !w.lastRun.IsZero() {
		value := w.lastRun
		lastRun = &value
	}
	return Status{
		Version:      serviceVersion,
		Running:      w.running,
		AccountCount: len(cfg.Accounts),
		Timezone:     cfg.Timezone,
		Schedules:    next,
		LastRun:      lastRun,
		LastSource:   w.lastSource,
		LastResults:  append([]warmResult(nil), w.lastResults...),
		LogCount:     len(w.logs),
		StateError:   w.stateError,
		Config:       cfg,
	}
}

// RenameProviders 更新最近结果中的 provider 名称，保持界面与配置一致（改名场景）。
func (w *warmer) RenameProviders(renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := false
	for index := range w.lastResults {
		if newName, ok := renames[w.lastResults[index].Provider]; ok {
			w.lastResults[index].Provider = newName
			changed = true
		}
	}
	if changed {
		if errPersist := w.persistLocked(); errPersist != nil {
			w.logStateFailure(errPersist.Error())
		}
	}
}

func (w *warmer) Logs() []logEntry {
	w.mu.RLock()
	defer w.mu.RUnlock()
	logs := make([]logEntry, len(w.logs))
	copy(logs, w.logs)
	return logs
}

// recordLog 追加一条有界日志并持久化，同时输出到服务日志。
func (w *warmer) recordLog(level, event, message string, fields map[string]interface{}) {
	w.mu.Lock()
	w.appendLogLocked(level, event, message, fields)
	errPersist := w.persistLocked()
	w.mu.Unlock()
	if errPersist != nil {
		w.logStateFailure(errPersist.Error())
	}
	log.Printf("[%s] %s %s", level, event, hostLogMessage(event, message, fields))
}

func hostLogMessage(event, message string, fields map[string]interface{}) string {
	switch event {
	case "schedule.planned":
		return fmt.Sprintf("%s schedule_id=%q scheduled_at=%v", message, fields["schedule_id"], fields["scheduled_at"])
	case "schedule.triggered":
		return fmt.Sprintf("%s source=%q scheduled_at=%v", message, fields["source"], fields["scheduled_at"])
	case "warmup.finished":
		return fmt.Sprintf("%s source=%q success_count=%v failure_count=%v", message, fields["source"], fields["success_count"], fields["failure_count"])
	case "warmup.started", "warmup.no_credentials", "warmup.credential", "schedule.failed":
		return fmt.Sprintf("%s source=%q", message, fields["source"])
	default:
		return message
	}
}

func (w *warmer) appendLogLocked(level, event, message string, fields map[string]interface{}) {
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

// sanitizeLogFields 剔除可能包含密钥/令牌的字段。
func sanitizeLogFields(fields map[string]interface{}) map[string]interface{} {
	if len(fields) == 0 {
		return nil
	}
	copyFields := make(map[string]interface{}, len(fields))
	for key, value := range fields {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
			strings.Contains(lower, "authorization") || strings.Contains(lower, "api_key") || lower == "key" {
			continue
		}
		copyFields[key] = value
	}
	return copyFields
}
