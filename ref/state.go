package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

const runStateSchemaVersion = 1

type runState struct {
	SchemaVersion int          `json:"schema_version"`
	LastRun       time.Time    `json:"last_run"`
	LastSource    string       `json:"last_source,omitempty"`
	LastResults   []warmResult `json:"last_results"`
	Logs          []logEntry   `json:"logs"`
}

func defaultStateFile() string {
	dir, errConfig := os.UserConfigDir()
	if errConfig != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "CLIProxyAPI", pluginID, "history.json")
}

func loadRunState(path string) (runState, bool, error) {
	raw, errRead := os.ReadFile(path)
	if errors.Is(errRead, os.ErrNotExist) {
		return runState{}, false, nil
	}
	if errRead != nil {
		return runState{}, false, fmt.Errorf("read warm-up state: %w", errRead)
	}
	var state runState
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil {
		return runState{}, false, fmt.Errorf("decode warm-up state: %w", errDecode)
	}
	if state.SchemaVersion != runStateSchemaVersion {
		return runState{}, false, fmt.Errorf("unsupported warm-up state schema %d", state.SchemaVersion)
	}
	return state, true, nil
}

func writeRunState(path string, state runState) error {
	state.SchemaVersion = runStateSchemaVersion
	raw, errMarshal := json.Marshal(state)
	if errMarshal != nil {
		return fmt.Errorf("encode warm-up state: %w", errMarshal)
	}
	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create warm-up state directory: %w", errMkdir)
	}
	file, errCreate := os.CreateTemp(dir, ".codex-5h-quota-warmer-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create temporary warm-up state: %w", errCreate)
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
		return fmt.Errorf("write warm-up state: %w", errWrite)
	}
	if errRename := os.Rename(tempPath, path); errRename != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace warm-up state: %w", errRename)
	}
	return nil
}

func (w *warmer) persistLocked() error {
	if w.stateFile == "" {
		return nil
	}
	if w.stateLocked {
		return fmt.Errorf("warm-up state is read-only: %s", w.stateError)
	}
	results := make([]warmResult, len(w.lastResults))
	for index, result := range w.lastResults {
		if result.Error != "" {
			result.Error = "See server log for details"
		}
		results[index] = result
	}
	logs := make([]logEntry, len(w.logs))
	for index, entry := range w.logs {
		if len(entry.Fields) > 0 {
			fields := make(map[string]any, len(entry.Fields))
			for key, value := range entry.Fields {
				if key != "error" {
					fields[key] = value
				}
			}
			entry.Fields = fields
		}
		logs[index] = entry
	}
	errWrite := writeRunState(w.stateFile, runState{
		LastRun:     w.lastRun,
		LastSource:  w.lastSource,
		LastResults: results,
		Logs:        logs,
	})
	if errWrite != nil {
		w.stateError = errWrite.Error()
	} else {
		w.stateError = ""
	}
	return errWrite
}

func (w *warmer) logStateFailure(message string) {
	_, _ = callHost(pluginabi.MethodHostLog, map[string]any{
		"level":   "error",
		"message": "Codex warm-up state persistence failed",
		"fields":  map[string]any{"plugin_id": pluginID, "error": message},
	})
}

