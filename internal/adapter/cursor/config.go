package cursor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"mlink/internal/install"
)

var hookEvents = []string{"sessionStart", "beforeSubmitPrompt", "afterAgentResponse", "sessionEnd"}

type hookCommand struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func PlanHooks(existing []byte, binaryPath string) ([]byte, error) {
	if !filepath.IsAbs(binaryPath) || filepath.Base(binaryPath) != "mlink" {
		return nil, errors.New("Cursor Hook binary path must be an absolute MLink binary")
	}
	document := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(existing)) != 0 {
		if err := json.Unmarshal(existing, &document); err != nil {
			return nil, fmt.Errorf("parse Cursor hooks.json: %w", err)
		}
	}
	if raw := document["version"]; len(raw) != 0 && string(bytes.TrimSpace(raw)) != "1" {
		return nil, errors.New("unsupported Cursor hooks.json version")
	}
	hooks := make(map[string][]json.RawMessage)
	if raw := document["hooks"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, fmt.Errorf("parse Cursor Hook events: %w", err)
		}
	}
	for _, event := range hookEvents {
		hooks[event] = removeOwned(hooks[event], event)
		entry, err := json.Marshal(hookCommand{Command: binaryPath + " hook cursor " + event, Timeout: hookTimeout(event)})
		if err != nil {
			return nil, err
		}
		hooks[event] = append(hooks[event], entry)
	}
	version, _ := json.Marshal(1)
	document["version"] = version
	encodedHooks, err := json.Marshal(hooks)
	if err != nil {
		return nil, err
	}
	document["hooks"] = encodedHooks
	result, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(result, '\n'), nil
}

func RemoveOwnedHooks(existing []byte) ([]byte, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		return []byte("{\n  \"version\": 1,\n  \"hooks\": {}\n}\n"), nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(existing, &document); err != nil {
		return nil, fmt.Errorf("parse Cursor hooks.json: %w", err)
	}
	var hooks map[string][]json.RawMessage
	if raw := document["hooks"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, fmt.Errorf("parse Cursor Hook events: %w", err)
		}
	}
	if hooks == nil {
		hooks = make(map[string][]json.RawMessage)
	}
	for _, event := range hookEvents {
		hooks[event] = removeOwned(hooks[event], event)
		if len(hooks[event]) == 0 {
			delete(hooks, event)
		}
	}
	encodedHooks, err := json.Marshal(hooks)
	if err != nil {
		return nil, err
	}
	document["hooks"] = encodedHooks
	result, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(result, '\n'), nil
}

func DesiredHooksResource(existing []byte, targetPath, binaryPath string) (install.DesiredResource, error) {
	content, err := PlanHooks(existing, binaryPath)
	if err != nil {
		return install.DesiredResource{}, err
	}
	return install.DesiredResource{
		OwnerID: "dev.mlink.adapter.cursor", Target: targetPath, Content: content, Mode: 0o600,
		SemanticDiff: []install.SemanticDiff{{Path: "hooks", Before: "existing Cursor hooks preserved", After: "MLink local lifecycle hooks present; fail-open"}},
	}, nil
}

func removeOwned(entries []json.RawMessage, event string) []json.RawMessage {
	result := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		var hook hookCommand
		if json.Unmarshal(entry, &hook) == nil && strings.HasSuffix(hook.Command, " hook cursor "+event) && filepath.Base(strings.TrimSuffix(hook.Command, " hook cursor "+event)) == "mlink" {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func hookTimeout(event string) int {
	if event == "sessionEnd" {
		return 3
	}
	return 2
}
