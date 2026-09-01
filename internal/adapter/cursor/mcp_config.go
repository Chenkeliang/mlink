package cursor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"mlink/internal/install"
)

const mcpServerName = "mlink-memory"

type mcpServerConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func PlanMCPConfig(existing []byte, binaryPath string) ([]byte, error) {
	if !filepath.IsAbs(binaryPath) || filepath.Base(binaryPath) != "mlink" {
		return nil, errors.New("Cursor MCP binary path must be an absolute MLink binary")
	}
	document, servers, err := parseMCPDocument(existing)
	if err != nil {
		return nil, err
	}
	server, err := json.Marshal(mcpServerConfig{Command: binaryPath, Args: []string{"mcp", "serve"}})
	if err != nil {
		return nil, err
	}
	servers[mcpServerName] = server
	return encodeMCPDocument(document, servers)
}

func RemoveOwnedMCPConfig(existing []byte) ([]byte, error) {
	document, servers, err := parseMCPDocument(existing)
	if err != nil {
		return nil, err
	}
	delete(servers, mcpServerName)
	return encodeMCPDocument(document, servers)
}

func DesiredMCPResource(existing []byte, targetPath, binaryPath string) (install.DesiredResource, error) {
	content, err := PlanMCPConfig(existing, binaryPath)
	if err != nil {
		return install.DesiredResource{}, err
	}
	return install.DesiredResource{
		OwnerID: "dev.mlink.adapter.cursor", Target: targetPath, Content: content, Mode: 0o600,
		SemanticDiff: []install.SemanticDiff{{Path: "mcpServers.mlink-memory", Before: "absent or owned", After: "local stdio; no credentials"}},
	}, nil
}

func parseMCPDocument(existing []byte) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	document := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(existing)) != 0 {
		if err := json.Unmarshal(existing, &document); err != nil {
			return nil, nil, fmt.Errorf("parse Cursor mcp.json: %w", err)
		}
	}
	servers := make(map[string]json.RawMessage)
	if raw := document["mcpServers"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, nil, fmt.Errorf("parse Cursor MCP servers: %w", err)
		}
	}
	return document, servers, nil
}

func encodeMCPDocument(document map[string]json.RawMessage, servers map[string]json.RawMessage) ([]byte, error) {
	encodedServers, err := json.Marshal(servers)
	if err != nil {
		return nil, err
	}
	document["mcpServers"] = encodedServers
	result, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(result, '\n'), nil
}
