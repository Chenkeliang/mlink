package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"mlink/internal/install"
)

var codexEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"}

type hookCommand struct {
	Type                   string `json:"type"`
	Command                string `json:"command"`
	Timeout                int    `json:"timeout"`
	AdditionalContextLimit int    `json:"additionalContextLimit,omitempty"`
	StatusMessage          string `json:"statusMessage,omitempty"`
}

type hookMatcher struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

func PlanHooks(existing []byte, binaryPath string) ([]byte, error) {
	if !filepath.IsAbs(binaryPath) {
		return nil, errors.New("Codex Hook binary path must be absolute")
	}
	document, hooks, err := parseHookDocument(existing)
	if err != nil {
		return nil, err
	}
	for _, event := range codexEvents {
		hooks[event] = removeOwned(hooks[event], event)
		matcher := hookMatcher{Hooks: []hookCommand{{
			Type:    "command",
			Command: binaryPath + " hook codex " + event,
			Timeout: hookTimeout(event),
		}}}
		switch event {
		case "SessionStart":
			matcher.Matcher = "startup|resume|clear|compact"
			matcher.Hooks[0].AdditionalContextLimit = 1200
			matcher.Hooks[0].StatusMessage = "Loading MLink memory"
		case "UserPromptSubmit":
			matcher.Hooks[0].AdditionalContextLimit = 1200
			matcher.Hooks[0].StatusMessage = "Recalling MLink memory"
		}
		encoded, err := json.Marshal(matcher)
		if err != nil {
			return nil, err
		}
		hooks[event] = append(hooks[event], encoded)
	}
	return encodeHookDocument(document, hooks)
}

func RemoveOwnedHooks(existing []byte) ([]byte, error) {
	document, hooks, err := parseHookDocument(existing)
	if err != nil {
		return nil, err
	}
	for _, event := range codexEvents {
		hooks[event] = removeOwned(hooks[event], event)
		if len(hooks[event]) == 0 {
			delete(hooks, event)
		}
	}
	return encodeHookDocument(document, hooks)
}

func DesiredHooksResource(existing []byte, targetPath, binaryPath string) (install.DesiredResource, error) {
	content, err := PlanHooks(existing, binaryPath)
	if err != nil {
		return install.DesiredResource{}, err
	}
	return install.DesiredResource{
		OwnerID: "dev.mlink.adapter.codex",
		Target:  targetPath,
		Content: content,
		Mode:    0o600,
		SemanticDiff: []install.SemanticDiff{{
			Path: "hooks", Before: "existing Hooks preserved", After: "MLink lifecycle Hooks present",
		}},
	}, nil
}

func parseHookDocument(existing []byte) (map[string]json.RawMessage, map[string][]json.RawMessage, error) {
	document := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(existing)) != 0 {
		if err := json.Unmarshal(existing, &document); err != nil {
			return nil, nil, fmt.Errorf("parse Codex hooks.json: %w", err)
		}
	}
	hooks := make(map[string][]json.RawMessage)
	if raw := document["hooks"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, nil, fmt.Errorf("parse Codex Hook events: %w", err)
		}
	}
	return document, hooks, nil
}

func encodeHookDocument(document map[string]json.RawMessage, hooks map[string][]json.RawMessage) ([]byte, error) {
	encodedHooks, err := json.Marshal(hooks)
	if err != nil {
		return nil, err
	}
	document["hooks"] = encodedHooks
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Codex hooks.json: %w", err)
	}
	return append(encoded, '\n'), nil
}

func removeOwned(entries []json.RawMessage, event string) []json.RawMessage {
	kept := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		if !isOwnedHook(entry, event) {
			kept = append(kept, entry)
		}
	}
	return kept
}

func countOwnedHooks(entries []json.RawMessage, event string) int {
	count := 0
	for _, entry := range entries {
		if isOwnedHook(entry, event) {
			count++
		}
	}
	return count
}

func isOwnedHook(entry json.RawMessage, event string) bool {
	var matcher hookMatcher
	if err := json.Unmarshal(entry, &matcher); err != nil {
		return false
	}
	suffix := " hook codex " + event
	for _, hook := range matcher.Hooks {
		if hook.Type != "command" || !strings.HasSuffix(hook.Command, suffix) {
			continue
		}
		binary := strings.TrimSuffix(hook.Command, suffix)
		if filepath.Base(binary) == "mlink" {
			return true
		}
	}
	return false
}

func hookTimeout(event string) int {
	if event == "SessionEnd" {
		return 3
	}
	return 2
}
