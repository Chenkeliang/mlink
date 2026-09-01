package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	gorruntime "runtime"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"mlink/internal/adapter/codex"
	"mlink/internal/adapter/hermes"
	"mlink/internal/adapter/pi"
	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/doctor"
	"mlink/internal/identity"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/launchagent"
	"mlink/internal/panel"
	"mlink/internal/secret"
	"mlink/internal/version"
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

func (runtime *runtimeApplication) ConfigDiff(ctx context.Context) (app.DriftReport, error) {
	store, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
	if err != nil {
		return app.DriftReport{}, err
	}
	defer store.Close()
	ledger, err := journal.NewInstallationLedger(store, runtime.paths.Backups)
	if err != nil {
		return app.DriftReport{}, err
	}
	status, err := runtime.Status(ctx)
	if err != nil {
		return app.DriftReport{}, err
	}
	request := defaultInstallRequest()
	for _, agent := range []app.Agent{app.Codex, app.Pi, app.Hermes} {
		if status.Adapters[agent] {
			request.Agents = append(request.Agents, agent)
		}
	}
	if len(request.Agents) == 0 {
		return app.DriftReport{}, errors.New("MLink has no active Adapter installation")
	}
	service, _, err := runtime.prepare(ctx, request, ledger)
	if err != nil {
		return app.DriftReport{}, err
	}
	return service.ConfigDiff(ctx)
}

func (runtime *runtimeApplication) ListBackups(ctx context.Context) ([]journal.BackupSummary, error) {
	store, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	ledger, err := journal.NewInstallationLedger(store, runtime.paths.Backups)
	if err != nil {
		return nil, err
	}
	return ledger.ListBackupSummaries(ctx)
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
	if status.Installed {
		keychain := secret.Keychain{}
		identityKey, keyErr := keychain.Get(ctx, "identity/hmac-key")
		bindingErr := keyErr
		if keyErr == nil && len(identityKey) == 32 {
			bindings, err := (identity.Repository{Secrets: keychain, IdentityKey: identityKey}).Load(ctx, configuration.Bindings)
			bindingErr = err
			bindings.Wipe()
		}
		report.Checks = append(report.Checks, doctor.IdentityChecks(configuration, identityKey, bindingErr)...)
		wipeRuntimeSecret(identityKey)
		report.Checks = append(report.Checks, doctor.CompatibilityChecks(version.Current(), configuration.SchemaVersion, gorruntime.GOOS, gorruntime.GOARCH)...)
	}
	report.Checks = append(report.Checks, runtime.preflightChecks(ctx)...)
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
			providerCheck, bridgeCheck, sessionCheck := runtime.checkHermes(ctx, configuration)
			report.Checks = append(report.Checks, providerCheck, bridgeCheck, sessionCheck, runtime.checkHermesGrantFingerprint(ctx))
		}
	}
	if status.Installed && configuration.ControlPlane != nil {
		report.Checks = append(report.Checks, runtime.checkHubContainer(ctx)...)
	}
	report.Checks = append(report.Checks, runtime.checkQueue(ctx))
	return report, nil
}

func (runtime *runtimeApplication) preflightChecks(ctx context.Context) []doctor.Check {
	checks := make([]doctor.Check, 0, 7)
	for _, dependency := range []struct{ id, command string }{
		{"dependency.docker", "docker"}, {"dependency.orb", "orb"}, {"dependency.keychain", "security"},
	} {
		check := doctor.Check{ID: dependency.id, State: doctor.StatePassed, Code: "available"}
		if _, err := exec.LookPath(dependency.command); err != nil {
			check.State, check.Code, check.Message = doctor.StateFailed, "missing", "install required dependency: "+dependency.command
		}
		checks = append(checks, check)
	}
	arch := doctor.Check{ID: "system.architecture", State: doctor.StatePassed, Code: "supported", Message: gorruntime.GOOS + "/" + gorruntime.GOARCH}
	if gorruntime.GOOS != "darwin" || gorruntime.GOARCH != "arm64" {
		arch.State, arch.Code = doctor.StateFailed, "unsupported"
	}
	checks = append(checks, arch)
	var stats syscall.Statfs_t
	disk := doctor.Check{ID: "system.disk", State: doctor.StatePassed, Code: "sufficient"}
	if err := syscall.Statfs(filepath.Dir(runtime.paths.Home), &stats); err != nil || uint64(stats.Bavail)*uint64(stats.Bsize) < 256<<20 {
		disk.State, disk.Code, disk.Message = doctor.StateFailed, "insufficient", "at least 256 MiB free space is required"
	}
	checks = append(checks, disk)
	core := doctor.Check{ID: "network.memorycore", State: doctor.StatePassed, Code: "reachable"}
	connection, err := net.DialTimeout("tcp", "127.0.0.1:8420", 500*time.Millisecond)
	if err != nil {
		core.State, core.Code, core.Message = doctor.StateFailed, "unreachable", "start TencentDB MemoryCore on 127.0.0.1:8420"
	} else {
		_ = connection.Close()
	}
	checks = append(checks, core)
	return checks
}

func (runtime *runtimeApplication) checkHubContainer(ctx context.Context) []doctor.Check {
	output, err := (install.LocalTarget{}).Run(ctx, []string{"docker", "inspect", panel.ContainerName}, nil)
	if err != nil {
		failed := doctor.Check{State: doctor.StateFailed, Code: "missing", Message: "run: mlink panel provision --dry-run --json"}
		return []doctor.Check{{ID: "hub.image", State: failed.State, Code: failed.Code, Message: failed.Message}, {ID: "hub.args", State: failed.State, Code: failed.Code, Message: failed.Message}, {ID: "hub.knowledge_volume", State: failed.State, Code: failed.Code, Message: failed.Message}}
	}
	return inspectHubContainer(output)
}

func inspectHubContainer(content []byte) []doctor.Check {
	var values []struct {
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		HostConfig struct {
			RestartPolicy struct {
				Name string `json:"Name"`
			} `json:"RestartPolicy"`
		} `json:"HostConfig"`
		Mounts []struct {
			Name, Destination string
		} `json:"Mounts"`
	}
	invalid := func(id, code string) doctor.Check { return doctor.Check{ID: id, State: doctor.StateFailed, Code: code} }
	if json.Unmarshal(content, &values) != nil || len(values) != 1 {
		return []doctor.Check{invalid("hub.image", "inspect_invalid"), invalid("hub.args", "inspect_invalid"), invalid("hub.knowledge_volume", "inspect_invalid")}
	}
	value := values[0]
	image := doctor.Check{ID: "hub.image", State: doctor.StatePassed, Code: "pinned"}
	if value.Config.Image != panel.ImageReference {
		image.State, image.Code = doctor.StateFailed, "digest_mismatch"
	}
	args := doctor.Check{ID: "hub.args", State: doctor.StatePassed, Code: "compatible"}
	if value.Config.Labels["dev.mlink.component"] != "memory-hub" || value.HostConfig.RestartPolicy.Name != "unless-stopped" {
		args.State, args.Code = doctor.StateFailed, "argument_drift"
	}
	volume := doctor.Check{ID: "hub.knowledge_volume", State: doctor.StateFailed, Code: "volume_mismatch"}
	for _, mount := range value.Mounts {
		if mount.Name == panel.VolumeName && mount.Destination == "/data/knowledge" {
			volume.State, volume.Code = doctor.StatePassed, "persistent"
		}
	}
	return []doctor.Check{image, args, volume}
}

func (runtime *runtimeApplication) checkHermesGrantFingerprint(ctx context.Context) doctor.Check {
	grant, err := (secret.Keychain{}).Get(ctx, "adapter/hermes/token")
	if err != nil {
		return doctor.Check{ID: "hermes.grant", State: doctor.StateFailed, Code: "keychain_missing"}
	}
	defer wipeRuntimeSecret(grant)
	machine := environmentDefault("MLINK_HERMES_MACHINE", "hermes-agent-env")
	detection, err := hermes.Detect(ctx, install.LocalTarget{}, machine)
	if err != nil {
		return doctor.Check{ID: "hermes.grant", State: doctor.StateFailed, Code: "orb_unavailable"}
	}
	target, err := hermes.NewOrbTarget(machine, detection.HermesHome, nil)
	if err != nil {
		return doctor.Check{ID: "hermes.grant", State: doctor.StateFailed, Code: "orb_unavailable"}
	}
	content, _, err := target.Read(ctx, filepath.Join(detection.HermesHome, "mlink.json"))
	if err != nil {
		return doctor.Check{ID: "hermes.grant", State: doctor.StateFailed, Code: "config_missing"}
	}
	return compareHermesGrant(content, grant)
}

func compareHermesGrant(content, keychainGrant []byte) doctor.Check {
	var value struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(content, &value) != nil || value.Token == "" {
		return doctor.Check{ID: "hermes.grant", State: doctor.StateFailed, Code: "config_invalid"}
	}
	left := sha256.Sum256([]byte(value.Token))
	right := sha256.Sum256(keychainGrant)
	check := doctor.Check{ID: "hermes.grant", State: doctor.StatePassed, Code: "fingerprint_match", Message: "sha256:" + hex.EncodeToString(right[:])[:12]}
	if !hmac.Equal(left[:], right[:]) {
		check.State, check.Code, check.Message = doctor.StateFailed, "fingerprint_mismatch", "run: mlink maintenance credentials rotate hermes-grant --dry-run --json"
	}
	return check
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

func (runtime *runtimeApplication) checkHermes(ctx context.Context, configuration config.Config) (doctor.Check, doctor.Check, doctor.Check) {
	machine := environmentDefault("MLINK_HERMES_MACHINE", "hermes-agent-env")
	detection, err := hermes.Detect(ctx, install.LocalTarget{}, machine)
	if err != nil {
		failed := doctor.Check{ID: "hermes.provider", State: doctor.StateFailed, Code: "provider_mismatch"}
		return failed, doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "dns_failed"}, doctor.Check{ID: "hermes.session_policy", State: doctor.StateFailed, Code: "unavailable"}
	}
	target, err := hermes.NewOrbTarget(machine, detection.HermesHome, nil)
	if err != nil {
		return doctor.Check{ID: "hermes.provider", State: doctor.StateFailed, Code: "provider_mismatch"}, doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "dns_failed"}, doctor.Check{ID: "hermes.session_policy", State: doctor.StateFailed, Code: "unavailable"}
	}
	providerCheck := inspectHermesConfig(ctx, target, detection.HermesHome)
	configContent, _, configErr := target.Read(ctx, filepath.Join(detection.HermesHome, "config.yaml"))
	sessionCheck := inspectHermesSessionPolicy(configContent)
	if configErr != nil {
		sessionCheck = doctor.Check{ID: "hermes.session_policy", State: doctor.StateFailed, Code: "unavailable"}
	}
	if providerCheck.State != doctor.StatePassed {
		return providerCheck, doctor.Check{ID: "hermes.bridge", State: doctor.StateFailed, Code: "timeout"}, sessionCheck
	}
	bridgeCheck := runtime.probeHermesBridge(ctx, machine, configuration)
	return providerCheck, bridgeCheck, sessionCheck
}

func inspectHermesSessionPolicy(content []byte) doctor.Check {
	var document map[string]any
	if yaml.Unmarshal(content, &document) != nil {
		return doctor.Check{ID: "hermes.session_policy", State: doctor.StateFailed, Code: "unavailable"}
	}
	groupShared, groupExists := document["group_sessions_per_user"].(bool)
	threadShared, threadExists := document["thread_sessions_per_user"].(bool)
	if !groupExists || groupShared {
		return doctor.Check{ID: "hermes.session_policy", State: doctor.StateFailed, Code: "group_session_split"}
	}
	if !threadExists || threadShared {
		return doctor.Check{ID: "hermes.session_policy", State: doctor.StateFailed, Code: "thread_session_split"}
	}
	return doctor.Check{ID: "hermes.session_policy", State: doctor.StatePassed, Code: "active"}
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
	return doctor.JournalCheck(summary)
}
