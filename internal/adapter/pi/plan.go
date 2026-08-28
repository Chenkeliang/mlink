package pi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"path/filepath"
	"text/template"

	"mlink/internal/install"
)

//go:embed templates/mlink.ts.tmpl
var extensionTemplate string

func PlanExtension(binaryPath, socketPath string) (install.DesiredResource, error) {
	target, err := extensionTarget(binaryPath)
	if err != nil {
		return install.DesiredResource{}, err
	}
	if !filepath.IsAbs(socketPath) {
		return install.DesiredResource{}, errors.New("Pi Broker socket path must be absolute")
	}
	tmpl, err := template.New("mlink.ts").Funcs(template.FuncMap{
		"js": func(value string) (string, error) {
			encoded, err := json.Marshal(value)
			return string(encoded), err
		},
	}).Parse(extensionTemplate)
	if err != nil {
		return install.DesiredResource{}, err
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, struct{ SocketPath string }{SocketPath: socketPath}); err != nil {
		return install.DesiredResource{}, err
	}
	return install.DesiredResource{
		OwnerID: "dev.mlink.adapter.pi",
		Target:  target,
		Content: rendered.Bytes(),
		Mode:    0o600,
		SemanticDiff: []install.SemanticDiff{{
			Path: "extension:mlink", Before: "absent or owned", After: "official Pi lifecycle Extension",
		}},
	}, nil
}

func RemoveOwnedExtension(binaryPath string) (install.DesiredResource, error) {
	target, err := extensionTarget(binaryPath)
	if err != nil {
		return install.DesiredResource{}, err
	}
	return install.DesiredResource{
		OwnerID: "dev.mlink.adapter.pi",
		Target:  target,
		Action:  install.ActionRemoveOwned,
	}, nil
}

func extensionTarget(binaryPath string) (string, error) {
	if !filepath.IsAbs(binaryPath) || filepath.Base(binaryPath) != "mlink" || filepath.Base(filepath.Dir(binaryPath)) != "bin" {
		return "", errors.New("MLink binary path is invalid")
	}
	localDirectory := filepath.Dir(filepath.Dir(binaryPath))
	if filepath.Base(localDirectory) != ".local" {
		return "", errors.New("MLink binary must be installed under ~/.local/bin")
	}
	home := filepath.Dir(localDirectory)
	return filepath.Join(home, ".pi", "agent", "extensions", "mlink.ts"), nil
}
