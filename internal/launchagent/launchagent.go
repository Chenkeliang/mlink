package launchagent

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"text/template"

	"mlink/internal/install"
	"mlink/internal/layout"
)

const label = "dev.mlink.broker"

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{xml .Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{xml .Binary}}</string>
    <string>broker</string>
    <string>serve</string>
    <string>--config</string>
    <string>{{xml .Config}}</string>
    <string>--journal</string>
    <string>{{xml .Journal}}</string>
    <string>--socket</string>
    <string>{{xml .Socket}}</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ProcessType</key>
  <string>Background</string>
  <key>Umask</key>
  <integer>63</integer>
  <key>StandardOutPath</key>
  <string>{{xml .Stdout}}</string>
  <key>StandardErrorPath</key>
  <string>{{xml .Stderr}}</string>
</dict>
</plist>
`

func Plan(paths layout.Paths, uid int) ([]install.DesiredResource, error) {
	if uid <= 0 {
		return nil, errors.New("user UID must be positive")
	}
	if err := validatePaths(paths); err != nil {
		return nil, err
	}
	home := filepath.Dir(paths.Home)
	plistPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	content, err := renderPlist(paths)
	if err != nil {
		return nil, err
	}
	domain := "gui/" + strconv.Itoa(uid)
	service := domain + "/" + label
	return []install.DesiredResource{
		{
			OwnerID: "dev.mlink.broker",
			Target:  plistPath,
			Content: content,
			Mode:    0o600,
			SemanticDiff: []install.SemanticDiff{{
				Path: "launchagent:" + label, Before: "absent or owned", After: "MLink Broker user service",
			}},
		},
		{
			OwnerID:         "dev.mlink.broker",
			Target:          "service:bootstrap:" + label,
			Action:          install.ActionService,
			Command:         []string{"launchctl", "bootstrap", domain, plistPath},
			RollbackCommand: []string{"launchctl", "bootout", service},
		},
		{
			OwnerID: "dev.mlink.broker",
			Target:  "service:kickstart:" + label,
			Action:  install.ActionService,
			Command: []string{"launchctl", "kickstart", "-k", service},
		},
	}, nil
}

func PlanUnload(paths layout.Paths, uid int) ([]install.DesiredResource, error) {
	if uid <= 0 {
		return nil, errors.New("user UID must be positive")
	}
	if err := validatePaths(paths); err != nil {
		return nil, err
	}
	home := filepath.Dir(paths.Home)
	plistPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	service := "gui/" + strconv.Itoa(uid) + "/" + label
	return []install.DesiredResource{
		{
			OwnerID:         "dev.mlink.broker",
			Target:          "service:bootout:" + label,
			Action:          install.ActionService,
			Command:         []string{"launchctl", "bootout", service},
			RollbackCommand: []string{"launchctl", "bootstrap", "gui/" + strconv.Itoa(uid), plistPath},
		},
		{
			OwnerID: "dev.mlink.broker",
			Target:  plistPath,
			Action:  install.ActionRemoveOwned,
		},
	}, nil
}

func renderPlist(paths layout.Paths) ([]byte, error) {
	tmpl, err := template.New("launchagent.plist").Funcs(template.FuncMap{
		"xml": escapeXML,
	}).Parse(plistTemplate)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	err = tmpl.Execute(&output, struct {
		Label   string
		Binary  string
		Config  string
		Journal string
		Socket  string
		Stdout  string
		Stderr  string
	}{
		Label: label, Binary: paths.Binary, Config: paths.Config, Journal: paths.Journal, Socket: paths.Socket,
		Stdout: filepath.Join(paths.Home, "logs", "broker.log"),
		Stderr: filepath.Join(paths.Home, "logs", "broker.error.log"),
	})
	if err != nil {
		return nil, fmt.Errorf("render LaunchAgent: %w", err)
	}
	return output.Bytes(), nil
}

func escapeXML(value string) (string, error) {
	var output bytes.Buffer
	if err := xml.EscapeText(&output, []byte(value)); err != nil {
		return "", err
	}
	return output.String(), nil
}

func validatePaths(paths layout.Paths) error {
	for name, path := range map[string]string{
		"MLink home": paths.Home,
		"binary":     paths.Binary,
		"config":     paths.Config,
		"journal":    paths.Journal,
		"socket":     paths.Socket,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s path must be absolute", name)
		}
	}
	return nil
}
