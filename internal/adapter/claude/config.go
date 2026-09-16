package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"mlink/internal/install"
)

// claudeEvents are the Claude Code lifecycle Hook events MLink owns. Claude Code
// carries no turn identifier of its own; prompt_id on the shared Hook payload is
// the per-turn correlation ID, so capture events stay inside these four.
var claudeEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"}

// settingsHooksKey is the only key in ~/.claude/settings.json MLink may change.
const settingsHooksKey = "hooks"

type hookCommand struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	Timeout       int    `json:"timeout"`
	StatusMessage string `json:"statusMessage,omitempty"`
}

type hookMatcher struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

func PlanHooks(existing []byte, binaryPath string) ([]byte, error) {
	if !filepath.IsAbs(binaryPath) {
		return nil, errors.New("Claude Code Hook binary path must be absolute")
	}
	document, hooks, err := parseSettingsDocument(existing)
	if err != nil {
		return nil, err
	}
	for _, event := range claudeEvents {
		hooks[event] = removeOwned(hooks[event], event)
		matcher := hookMatcher{Hooks: []hookCommand{{
			Type:    "command",
			Command: binaryPath + " hook claude " + event,
			Timeout: hookTimeout(event),
		}}}
		switch event {
		case "SessionStart":
			matcher.Matcher = "startup|resume|clear|compact|fork"
			matcher.Hooks[0].StatusMessage = "Loading MLink memory"
		case "UserPromptSubmit":
			matcher.Hooks[0].StatusMessage = "Recalling MLink memory"
		}
		encoded, err := json.Marshal(matcher)
		if err != nil {
			return nil, err
		}
		hooks[event] = append(hooks[event], encoded)
	}
	return encodeSettingsDocument(document, hooks)
}

func RemoveOwnedHooks(existing []byte) ([]byte, error) {
	document, hooks, err := parseSettingsDocument(existing)
	if err != nil {
		return nil, err
	}
	for _, event := range claudeEvents {
		hooks[event] = removeOwned(hooks[event], event)
		if len(hooks[event]) == 0 {
			delete(hooks, event)
		}
	}
	return encodeSettingsDocument(document, hooks)
}

func DesiredHooksResource(existing []byte, targetPath, binaryPath string) (install.DesiredResource, error) {
	content, err := PlanHooks(existing, binaryPath)
	if err != nil {
		return install.DesiredResource{}, err
	}
	before, err := ProtectedSettingsHash(existing)
	if err != nil {
		return install.DesiredResource{}, err
	}
	after, err := ProtectedSettingsHash(content)
	if err != nil {
		return install.DesiredResource{}, err
	}
	return install.DesiredResource{
		OwnerID: "dev.mlink.adapter.claude",
		Target:  targetPath,
		Content: content,
		Mode:    0o600,
		SemanticDiff: []install.SemanticDiff{{
			Path: "hooks", Before: "existing Hooks preserved", After: "MLink lifecycle Hooks present",
		}},
		ProtectedInvariants: []install.Invariant{{
			Name: "all_non_hook_claude_settings", BeforeHash: before, ProposedHash: after, Preserved: before == after,
		}},
		Verify: func(written []byte) error {
			hash, err := ProtectedSettingsHash(written)
			if err != nil {
				return err
			}
			if hash != before {
				return errors.New("Claude Code settings outside hooks changed")
			}
			return nil
		},
	}, nil
}

// ProtectedSettingsHash digests every Claude Code setting MLink must never
// touch: model, env, permissions, plugins and any other top-level key.
// OwnedHooksInstalled reports whether every MLink Hook is present exactly once
// and points at binaryPath. Claude Code rewrites settings.json whenever the user
// changes an unrelated setting, so whole-file equality is not a drift signal.
func OwnedHooksInstalled(existing []byte, binaryPath string) (bool, error) {
	_, hooks, err := parseSettingsDocument(existing)
	if err != nil {
		return false, err
	}
	for _, event := range claudeEvents {
		if countOwnedHooks(hooks[event], event) != 1 {
			return false, nil
		}
		if !hasHookCommand(hooks[event], binaryPath+" hook claude "+event) {
			return false, nil
		}
	}
	return true, nil
}

func hasHookCommand(entries []json.RawMessage, command string) bool {
	for _, entry := range entries {
		var matcher hookMatcher
		if err := json.Unmarshal(entry, &matcher); err != nil {
			continue
		}
		for _, hook := range matcher.Hooks {
			if hook.Command == command {
				return true
			}
		}
	}
	return false
}

func ProtectedSettingsHash(content []byte) (string, error) {
	document, _, err := parseSettingsDocument(content)
	if err != nil {
		return "", err
	}
	delete(document, settingsHooksKey)
	normalized, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(normalized)
	return hex.EncodeToString(digest[:]), nil
}

func parseSettingsDocument(existing []byte) (map[string]json.RawMessage, map[string][]json.RawMessage, error) {
	document := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(existing)) != 0 {
		if err := json.Unmarshal(existing, &document); err != nil {
			return nil, nil, fmt.Errorf("parse Claude Code settings.json: %w", err)
		}
	}
	hooks := make(map[string][]json.RawMessage)
	if raw := document[settingsHooksKey]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, nil, fmt.Errorf("parse Claude Code Hook events: %w", err)
		}
	}
	return document, hooks, nil
}

func encodeSettingsDocument(document map[string]json.RawMessage, hooks map[string][]json.RawMessage) ([]byte, error) {
	if len(hooks) == 0 {
		delete(document, settingsHooksKey)
	} else {
		encodedHooks, err := json.Marshal(hooks)
		if err != nil {
			return nil, err
		}
		document[settingsHooksKey] = encodedHooks
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Claude Code settings.json: %w", err)
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
	suffix := " hook claude " + event
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
