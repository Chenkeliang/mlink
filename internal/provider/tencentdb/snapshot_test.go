package tencentdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"mlink/internal/install"
	"mlink/internal/panel"
	"mlink/internal/provider/lifecycle"
	"mlink/internal/workspacebackup"
)

type snapshotRunner struct {
	coreInspect []byte
	hubInspect  []byte
	objects     map[string]bool
	commands    [][]string
	capacity    []byte
}

func (runner *snapshotRunner) Run(_ context.Context, args []string, _ io.Reader) ([]byte, error) {
	runner.commands = append(runner.commands, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	switch joined {
	case "docker inspect " + MemoryCoreContainerName:
		if runner.coreInspect == nil {
			return nil, errors.New("not found")
		}
		return append([]byte(nil), runner.coreInspect...), nil
	case "docker inspect " + panel.ContainerName:
		if runner.hubInspect == nil {
			return nil, errors.New("not found")
		}
		return append([]byte(nil), runner.hubInspect...), nil
	case "docker volume inspect " + MemoryCoreVolumeName, "docker volume inspect " + panel.VolumeName,
		"docker network inspect " + MemoryCoreNetworkName:
		if runner.objects[joined] {
			return []byte("[]"), nil
		}
		return nil, errors.New("not found")
	default:
		if strings.Contains(joined, "find /source") {
			return []byte("files=3\nbytes=12\n"), nil
		}
		if strings.Contains(joined, "df -Pk /") {
			if runner.capacity != nil {
				return append([]byte(nil), runner.capacity...), nil
			}
			return []byte("1099511627776"), nil
		}
		if strings.Contains(joined, "createHash('sha256')") {
			return []byte(strings.Repeat("b", 64)), nil
		}
		return []byte("ok"), nil
	}
}

type snapshotStreamRunner struct {
	commands [][]string
	input    bytes.Buffer
	content  []byte
}

type snapshotEnvironmentRunner struct {
	argv        []string
	environment map[string][]byte
	commands    [][]string
}

func (runner *snapshotEnvironmentRunner) RunEnvironment(_ context.Context, argv []string, environment map[string][]byte, _ io.Reader) ([]byte, error) {
	runner.argv = append([]string(nil), argv...)
	runner.commands = append(runner.commands, append([]string(nil), argv...))
	runner.environment = map[string][]byte{}
	for name, value := range environment {
		runner.environment[name] = append([]byte(nil), value...)
	}
	return []byte("container-id"), nil
}

type snapshotMetadata struct {
	owner  User
	teams  []Team
	agents []Agent
	assets map[string]Asset
}

type pagedSnapshotMetadata struct {
	*snapshotMetadata
	all   []Agent
	calls int
}

func (metadata *pagedSnapshotMetadata) ListAgents(_ context.Context, _ []byte, request ListAgentsRequest) ([]Agent, error) {
	metadata.calls++
	if request.Offset >= len(metadata.all) {
		return nil, nil
	}
	end := request.Offset + request.Limit
	if end > len(metadata.all) {
		end = len(metadata.all)
	}
	return append([]Agent(nil), metadata.all[request.Offset:end]...), nil
}

func (*snapshotMetadata) InitAdmin(context.Context, InitAdminRequest) (UserCredential, error) {
	return UserCredential{}, errors.New("unexpected metadata mutation")
}
func (*snapshotMetadata) CreateUser(context.Context, []byte, CreateUserRequest) (UserCredential, error) {
	return UserCredential{}, errors.New("unexpected metadata mutation")
}
func (metadata *snapshotMetadata) VerifyUser(context.Context, []byte) (User, error) {
	return metadata.owner, nil
}
func (*snapshotMetadata) CreateTeam(context.Context, []byte, CreateTeamRequest) (Team, error) {
	return Team{}, errors.New("unexpected metadata mutation")
}
func (metadata *snapshotMetadata) ListTeams(context.Context, []byte, ListTeamsRequest) ([]Team, error) {
	return append([]Team(nil), metadata.teams...), nil
}
func (*snapshotMetadata) CreateAgent(context.Context, []byte, CreateAgentRequest) (Agent, error) {
	return Agent{}, errors.New("unexpected metadata mutation")
}
func (metadata *snapshotMetadata) ListAgents(context.Context, []byte, ListAgentsRequest) ([]Agent, error) {
	return append([]Agent(nil), metadata.agents...), nil
}
func (metadata *snapshotMetadata) GetAsset(_ context.Context, _ []byte, assetID string) (Asset, error) {
	value, exists := metadata.assets[assetID]
	if !exists {
		return Asset{}, errors.New("missing")
	}
	return value, nil
}
func (*snapshotMetadata) InstanceQuota(context.Context, []byte) (InstanceQuota, error) {
	return InstanceQuota{}, nil
}

func (runner *snapshotStreamRunner) RunStream(_ context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	runner.commands = append(runner.commands, append([]string(nil), args...))
	if stdin != nil {
		_, _ = io.Copy(&runner.input, stdin)
	}
	if stdout != nil && len(runner.content) != 0 {
		_, _ = stdout.Write(runner.content)
	}
	return nil
}

func ownedHubInspect() []byte {
	return []byte(`[{"Config":{"Image":"` + panel.ImageReference + `","Labels":{"dev.mlink.component":"memory-hub"}},"State":{"Status":"running"},"Mounts":[{"Name":"` + panel.VolumeName + `","Destination":"/data/knowledge"}]}]`)
}

func TestSnapshotDetectsOwnedFormalCoreAndKnowledge(t *testing.T) {
	runner := &snapshotRunner{coreInspect: ownedCoreInspect("running", MemoryCoreImageReference), hubInspect: ownedHubInspect()}
	driver := SnapshotDriver{Runner: runner, Target: install.LocalTarget{Runner: runner}, InstanceID: "default"}
	source, err := driver.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !source.Owned || source.ProviderID != providerID || source.InstanceID != "default" || len(source.Volumes) != 2 ||
		source.Volumes[0].Name != MemoryCoreVolumeName || source.Volumes[1].Name != panel.VolumeName {
		t.Fatalf("source = %#v", source)
	}
}

func TestSnapshotBackupPlanRequiresApprovalForCompatibleExternalCore(t *testing.T) {
	external := ownedCoreInspect("running", MemoryCoreImageReference)
	external = bytes.ReplaceAll(external, []byte(`"dev.mlink.component":"memory-core"`), []byte(`"other.component":"memory-core"`))
	runner := &snapshotRunner{coreInspect: external}
	driver := SnapshotDriver{Runner: runner, Target: install.LocalTarget{Runner: runner}, InstanceID: "default"}
	if _, err := driver.PlanBackup(context.Background(), lifecycle.BackupRequest{ProviderID: providerID}); err == nil {
		t.Fatal("external source was accepted without approval")
	}
	plan, err := driver.PlanBackup(context.Background(), lifecycle.BackupRequest{ProviderID: providerID, ApproveExternalSource: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := snapshotPlanCommands(plan)
	if !strings.Contains(joined, "docker stop "+MemoryCoreContainerName) || strings.Contains(joined, "volume rm") {
		t.Fatalf("backup Plan = %s", joined)
	}
}

func TestSnapshotRejectsIncompatibleCoreMountOrImage(t *testing.T) {
	for name, inspect := range map[string][]byte{
		"image":  bytes.ReplaceAll(ownedCoreInspect("running", MemoryCoreImageReference), []byte(MemoryCoreImageReference), []byte("other/image:latest")),
		"volume": bytes.ReplaceAll(ownedCoreInspect("running", MemoryCoreImageReference), []byte("/data/tdai-memory"), []byte("/wrong")),
	} {
		t.Run(name, func(t *testing.T) {
			runner := &snapshotRunner{coreInspect: inspect}
			_, err := (SnapshotDriver{Runner: runner, Target: install.LocalTarget{Runner: runner}, InstanceID: "default"}).Detect(context.Background())
			if err == nil {
				t.Fatal("Detect() error = nil")
			}
		})
	}
}

func TestSnapshotStreamsReadOnlyPinnedVolumeTar(t *testing.T) {
	runner := &snapshotRunner{coreInspect: ownedCoreInspect("exited", MemoryCoreImageReference), hubInspect: ownedHubInspect()}
	stream := &snapshotStreamRunner{content: []byte("tar-stream")}
	driver := SnapshotDriver{Runner: runner, Stream: stream, Target: install.LocalTarget{Runner: runner}, InstanceID: "default"}
	var output bytes.Buffer
	manifest, err := driver.StreamSection(context.Background(), lifecycle.BackupRequest{ProviderID: providerID}, workspacebackup.SectionCore, &output)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "tar-stream" || manifest.Kind != workspacebackup.VolumeCore || manifest.Name != MemoryCoreVolumeName ||
		manifest.FileCount != 3 || manifest.LogicalBytes != 12 || len(manifest.SHA256) != 64 {
		t.Fatalf("manifest/output = %#v/%q", manifest, output.String())
	}
	joined := strings.Join(stream.commands[0], " ")
	for _, want := range []string{"--network none", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges", "-v " + MemoryCoreVolumeName + ":/source:ro", SnapshotHelperImage, "tar -C /source -cf - ."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stream command missing %q: %s", want, joined)
		}
	}
	validationCommands := strings.Join(flattenCommands(runner.commands), "\n")
	if !strings.Contains(validationCommands, "PRAGMA quick_check") || strings.Contains(validationCommands, "--tmpfs") {
		t.Fatalf("SQLite validation command is incomplete: %s", validationCommands)
	}
}

func flattenCommands(commands [][]string) []string {
	result := make([]string, len(commands))
	for index, command := range commands {
		result[index] = strings.Join(command, " ")
	}
	return result
}

func TestSnapshotRestorePlanCreatesOnlyEmptyFormalResourcesAndApplyStreamsTar(t *testing.T) {
	runner := &snapshotRunner{objects: map[string]bool{}}
	stream := &snapshotStreamRunner{}
	driver := SnapshotDriver{Runner: runner, Stream: stream, Target: install.LocalTarget{Runner: runner}, InstanceID: "default"}
	request := lifecycle.RestoreRequest{
		ProviderID: providerID, CoreContainer: MemoryCoreContainerName, CoreVolume: MemoryCoreVolumeName,
		KnowledgeVolume: panel.VolumeName, CoreNetwork: MemoryCoreNetworkName, Endpoint: "http://127.0.0.1:8420",
	}
	provider := workspacebackup.ProviderManifest{ProviderID: providerID, DriverVersion: SnapshotDriverVersion, InstanceID: "default", CoreImageDigest: imageDigestReference(MemoryCoreImageReference), HubImageDigest: imageDigestReference(panel.ImageReference)}
	plan, err := driver.PlanRestore(context.Background(), request, provider)
	if err != nil {
		t.Fatal(err)
	}
	joined := snapshotPlanCommands(plan)
	if !strings.Contains(joined, "docker volume create") || !strings.Contains(joined, MemoryCoreVolumeName) || !strings.Contains(joined, panel.VolumeName) || strings.Contains(joined, "volume rm") {
		t.Fatalf("restore Plan = %s", joined)
	}
	if !strings.Contains(joined, "--label dev.mlink.component=memory-core "+MemoryCoreVolumeName) ||
		!strings.Contains(joined, "--label dev.mlink.component=memory-hub "+panel.VolumeName) {
		t.Fatalf("restore ownership labels = %s", joined)
	}
	if err := driver.ApplySection(context.Background(), request, workspacebackup.SectionCore, strings.NewReader("restored-tar")); err != nil {
		t.Fatal(err)
	}
	if stream.input.String() != "restored-tar" || !strings.Contains(strings.Join(stream.commands[0], " "), "-v "+MemoryCoreVolumeName+":/target") {
		t.Fatalf("restore stream/input = %#v/%q", stream.commands, stream.input.String())
	}
}

func TestSnapshotRestorePlanRejectsInsufficientCapacityBeforeCreatingResources(t *testing.T) {
	runner := &snapshotRunner{objects: map[string]bool{}, capacity: []byte("100")}
	driver := SnapshotDriver{Runner: runner, Target: install.LocalTarget{Runner: runner}, InstanceID: "default"}
	request := lifecycle.RestoreRequest{ProviderID: providerID, CoreContainer: MemoryCoreContainerName, CoreVolume: MemoryCoreVolumeName, KnowledgeVolume: panel.VolumeName, CoreNetwork: MemoryCoreNetworkName, Endpoint: "http://127.0.0.1:8420"}
	provider := workspacebackup.ProviderManifest{
		ProviderID: providerID, DriverVersion: SnapshotDriverVersion, InstanceID: "default",
		CoreImageDigest: imageDigestReference(MemoryCoreImageReference), HubImageDigest: imageDigestReference(panel.ImageReference),
		Volumes: []workspacebackup.VolumeManifest{{Kind: workspacebackup.VolumeCore, Name: MemoryCoreVolumeName, LogicalBytes: 80, FileCount: 1, SHA256: strings.Repeat("a", 64)}},
	}
	if _, err := driver.PlanRestore(context.Background(), request, provider); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("capacity error = %v", err)
	}
}

func TestSnapshotRestoreRefusesExistingTargetVolume(t *testing.T) {
	runner := &snapshotRunner{objects: map[string]bool{"docker volume inspect " + MemoryCoreVolumeName: true}}
	driver := SnapshotDriver{Runner: runner, Target: install.LocalTarget{Runner: runner}, InstanceID: "default"}
	request := lifecycle.RestoreRequest{ProviderID: providerID, CoreContainer: MemoryCoreContainerName, CoreVolume: MemoryCoreVolumeName, KnowledgeVolume: panel.VolumeName, CoreNetwork: MemoryCoreNetworkName, Endpoint: "http://127.0.0.1:8420"}
	provider := workspacebackup.ProviderManifest{ProviderID: providerID, DriverVersion: SnapshotDriverVersion, InstanceID: "default", CoreImageDigest: imageDigestReference(MemoryCoreImageReference), HubImageDigest: imageDigestReference(panel.ImageReference)}
	if _, err := driver.PlanRestore(context.Background(), request, provider); err == nil {
		t.Fatal("existing target volume was accepted")
	}
}

func TestSnapshotVerifyRestoreRequiresExactFixedAndDynamicIdentity(t *testing.T) {
	metadata := &snapshotMetadata{
		owner: User{UserID: "usr-owner", UserType: "normal"},
		teams: []Team{{TeamID: "team-owner", OwnerUserID: "usr-owner"}},
		agents: []Agent{
			{AgentID: "agt-owner", TeamID: "team-owner", OwnerUserID: "usr-owner"},
			{AgentID: "agt-dynamic", TeamID: "team-owner", OwnerUserID: "usr-owner"},
		},
		assets: map[string]Asset{
			"asset-owner":   {AssetID: "asset-owner", TeamID: "team-owner", OwnerUserID: "usr-owner"},
			"asset-dynamic": {AssetID: "asset-dynamic", TeamID: "team-owner", OwnerUserID: "usr-owner"},
		},
	}
	driver := SnapshotDriver{Metadata: metadata}
	manifest := fixtureManifestForSnapshot()
	request := lifecycle.RestoreRequest{OwnerUserKey: []byte("owner-key")}
	if err := driver.VerifyRestore(context.Background(), request, manifest); err != nil {
		t.Fatal(err)
	}
	manifest.PrincipalAgents[0].BackendAgentID = "agt-other"
	if err := driver.VerifyRestore(context.Background(), request, manifest); err == nil || !strings.Contains(err.Error(), "dynamic Agent") {
		t.Fatalf("dynamic mismatch error = %v", err)
	}
}

func TestSnapshotVerifyStartsOfficialCoreWithSecretsOutsideArgv(t *testing.T) {
	metadata := &snapshotMetadata{
		owner: User{UserID: "usr-owner", UserType: "normal"}, teams: []Team{{TeamID: "team-owner", OwnerUserID: "usr-owner"}},
		agents: []Agent{{AgentID: "agt-owner", TeamID: "team-owner", OwnerUserID: "usr-owner"}, {AgentID: "agt-dynamic", TeamID: "team-owner", OwnerUserID: "usr-owner"}},
		assets: map[string]Asset{"asset-owner": {AssetID: "asset-owner", TeamID: "team-owner", OwnerUserID: "usr-owner"}, "asset-dynamic": {AssetID: "asset-dynamic", TeamID: "team-owner", OwnerUserID: "usr-owner"}},
	}
	environment := &snapshotEnvironmentRunner{}
	driver := SnapshotDriver{Metadata: metadata, Environment: environment, WaitHealthy: func(context.Context, string) error { return nil }}
	request := lifecycle.RestoreRequest{
		ProviderID: providerID, CoreContainer: MemoryCoreContainerName, CoreVolume: MemoryCoreVolumeName, CoreNetwork: MemoryCoreNetworkName,
		CoreConfigPath: "/Users/test/.mlink/memorycore/tdai-gateway.yaml", Endpoint: "http://127.0.0.1:8420",
		GatewayToken: []byte("gateway-secret"), LLMAPIKey: []byte("llm-secret"), OwnerUserKey: []byte("owner-key"),
	}
	if err := driver.VerifyRestore(context.Background(), request, fixtureManifestForSnapshot()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(environment.argv, " ")
	for _, secret := range []string{"gateway-secret", "llm-secret"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("restore argv leaked %q: %s", secret, joined)
		}
	}
	if string(environment.environment["TDAI_GATEWAY_API_KEY"]) != "gateway-secret" || string(environment.environment["TDAI_LLM_API_KEY"]) != "llm-secret" ||
		!strings.Contains(joined, MemoryCoreImageReference) || !strings.Contains(joined, "-e TDAI_GATEWAY_API_KEY") {
		t.Fatalf("restore command/environment = %s/%#v", joined, environment.environment)
	}
}

func TestSnapshotVerifyPaginatesDynamicAgentsToDeclaredLimit(t *testing.T) {
	base := &snapshotMetadata{
		owner: User{UserID: "usr-owner", UserType: "normal"}, teams: []Team{{TeamID: "team-owner", OwnerUserID: "usr-owner"}},
		assets: map[string]Asset{"asset-owner": {AssetID: "asset-owner", TeamID: "team-owner", OwnerUserID: "usr-owner"}},
	}
	manifest := fixtureManifestForSnapshot()
	manifest.PrincipalAgents = nil
	all := []Agent{{AgentID: "agt-owner", TeamID: "team-owner", OwnerUserID: "usr-owner"}}
	for index := 0; index < 125; index++ {
		fingerprint := fmt.Sprintf("prn_%026d", index)
		agentID := fmt.Sprintf("agt-dynamic-%03d", index)
		assetID := "chat_memory-team-owner-" + agentID
		marker := fmt.Sprintf(`{"mlink":{"principal_fingerprint":%q,"route_kind":"hermes-private","role":"dynamic-agent"}}`, fingerprint)
		all = append(all, Agent{AgentID: agentID, TeamID: "team-owner", OwnerUserID: "usr-owner", MetadataJSON: marker})
		base.assets[assetID] = Asset{AssetID: assetID, TeamID: "team-owner", OwnerUserID: "usr-owner"}
		manifest.PrincipalAgents = append(manifest.PrincipalAgents, workspacebackup.PrincipalAgentManifest{
			Fingerprint: fingerprint, RouteKind: "hermes-private", BackendUserID: "usr-owner", BackendTeamID: "team-owner", BackendAgentID: agentID, BackendAssetID: assetID,
		})
	}
	metadata := &pagedSnapshotMetadata{snapshotMetadata: base, all: all}
	driver := SnapshotDriver{Metadata: metadata}
	if err := driver.VerifyRestore(context.Background(), lifecycle.RestoreRequest{OwnerUserKey: []byte("owner-key")}, manifest); err != nil {
		t.Fatal(err)
	}
	if metadata.calls != 2 {
		t.Fatalf("Agent pagination calls = %d", metadata.calls)
	}
}

func fixtureManifestForSnapshot() workspacebackup.Manifest {
	return workspacebackup.Manifest{
		ControlPlane: workspacebackup.ControlPlaneManifest{
			OwnerUserID: "usr-owner", OwnerTeamID: "team-owner", OwnerAgentID: "agt-owner", OwnerAssetID: "asset-owner", DynamicAgentLimit: 500,
		},
		PrincipalAgents: []workspacebackup.PrincipalAgentManifest{{
			BackendUserID: "usr-owner", BackendTeamID: "team-owner", BackendAgentID: "agt-dynamic", BackendAssetID: "asset-dynamic",
		}},
	}
}

func snapshotPlanCommands(plan install.ChangeSet) string {
	var commands []string
	for _, operation := range plan.Operations {
		commands = append(commands, strings.Join(operation.Command, " "))
	}
	return strings.Join(commands, "\n")
}
