package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"mlink/internal/adapter/codex"
	"mlink/internal/adapter/hermes"
	"mlink/internal/adapter/pi"
	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/doctor"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/launchagent"
	"mlink/internal/secret"
)

func (runtime *runtimeApplication) Status(ctx context.Context) (app.Status, error) {
	service := runtime.baseService
	service.Target = install.LocalTarget{}
	if _, err := os.Stat(runtime.paths.Journal); err == nil {
		store, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
		if err != nil {
			return app.Status{}, err
		}
		defer store.Close()
		ledger, err := journal.NewInstallationLedger(store, runtime.paths.Backups)
		if err != nil {
			return app.Status{}, err
		}
		service.Ledger = ledger
	} else if !errors.Is(err, fs.ErrNotExist) {
		return app.Status{}, err
	}
	return service.Status(ctx)
}

func (runtime *runtimeApplication) Doctor(ctx context.Context, selected []app.Agent) (doctor.Report, error) {
	status, err := runtime.Status(ctx)
	if err != nil {
		return doctor.Report{}, err
	}
	configuration := config.Config{}
	if status.Installed {
		configuration, err = (config.Store{Path: runtime.paths.Config}).Load()
		if err != nil {
			return doctor.Report{}, err
		}
	}
	agents := selected
	if len(agents) == 0 {
		for _, agent := range []app.Agent{app.Codex, app.Pi, app.Hermes} {
			if status.Adapters[agent] {
				agents = append(agents, agent)
			}
		}
	}
	report := doctor.Report{}
	report.Checks = append(report.Checks, runtime.checkLaunchAgent(ctx), runtime.checkBrokerSocket(ctx, agents))
	if report.Checks[len(report.Checks)-1].State == doctor.StatePassed {
		report.Checks = append(report.Checks, doctor.Check{ID: "provider.tencentdb", State: doctor.StatePassed, Code: "available"})
	} else {
		report.Checks = append(report.Checks, doctor.Check{ID: "provider.tencentdb", State: doctor.StateFailed, Code: "backend_unavailable"})
	}
	activity := runtime.adapterActivity(ctx)
	for _, agent := range agents {
		switch agent {
		case app.Codex:
			report.Checks = append(report.Checks, runtime.checkCodex(ctx, activity))
		case app.Pi:
			report.Checks = append(report.Checks, runtime.checkPi(ctx, activity))
		case app.Hermes:
			providerCheck, bridgeCheck := runtime.checkHermes(ctx, configuration)
			report.Checks = append(report.Checks, providerCheck, bridgeCheck)
		}
	}
	report.Checks = append(report.Checks, runtime.checkQueue(ctx))
	return report, nil
}

func (runtime *runtimeApplication) checkLaunchAgent(ctx context.Context) doctor.Check {
	resources, err := launchagent.Plan(runtime.paths, runtime.uid)
	if err != nil {
		return doctor.Check{ID: "broker.launchagent", State: doctor.StateFailed, Code: "plist_mismatch"}
	}
	content, err := os.ReadFile(resources[0].Target)
	if errors.Is(err, fs.ErrNotExist) {
		return doctor.Check{ID: "broker.launchagent", State: doctor.StateFailed, Code: "inactive"}
	}
	if err != nil || !bytes.Equal(content, resources[0].Content) {
		return doctor.Check{ID: "broker.launchagent", State: doctor.StateFailed, Code: "plist_mismatch"}
	}
	service := fmt.Sprintf("gui/%d/dev.mlink.broker", runtime.uid)
	if _, err := (install.LocalTarget{}).Run(ctx, []string{"launchctl", "print", service}, nil); err != nil {
		return doctor.Check{ID: "broker.launchagent", State: doctor.StateFailed, Code: "inactive"}
	}
	return doctor.Check{ID: "broker.launchagent", State: doctor.StatePassed, Code: "active"}
}

func (runtime *runtimeApplication) checkBrokerSocket(ctx context.Context, agents []app.Agent) doctor.Check {
	info, err := os.Lstat(runtime.paths.Socket)
	if errors.Is(err, fs.ErrNotExist) {
		return doctor.Check{ID: "broker.socket", State: doctor.StateFailed, Code: "socket_unavailable"}
	}
	if err != nil || info.Mode()&fs.ModeSocket == 0 {
		return doctor.Check{ID: "broker.socket", State: doctor.StateFailed, Code: "socket_unavailable"}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return doctor.Check{ID: "broker.socket", State: doctor.StateFailed, Code: "permission_invalid"}
	}
	adapterID := ""
	for _, agent := range agents {
		if agent == app.Codex || agent == app.Pi {
			adapterID = string(agent)
			break
		}
	}
	if adapterID == "" {
		connection, err := net.DialTimeout("unix", runtime.paths.Socket, 500*time.Millisecond)
		if err != nil {
			return doctor.Check{ID: "broker.socket", State: doctor.StateFailed, Code: "socket_unavailable"}
		}
		_ = connection.Close()
		return doctor.Check{ID: "broker.socket", State: doctor.StatePassed, Code: "reachable"}
	}
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return net.DialTimeout("unix", runtime.paths.Socket, 500*time.Millisecond)
	}}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	defer transport.CloseIdleConnections()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://mlink/v1/health?adapter_id="+adapterID, nil)
	response, err := client.Do(request)
	if err != nil {
		return doctor.Check{ID: "broker.socket", State: doctor.StateFailed, Code: "socket_unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return doctor.Check{ID: "broker.socket", State: doctor.StateFailed, Code: "socket_unavailable"}
	}
	return doctor.Check{ID: "broker.socket", State: doctor.StatePassed, Code: "reachable"}
}

func (runtime *runtimeApplication) checkCodex(ctx context.Context, activity map[string]bool) doctor.Check {
	path := filepath.Join(filepath.Dir(runtime.paths.Home), ".codex", "hooks.json")
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return doctor.Check{ID: "codex.hook", State: doctor.StateFailed, Code: "missing"}
	}
	if err != nil {
		return doctor.Check{ID: "codex.hook", State: doctor.StateFailed, Code: "config_conflict"}
	}
	desired, err := codex.DesiredHooksResource(content, path, runtime.paths.Binary)
	if err != nil || !bytes.Equal(content, desired.Content) {
		return doctor.Check{ID: "codex.hook", State: doctor.StateFailed, Code: "config_conflict"}
	}
	if !activity["codex"] {
		return doctor.Check{ID: "codex.hook", State: doctor.StatePendingAction, Code: "awaiting_trust"}
	}
	return doctor.Check{ID: "codex.hook", State: doctor.StatePassed, Code: "active"}
}

func (runtime *runtimeApplication) checkPi(ctx context.Context, activity map[string]bool) doctor.Check {
	desired, err := pi.PlanExtension(runtime.paths.Binary, runtime.paths.Socket)
	if err != nil {
		return doctor.Check{ID: "pi.extension", State: doctor.StateFailed, Code: "version_unsupported"}
	}
	content, err := os.ReadFile(desired.Target)
	if errors.Is(err, fs.ErrNotExist) {
		return doctor.Check{ID: "pi.extension", State: doctor.StateFailed, Code: "missing"}
	}
	if err != nil || !bytes.Equal(content, desired.Content) {
		return doctor.Check{ID: "pi.extension", State: doctor.StateFailed, Code: "fingerprint_mismatch"}
	}
	if !activity["pi"] {
		return doctor.Check{ID: "pi.extension", State: doctor.StatePendingAction, Code: "awaiting_first_turn"}
	}
	return doctor.Check{ID: "pi.extension", State: doctor.StatePassed, Code: "active"}
}

func (runtime *runtimeApplication) checkHermes(ctx context.Context, configuration config.Config) (doctor.Check, doctor.Check) {
	machine := environmentDefault("MLINK_HERMES_MACHINE", "hermes-agent-env")
	detection, err := hermes.Detect(ctx, install.LocalTarget{}, machine)
	if err != nil {
		failed := doctor.Check{ID: "hermes.provider", State: doctor.StateFailed, Code: "provider_mismatch"}
		return failed, doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "dns_failed"}
	}
	target, err := hermes.NewOrbTarget(machine, detection.HermesHome, nil)
	if err != nil {
		return doctor.Check{ID: "hermes.provider", State: doctor.StateFailed, Code: "provider_mismatch"}, doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "dns_failed"}
	}
	providerCheck := inspectHermesConfig(ctx, target, detection.HermesHome)
	if providerCheck.State != doctor.StatePassed {
		return providerCheck, doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "timeout"}
	}
	bridgeCheck := runtime.probeHermesBridge(ctx, machine, configuration)
	return providerCheck, bridgeCheck
}

func inspectHermesConfig(ctx context.Context, target install.Target, home string) doctor.Check {
	content, _, err := target.Read(ctx, filepath.Join(home, "config.yaml"))
	if err != nil {
		return doctor.Check{ID: "hermes.provider", State: doctor.StateFailed, Code: "provider_mismatch"}
	}
	var document map[string]any
	if yaml.Unmarshal(content, &document) != nil {
		return doctor.Check{ID: "hermes.provider", State: doctor.StateFailed, Code: "provider_mismatch"}
	}
	memory, _ := document["memory"].(map[string]any)
	agent, _ := document["agent"].(map[string]any)
	disabled := false
	if values, ok := agent["disabled_toolsets"].([]any); ok {
		for _, value := range values {
			disabled = disabled || value == "memory"
		}
	}
	if memory["provider"] != "mlink" {
		return doctor.Check{ID: "hermes.provider", State: doctor.StateFailed, Code: "provider_mismatch"}
	}
	if memory["memory_enabled"] != false || memory["user_profile_enabled"] != false || !disabled {
		return doctor.Check{ID: "hermes.provider", State: doctor.StateFailed, Code: "builtin_memory_active"}
	}
	for _, path := range []string{filepath.Join(home, "plugins", "mlink", "plugin.yaml"), filepath.Join(home, "plugins", "mlink", "__init__.py"), filepath.Join(home, "mlink.json")} {
		if _, _, err := target.Read(ctx, path); err != nil {
			return doctor.Check{ID: "hermes.provider", State: doctor.StateFailed, Code: "provider_mismatch"}
		}
	}
	return doctor.Check{ID: "hermes.provider", State: doctor.StatePassed, Code: "active"}
}

func (runtime *runtimeApplication) probeHermesBridge(ctx context.Context, machine string, configuration config.Config) doctor.Check {
	endpoint := strings.TrimSuffix(configuration.Broker.HermesEndpoint, "/")
	if endpoint == "" {
		return doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "dns_failed"}
	}
	token, err := (secret.Keychain{}).Get(ctx, "adapter/hermes/token")
	if err != nil {
		return doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "auth_failed"}
	}
	defer wipeRuntimeSecret(token)
	payload, _ := json.Marshal(map[string]string{"endpoint": endpoint, "token": string(token)})
	script := `import json,sys,urllib.request
c=json.load(sys.stdin)
r=urllib.request.Request(c["endpoint"]+"/v1/health?adapter_id=hermes",headers={"Authorization":"Bearer "+c["token"]})
with urllib.request.urlopen(r,timeout=1.5) as x: print(x.status)`
	output, err := (install.LocalTarget{}).Run(ctx, []string{"orb", "-m", machine, "python3", "-c", script}, bytes.NewReader(payload))
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "401") || strings.Contains(message, "403") {
			return doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "auth_failed"}
		}
		if strings.Contains(message, "name or service") || strings.Contains(message, "resolve") {
			return doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "dns_failed"}
		}
		return doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "timeout"}
	}
	if strings.TrimSpace(string(output)) != "200" {
		return doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "auth_failed"}
	}
	return doctor.Check{ID: "hermes.bridge", State: doctor.StatePassed, Code: "reachable"}
}

func (runtime *runtimeApplication) adapterActivity(ctx context.Context) map[string]bool {
	result := make(map[string]bool)
	store, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
	if err != nil {
		return result
	}
	defer store.Close()
	for _, adapterID := range []string{"codex", "pi", "hermes"} {
		_, err := store.LatestAdapterActivity(ctx, adapterID)
		result[adapterID] = err == nil
	}
	return result
}

func (runtime *runtimeApplication) checkQueue(ctx context.Context) doctor.Check {
	store, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
	if errors.Is(err, fs.ErrNotExist) {
		return doctor.Check{ID: "journal.queue", State: doctor.StatePassed, Code: "clean"}
	}
	if err != nil {
		return doctor.Check{ID: "journal.queue", State: doctor.StateFailed, Code: "permanent_failure"}
	}
	defer store.Close()
	summary, err := store.QueueSummary(ctx)
	if err != nil {
		return doctor.Check{ID: "journal.queue", State: doctor.StateFailed, Code: "permanent_failure"}
	}
	switch {
	case summary.Ambiguous > 0:
		return doctor.Check{ID: "journal.queue", State: doctor.StateFailed, Code: "ambiguous"}
	case summary.Permanent > 0:
		return doctor.Check{ID: "journal.queue", State: doctor.StateFailed, Code: "permanent_failure"}
	case summary.Retrying > 0 || summary.Queued > 0:
		return doctor.Check{ID: "journal.queue", State: doctor.StatePendingAction, Code: "retrying"}
	default:
		return doctor.Check{ID: "journal.queue", State: doctor.StatePassed, Code: "clean"}
	}
}
