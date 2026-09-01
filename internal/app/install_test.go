package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"mlink/internal/config"
	"mlink/internal/install"
	"mlink/internal/layout"
)

type memoryFile struct {
	content []byte
	mode    fs.FileMode
}

type memoryTarget struct {
	files       map[string]memoryFile
	writes      int
	runs        int
	failAtWrite int
	failAtRun   int
}

func newMemoryTarget(files map[string]memoryFile) *memoryTarget {
	cloned := make(map[string]memoryFile, len(files))
	for path, file := range files {
		cloned[path] = memoryFile{content: append([]byte(nil), file.content...), mode: file.mode}
	}
	return &memoryTarget{files: cloned}
}

func (target *memoryTarget) Read(_ context.Context, path string) ([]byte, fs.FileMode, error) {
	file, exists := target.files[path]
	if !exists {
		return nil, 0, fs.ErrNotExist
	}
	return append([]byte(nil), file.content...), file.mode, nil
}

func (target *memoryTarget) WriteAtomic(_ context.Context, path string, content []byte, mode fs.FileMode) error {
	target.writes++
	if target.failAtWrite > 0 && target.writes == target.failAtWrite {
		return errors.New("injected write failure")
	}
	target.files[path] = memoryFile{content: append([]byte(nil), content...), mode: mode}
	return nil
}

func (target *memoryTarget) Remove(_ context.Context, path string) error {
	delete(target.files, path)
	return nil
}

func (target *memoryTarget) Run(context.Context, []string, io.Reader) ([]byte, error) {
	target.runs++
	if target.failAtRun > 0 && target.runs == target.failAtRun {
		return nil, errors.New("injected run failure")
	}
	return nil, nil
}

type memoryLedger struct {
	backups      map[string]install.Backup
	owned        []install.OwnedResource
	activePlanID string
	activeAgents []string
}

func newMemoryLedger() *memoryLedger {
	return &memoryLedger{backups: make(map[string]install.Backup)}
}

func (ledger *memoryLedger) SaveBackup(_ context.Context, backup install.Backup) error {
	backup.Content = append([]byte(nil), backup.Content...)
	ledger.backups[backup.PlanID+"\x00"+backup.Target] = backup
	return nil
}

func (ledger *memoryLedger) LoadBackup(_ context.Context, planID, target string) (install.Backup, error) {
	backup, exists := ledger.backups[planID+"\x00"+target]
	if !exists {
		return install.Backup{}, fs.ErrNotExist
	}
	backup.Content = append([]byte(nil), backup.Content...)
	return backup, nil
}

func (ledger *memoryLedger) RecordOwned(_ context.Context, resource install.OwnedResource) error {
	ledger.owned = append(ledger.owned, resource)
	return nil
}

func (ledger *memoryLedger) ListOwned(context.Context) ([]install.OwnedResource, error) {
	return append([]install.OwnedResource(nil), ledger.owned...), nil
}

func (ledger *memoryLedger) RecordInstallPlan(_ context.Context, planID string, agents []string) error {
	ledger.activePlanID = planID
	ledger.activeAgents = append([]string(nil), agents...)
	return nil
}

func (ledger *memoryLedger) ActiveInstallPlan(context.Context) (string, error) {
	if ledger.activePlanID == "" {
		return "", fs.ErrNotExist
	}
	return ledger.activePlanID, nil
}

func (ledger *memoryLedger) MarkInstallRemoved(context.Context) error {
	ledger.activePlanID = ""
	ledger.activeAgents = nil
	return nil
}

type memorySecrets struct {
	values  map[string][]byte
	puts    int
	deletes int
}

func (store *memorySecrets) Put(_ context.Context, account string, value []byte) error {
	store.puts++
	if store.values == nil {
		store.values = make(map[string][]byte)
	}
	store.values[account] = append([]byte(nil), value...)
	return nil
}

func (store *memorySecrets) Get(_ context.Context, account string) ([]byte, error) {
	value, exists := store.values[account]
	if !exists {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), value...), nil
}

func (store *memorySecrets) Delete(_ context.Context, account string) error {
	store.deletes++
	delete(store.values, account)
	return nil
}

func TestPlanInstallContainsAllSelectedResourcesAndNoWrites(t *testing.T) {
	service, target, _ := newInstallFixture(t)
	request := fixtureInstallRequest()
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wantTargets := []string{
		"/Users/test/.local/bin/mlink",
		"/Users/test/.mlink/config.yaml",
		"/Users/test/.codex/hooks.json",
		"/Users/test/.pi/agent/extensions/mlink.ts",
		"/home/test/.hermes/plugins/mlink/plugin.yaml",
		"/home/test/.hermes/plugins/mlink/__init__.py",
		"/home/test/.hermes/mlink.json",
		"/home/test/.hermes/config.yaml",
		"/Users/test/Library/LaunchAgents/dev.mlink.broker.plist",
	}
	assertPlanTargets(t, plan, wantTargets)
	if target.writes != 0 || target.runs != 0 {
		t.Fatalf("preview writes/runs = %d/%d", target.writes, target.runs)
	}
	rendered, err := install.RenderJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rendered, request.SecretInputs[MemoryCoreTokenSecret]) || bytes.Contains(rendered, []byte("hermes-grant-secret")) {
		t.Fatal("secret leaked into serialized ChangeSet")
	}
	if len(plan.ProtectedInvariants) == 0 {
		t.Fatal("protected model/auth invariants missing")
	}
	for _, invariant := range plan.ProtectedInvariants {
		if !invariant.Preserved {
			t.Fatalf("invariant not preserved: %#v", invariant)
		}
	}
	var hermesDiff []install.SemanticDiff
	for _, operation := range plan.Operations {
		if operation.Target == "/home/test/.hermes/config.yaml" {
			hermesDiff = operation.SemanticDiff
			break
		}
	}
	for _, want := range []string{"group_sessions_per_user", "thread_sessions_per_user"} {
		found := false
		for _, diff := range hermesDiff {
			found = found || diff.Path == want && diff.After == "false"
		}
		if !found {
			t.Fatalf("Hermes ChangeSet missing %q: %#v", want, hermesDiff)
		}
	}
}

func TestPlanInstallCursorUsesFixedOwnerHooksWithoutModelConfiguration(t *testing.T) {
	service, target, _ := newInstallFixture(t)
	request := fixtureInstallRequest()
	request.Agents = []Agent{Cursor}
	request.OwnerBindingSlot = config.BindingRef{}
	delete(request.SecretInputs, OwnerBindingSecret)
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var hooks, mcpConfig, configuration []byte
	for _, operation := range plan.Operations {
		switch operation.Target {
		case "/Users/test/.cursor/hooks.json":
			hooks = operation.Content
		case "/Users/test/.cursor/mcp.json":
			mcpConfig = operation.Content
		case service.Paths.Config:
			configuration = operation.Content
		}
		if strings.Contains(strings.ToLower(operation.Target), "settings.json") {
			t.Fatalf("Cursor model settings targeted: %s", operation.Target)
		}
	}
	if !bytes.Contains(hooks, []byte("hook cursor beforeSubmitPrompt")) || !bytes.Contains(mcpConfig, []byte(`"mlink-memory"`)) || !bytes.Contains(mcpConfig, []byte(`"mcp"`)) || !bytes.Contains(configuration, []byte("cursor:")) || !bytes.Contains(configuration, []byte("space_id: personal-owner")) {
		t.Fatalf("hooks/mcp/config = %s\n%s\n%s", hooks, mcpConfig, configuration)
	}
	if target.writes != 0 {
		t.Fatalf("preview writes = %d", target.writes)
	}
}

func TestPlanInstallCreatesThreeSpacesAndStableOwner(t *testing.T) {
	service, _, _ := newInstallFixture(t)
	plan, err := service.PlanInstall(context.Background(), fixtureInstallRequest())
	if err != nil {
		t.Fatal(err)
	}
	configOperation := operationForTarget(t, plan, service.Paths.Config)
	var got config.Config
	if err := yaml.Unmarshal(configOperation.Content, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 2 || got.Principals["owner"].CanonicalUserID != "usr_owner_keliang" || len(got.Spaces) != 3 {
		t.Fatalf("config = %#v", got)
	}
	if !got.Spaces["personal-owner"].IncludeAgentShared || got.Spaces["hermes-groups"].IncludeAgentShared || got.Spaces["hermes-private"].IncludeAgentShared {
		t.Fatalf("spaces = %#v", got.Spaces)
	}
	connection := got.Connections["local"]
	if connection.TenantID != "" || connection.AgentID != "" || connection.UserID != "" || connection.IncludeAgentShared {
		t.Fatalf("legacy identity leaked into Connection: %#v", connection)
	}
	wantDiffs := map[string]string{
		"schema_version":               "2",
		"principal:owner":              "stable canonical owner",
		"binding:owner-feishu-union-1": "redacted union_id alias -> owner",
		"space:personal-owner":         "owner L1/L2/L3; Codex/Pi/owner Hermes",
		"space:hermes-private":         "per-user L1 only; no agent-shared",
		"space:hermes-groups":          "per-group L1 only; topics share group principal",
		"adapter:codex.route":          "fixed personal-owner",
		"adapter:pi.route":             "fixed personal-owner",
		"adapter:hermes.route":         "dynamic owner/private/group",
	}
	for path, after := range wantDiffs {
		found := false
		for _, diff := range configOperation.SemanticDiff {
			found = found || diff.Path == path && diff.After == after
		}
		if !found {
			t.Fatalf("MLink config ChangeSet missing %q -> %q: %#v", path, after, configOperation.SemanticDiff)
		}
	}
}

func TestPlanIdentityDoesNotDependOnTokenBindingOrIdentityKey(t *testing.T) {
	a, _, _ := newInstallFixture(t)
	b, _, _ := newInstallFixture(t)
	a.IdentityKey = bytes.Repeat([]byte{0x11}, 32)
	b.IdentityKey = bytes.Repeat([]byte{0x22}, 32)
	first := fixtureInstallRequest()
	second := fixtureInstallRequest()
	first.SecretInputs[MemoryCoreTokenSecret] = []byte("token-one")
	second.SecretInputs[MemoryCoreTokenSecret] = []byte("token-two")
	first.SecretInputs[OwnerBindingSecret] = []byte("on_old")
	second.SecretInputs[OwnerBindingSecret] = []byte("on_new")
	firstPlan, err := a.PlanInstall(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := b.PlanInstall(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if firstPlan.PlanID != secondPlan.PlanID {
		t.Fatalf("secret-dependent plans: %s %s", firstPlan.PlanID, secondPlan.PlanID)
	}
	firstJSON, _ := install.RenderJSON(firstPlan)
	if bytes.Contains(firstJSON, []byte("on_old")) || bytes.Contains(firstJSON, []byte("token-one")) {
		t.Fatal("secret leaked into ChangeSet")
	}
}

func TestPlanInstallSecretValueDoesNotAffectPlanIdentity(t *testing.T) {
	service, _, _ := newInstallFixture(t)
	first := fixtureInstallRequest()
	second := fixtureInstallRequest()
	second.SecretInputs[MemoryCoreTokenSecret] = []byte("different-secret")
	firstPlan, err := service.PlanInstall(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := service.PlanInstall(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if firstPlan.PlanID != secondPlan.PlanID {
		t.Fatalf("plan IDs differ by secret value: %q != %q", firstPlan.PlanID, secondPlan.PlanID)
	}
}

func TestApplyInstallStoresSecretAndAppliesExactPlan(t *testing.T) {
	service, target, secrets := newInstallFixture(t)
	request := fixtureInstallRequest()
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if got := string(secrets.values["connection/local/token"]); got != "memorycore-secret" {
		t.Fatalf("stored secret = %q", got)
	}
	if got := secrets.values["identity/hmac-key"]; len(got) != 32 {
		t.Fatalf("identity key length = %d", len(got))
	}
	if got := string(secrets.values["adapter/hermes/token"]); got != "hermes-grant-secret" {
		t.Fatalf("Hermes grant = %q", got)
	}
	if target.writes == 0 || target.runs != 2 {
		t.Fatalf("apply writes/runs = %d/%d", target.writes, target.runs)
	}
	ledger := service.Ledger.(*memoryLedger)
	if ledger.activePlanID != plan.PlanID || len(ledger.activeAgents) != 3 {
		t.Fatalf("installation manifest = %q %#v", ledger.activePlanID, ledger.activeAgents)
	}
	configData := target.files[service.Paths.Config].content
	if bytes.Contains(configData, []byte("memorycore-secret")) || !bytes.Contains(configData, []byte("keychain://dev.mlink/connection/local/token")) {
		t.Fatalf("non-secret config is unsafe: %s", configData)
	}
	if !bytes.Contains(configData, []byte("listen_address: 192.168.139.1:8097")) {
		t.Fatalf("Broker listen address missing: %s", configData)
	}
}

func TestApplyInstallPreservesExistingIdentityKey(t *testing.T) {
	service, _, secrets := newInstallFixture(t)
	existing := bytes.Repeat([]byte{0x31}, 32)
	secrets.values["identity/hmac-key"] = append([]byte(nil), existing...)
	service.IdentityKey = bytes.Repeat([]byte{0x42}, 32)
	request := fixtureInstallRequest()
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secrets.values["identity/hmac-key"], existing) {
		t.Fatal("reinstall rotated the stable identity key")
	}
}

func TestApplyInstallFailureRestoresPreviousSecret(t *testing.T) {
	service, target, secrets := newInstallFixture(t)
	secrets.values["connection/local/token"] = []byte("previous-secret")
	request := fixtureInstallRequest()
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	target.failAtWrite = 2
	if err := service.ApplyInstall(context.Background(), plan.PlanID, request); err == nil {
		t.Fatal("ApplyInstall() error = nil")
	}
	if got := string(secrets.values["connection/local/token"]); got != "previous-secret" {
		t.Fatalf("secret after rollback = %q", got)
	}
}

func TestApplyInstallRejectsWrongPlanBeforeSecretWrite(t *testing.T) {
	service, _, secrets := newInstallFixture(t)
	if err := service.ApplyInstall(context.Background(), "plan_wrong", fixtureInstallRequest()); !errors.Is(err, install.ErrPlanStale) {
		t.Fatalf("ApplyInstall() error = %v", err)
	}
	if secrets.puts != 0 {
		t.Fatalf("secret writes = %d", secrets.puts)
	}
}

func newInstallFixture(t *testing.T) (*Service, *memoryTarget, *memorySecrets) {
	t.Helper()
	paths, err := layout.FromHome("/Users/test", "/Users/test/projects/mlink/build/mlink")
	if err != nil {
		t.Fatal(err)
	}
	target := newMemoryTarget(map[string]memoryFile{
		paths.SourceExecutable:          {content: []byte("verified-binary"), mode: 0o700},
		"/Users/test/.codex/hooks.json": {content: []byte("{\"hooks\":{}}\n"), mode: 0o600},
		"/home/test/.hermes/config.yaml": {
			content: []byte("group_sessions_per_user: true\nmodel:\n  provider: llm.dedao\n  base_url: https://llm.example/v1\nagent:\n  disabled_toolsets: null\n  reasoning_effort: high\nmemory:\n  memory_enabled: true\n  user_profile_enabled: true\n  provider: hy-memory\n"),
			mode:    0o600,
		},
	})
	secrets := &memorySecrets{values: make(map[string][]byte)}
	return &Service{
		Paths:               paths,
		UID:                 501,
		Target:              target,
		Ledger:              newMemoryLedger(),
		Secrets:             secrets,
		HermesEndpoint:      "http://192.168.139.1:8097",
		HermesListenAddress: "192.168.139.1:8097",
		HermesGrantToken:    []byte("hermes-grant-secret"),
		IdentityKey:         bytes.Repeat([]byte{0x2a}, 32),
	}, target, secrets
}

func fixtureInstallRequest() InstallRequest {
	request := InstallRequest{
		Agents: []Agent{Codex, Pi, Hermes},
		Connection: config.Connection{
			ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-1",
			ProviderConfig: map[string]any{
				"base_url": "http://127.0.0.1:8096", "service_id": "default", "team_id": "personal",
			},
			TenantID: "personal", AgentID: "default", UserID: "user-local", IncludeAgentShared: true,
		},
		SecretInputs: map[string][]byte{MemoryCoreTokenSecret: []byte("memorycore-secret"), OwnerBindingSecret: []byte("on_owner")},
		OwnerSlug:    "keliang",
		OwnerBindingSlot: config.BindingRef{
			ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner",
			SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: config.BindingActive,
		},
		HermesMachine: "hermes-agent-env",
		HermesHome:    "/home/test/.hermes",
	}
	return request
}

func operationForTarget(t *testing.T, plan install.ChangeSet, target string) install.Operation {
	t.Helper()
	for _, operation := range plan.Operations {
		if operation.Target == target {
			return operation
		}
	}
	t.Fatalf("operation for %q is missing", target)
	return install.Operation{}
}

func assertPlanTargets(t *testing.T, plan install.ChangeSet, want []string) {
	t.Helper()
	got := make([]string, 0, len(plan.Operations))
	for _, operation := range plan.Operations {
		if strings.HasPrefix(operation.Target, "service:") {
			continue
		}
		got = append(got, operation.Target)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("targets =\n%s\nwant =\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
