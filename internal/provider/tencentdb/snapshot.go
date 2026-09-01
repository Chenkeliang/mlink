package tencentdb

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mlink/internal/install"
	"mlink/internal/panel"
	"mlink/internal/provider/lifecycle"
	"mlink/internal/workspacebackup"
)

const (
	SnapshotDriverVersion = "1"
	SnapshotHelperImage   = "docker.io/library/alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
)

type SnapshotDriver struct {
	Runner          install.CommandRunner
	Stream          install.StreamRunner
	Target          install.Target
	Metadata        MetadataClient
	InstanceID      string
	CoreContainer   string
	CoreVolume      string
	HubContainer    string
	KnowledgeVolume string
}

var _ lifecycle.SnapshotDriver = (*SnapshotDriver)(nil)

type hubSnapshotInspect struct {
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status string `json:"Status"`
	} `json:"State"`
	Mounts []struct {
		Name        string `json:"Name"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
}

func (driver SnapshotDriver) Detect(ctx context.Context) (lifecycle.SnapshotSource, error) {
	if driver.Runner == nil || strings.TrimSpace(driver.InstanceID) == "" {
		return lifecycle.SnapshotSource{}, errors.New("TencentDB snapshot runner and instance are required")
	}
	coreContainer, coreVolume, hubContainer, knowledgeVolume := driver.names()
	core, err := driver.inspectCore(ctx, coreContainer)
	if err != nil {
		return lifecycle.SnapshotSource{}, errors.New("inspect MemoryCore snapshot source")
	}
	if core.Config.Image != MemoryCoreImageReference {
		return lifecycle.SnapshotSource{}, errors.New("MemoryCore snapshot source image is incompatible")
	}
	coreMount := false
	for _, mount := range core.Mounts {
		coreMount = coreMount || mount.Name == coreVolume && mount.Destination == "/data/tdai-memory"
	}
	if !coreMount {
		return lifecycle.SnapshotSource{}, errors.New("MemoryCore snapshot source has no compatible data volume")
	}
	owned := coreContainer == MemoryCoreContainerName && coreVolume == MemoryCoreVolumeName && core.Config.Labels["dev.mlink.component"] == "memory-core"
	source := lifecycle.SnapshotSource{
		ProviderID: providerID, DriverVersion: SnapshotDriverVersion, InstanceID: driver.InstanceID,
		Owned: owned, CoreContainer: coreContainer, CoreImage: core.Config.Image, CoreRunning: core.State.Status == "running",
		Volumes: []lifecycle.SnapshotVolume{{Kind: workspacebackup.VolumeCore, Name: coreVolume}},
	}
	hub, err := driver.inspectHub(ctx, hubContainer)
	if err == nil {
		if hub.Config.Image != panel.ImageReference || hub.Config.Labels["dev.mlink.component"] != "memory-hub" {
			return lifecycle.SnapshotSource{}, errors.New("Memory Hub snapshot source is incompatible")
		}
		mounted := false
		for _, mount := range hub.Mounts {
			mounted = mounted || mount.Name == knowledgeVolume && mount.Destination == "/data/knowledge"
		}
		if !mounted {
			return lifecycle.SnapshotSource{}, errors.New("Memory Hub snapshot source has no compatible Knowledge volume")
		}
		source.HubContainer, source.HubImage, source.HubRunning = hubContainer, hub.Config.Image, hub.State.Status == "running"
		source.Volumes = append(source.Volumes, lifecycle.SnapshotVolume{Kind: workspacebackup.VolumeKnowledge, Name: knowledgeVolume})
	}
	return source, nil
}

func (driver SnapshotDriver) PlanBackup(ctx context.Context, request lifecycle.BackupRequest) (install.ChangeSet, error) {
	if request.ProviderID != providerID || driver.Target == nil {
		return install.ChangeSet{}, errors.New("valid TencentDB backup request and target are required")
	}
	source, err := driver.Detect(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	if !source.Owned && !request.ApproveExternalSource {
		return install.ChangeSet{}, errors.New("compatible external MemoryCore requires explicit snapshot approval")
	}
	var resources []install.DesiredResource
	if source.HubContainer != "" && source.HubRunning {
		resources = append(resources, stopSnapshotResource("dev.mlink.snapshot.hub", source.HubContainer, "Knowledge"))
	}
	if source.CoreRunning {
		resources = append(resources, stopSnapshotResource("dev.mlink.snapshot.core", source.CoreContainer, "MemoryCore"))
	}
	if len(resources) == 0 {
		resources = append(resources, install.DesiredResource{
			OwnerID: "dev.mlink.snapshot", Target: "service:snapshot-ready:" + source.InstanceID, Action: install.ActionUnchanged,
			SemanticDiff: []install.SemanticDiff{{Path: "snapshot.source", Before: "stopped", After: "ready for read-only snapshot"}},
		})
	}
	plan, err := install.BuildChangeSet(driver.Target, resources)
	if err != nil {
		return install.ChangeSet{}, err
	}
	plan.SelectedConnection = source.InstanceID
	return plan, nil
}

func (driver SnapshotDriver) StreamSection(ctx context.Context, request lifecycle.BackupRequest, section workspacebackup.Section, destination io.Writer) (workspacebackup.VolumeManifest, error) {
	if request.ProviderID != providerID || destination == nil || driver.Stream == nil {
		return workspacebackup.VolumeManifest{}, errors.New("valid TencentDB snapshot stream request is required")
	}
	source, err := driver.Detect(ctx)
	if err != nil {
		return workspacebackup.VolumeManifest{}, err
	}
	if !source.Owned && !request.ApproveExternalSource {
		return workspacebackup.VolumeManifest{}, errors.New("compatible external MemoryCore requires explicit snapshot approval")
	}
	volume, kind, err := snapshotVolume(source, section)
	if err != nil {
		return workspacebackup.VolumeManifest{}, err
	}
	files, logicalBytes, err := driver.volumeStats(ctx, volume)
	if err != nil {
		return workspacebackup.VolumeManifest{}, err
	}
	digest := sha256.New()
	written := &countWriter{Writer: io.MultiWriter(destination, digest)}
	if err := driver.Stream.RunStream(ctx, snapshotTarCommand(volume), nil, written); err != nil {
		return workspacebackup.VolumeManifest{}, err
	}
	return workspacebackup.VolumeManifest{
		Kind: kind, Name: volume, LogicalBytes: logicalBytes, FileCount: files,
		SHA256: hex.EncodeToString(digest.Sum(nil)),
	}, nil
}

func (driver SnapshotDriver) PlanRestore(ctx context.Context, request lifecycle.RestoreRequest, provider workspacebackup.ProviderManifest) (install.ChangeSet, error) {
	if driver.Target == nil || request.ProviderID != providerID || provider.ProviderID != providerID || provider.DriverVersion != SnapshotDriverVersion ||
		provider.InstanceID == "" || provider.CoreImageDigest != MemoryCoreImageReference || provider.HubImageDigest != panel.ImageReference {
		return install.ChangeSet{}, errors.New("compatible TencentDB restore request and manifest are required")
	}
	if request.CoreContainer == "" || request.CoreVolume == "" || request.KnowledgeVolume == "" || request.CoreNetwork == "" {
		return install.ChangeSet{}, errors.New("complete TencentDB restore resource names are required")
	}
	if _, err := ValidateDeploymentEndpoint(request.Endpoint); err != nil {
		return install.ChangeSet{}, err
	}
	for _, object := range []struct{ kind, name string }{{"volume", request.CoreVolume}, {"volume", request.KnowledgeVolume}, {"network", request.CoreNetwork}} {
		if driver.objectExists(ctx, object.kind, object.name) {
			return install.ChangeSet{}, fmt.Errorf("restore target %s %q already exists", object.kind, object.name)
		}
	}
	if driver.objectExists(ctx, "container", request.CoreContainer) {
		return install.ChangeSet{}, fmt.Errorf("restore target container %q already exists", request.CoreContainer)
	}
	resources := []install.DesiredResource{
		createSnapshotObject("network", request.CoreNetwork, "memory-core"),
		createSnapshotObject("volume", request.CoreVolume, "memory-core"),
		createSnapshotObject("volume", request.KnowledgeVolume, "memory-hub"),
	}
	return install.BuildChangeSet(driver.Target, resources)
}

func (driver SnapshotDriver) ApplySection(ctx context.Context, request lifecycle.RestoreRequest, section workspacebackup.Section, source io.Reader) error {
	if driver.Stream == nil || source == nil {
		return errors.New("TencentDB restore stream dependencies are required")
	}
	volume := ""
	switch section {
	case workspacebackup.SectionCore:
		volume = request.CoreVolume
	case workspacebackup.SectionKnowledge:
		volume = request.KnowledgeVolume
	default:
		return errors.New("unsupported TencentDB restore section")
	}
	if volume == "" {
		return errors.New("TencentDB restore target volume is required")
	}
	return driver.Stream.RunStream(ctx, restoreTarCommand(volume), source, io.Discard)
}

func (driver SnapshotDriver) VerifyRestore(ctx context.Context, request lifecycle.RestoreRequest, manifest workspacebackup.Manifest) error {
	if driver.Metadata == nil || len(request.OwnerUserKey) == 0 {
		return errors.New("TencentDB restore metadata verification is unavailable")
	}
	control := manifest.ControlPlane
	owner, err := driver.Metadata.VerifyUser(ctx, request.OwnerUserKey)
	if err != nil || owner.UserID != control.OwnerUserID || owner.UserType != "normal" {
		return errors.New("restored Owner identity mismatch")
	}
	teams, err := driver.Metadata.ListTeams(ctx, request.OwnerUserKey, ListTeamsRequest{UserID: control.OwnerUserID, Limit: 100})
	if err != nil || !hasTeam(teams, control.OwnerTeamID, control.OwnerUserID) {
		return errors.New("restored Owner Team mismatch")
	}
	agents, err := driver.Metadata.ListAgents(ctx, request.OwnerUserKey, ListAgentsRequest{TeamID: control.OwnerTeamID, OwnerUserID: control.OwnerUserID, Limit: 100})
	if err != nil || !hasAgent(agents, control.OwnerAgentID, control.OwnerTeamID, control.OwnerUserID) {
		return errors.New("restored Owner Agent mismatch")
	}
	asset, err := driver.Metadata.GetAsset(ctx, request.OwnerUserKey, control.OwnerAssetID)
	if err != nil || asset.AssetID != control.OwnerAssetID || asset.TeamID != control.OwnerTeamID || asset.OwnerUserID != control.OwnerUserID {
		return errors.New("restored Owner Asset mismatch")
	}
	for _, mapping := range manifest.PrincipalAgents {
		if !hasAgent(agents, mapping.BackendAgentID, mapping.BackendTeamID, mapping.BackendUserID) {
			return errors.New("restored dynamic Agent mismatch")
		}
		asset, err := driver.Metadata.GetAsset(ctx, request.OwnerUserKey, mapping.BackendAssetID)
		if err != nil || asset.AssetID != mapping.BackendAssetID || asset.TeamID != mapping.BackendTeamID || asset.OwnerUserID != mapping.BackendUserID {
			return errors.New("restored dynamic Asset mismatch")
		}
	}
	return nil
}

func (driver SnapshotDriver) names() (string, string, string, string) {
	coreContainer, coreVolume := driver.CoreContainer, driver.CoreVolume
	hubContainer, knowledgeVolume := driver.HubContainer, driver.KnowledgeVolume
	if coreContainer == "" {
		coreContainer = MemoryCoreContainerName
	}
	if coreVolume == "" {
		coreVolume = MemoryCoreVolumeName
	}
	if hubContainer == "" {
		hubContainer = panel.ContainerName
	}
	if knowledgeVolume == "" {
		knowledgeVolume = panel.VolumeName
	}
	return coreContainer, coreVolume, hubContainer, knowledgeVolume
}

func (driver SnapshotDriver) inspectCore(ctx context.Context, name string) (coreInspect, error) {
	output, err := driver.Runner.Run(ctx, []string{"docker", "inspect", name}, nil)
	if err != nil {
		return coreInspect{}, err
	}
	var values []coreInspect
	if json.Unmarshal(output, &values) != nil || len(values) != 1 {
		return coreInspect{}, errors.New("invalid MemoryCore inspection")
	}
	return values[0], nil
}

func (driver SnapshotDriver) inspectHub(ctx context.Context, name string) (hubSnapshotInspect, error) {
	output, err := driver.Runner.Run(ctx, []string{"docker", "inspect", name}, nil)
	if err != nil {
		return hubSnapshotInspect{}, err
	}
	var values []hubSnapshotInspect
	if json.Unmarshal(output, &values) != nil || len(values) != 1 {
		return hubSnapshotInspect{}, errors.New("invalid Memory Hub inspection")
	}
	return values[0], nil
}

func (driver SnapshotDriver) volumeStats(ctx context.Context, volume string) (int, int64, error) {
	output, err := driver.Runner.Run(ctx, []string{
		"docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", volume + ":/source:ro", SnapshotHelperImage, "sh", "-c",
		`files=$(find /source -type f | wc -l); bytes=$(find /source -type f -exec stat -c %s {} + | awk '{s+=$1} END{print s+0}'); printf 'files=%s\nbytes=%s\n' "$files" "$bytes"`,
	}, nil)
	if err != nil {
		return 0, 0, errors.New("inspect snapshot volume statistics")
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok {
			values[key] = value
		}
	}
	files, filesErr := strconv.Atoi(values["files"])
	logicalBytes, bytesErr := strconv.ParseInt(values["bytes"], 10, 64)
	if scanner.Err() != nil || filesErr != nil || bytesErr != nil || files < 0 || logicalBytes < 0 {
		return 0, 0, errors.New("invalid snapshot volume statistics")
	}
	return files, logicalBytes, nil
}

func (driver SnapshotDriver) objectExists(ctx context.Context, kind, name string) bool {
	command := []string{"docker", kind, "inspect", name}
	if kind == "container" {
		command = []string{"docker", "inspect", name}
	}
	_, err := driver.Runner.Run(ctx, command, nil)
	return err == nil
}

func stopSnapshotResource(ownerID, container, label string) install.DesiredResource {
	return install.DesiredResource{
		OwnerID: ownerID, Target: "service:snapshot-stop:" + container, Action: install.ActionService,
		Command: []string{"docker", "stop", container}, RollbackCommand: []string{"docker", "start", container},
		SemanticDiff: []install.SemanticDiff{{Path: "snapshot:" + strings.ToLower(label), Before: "running", After: "stopped for consistent read-only snapshot"}},
	}
}

func createSnapshotObject(kind, name, component string) install.DesiredResource {
	command := []string{"docker", kind, "create", "--label", "dev.mlink.component=" + component, name}
	if kind == "volume" {
		command = []string{"docker", "volume", "create", "--label", "dev.mlink.component=" + component, name}
	}
	return install.DesiredResource{
		OwnerID: "dev.mlink.restore." + kind, Target: "service:restore-" + kind + ":" + name, Action: install.ActionService,
		Command: command, RollbackCommand: []string{"docker", kind, "rm", name},
		SemanticDiff: []install.SemanticDiff{{Path: "restore:" + kind, Before: "absent", After: name}},
	}
}

func snapshotVolume(source lifecycle.SnapshotSource, section workspacebackup.Section) (string, workspacebackup.VolumeKind, error) {
	want := workspacebackup.VolumeCore
	if section == workspacebackup.SectionKnowledge {
		want = workspacebackup.VolumeKnowledge
	} else if section != workspacebackup.SectionCore {
		return "", "", errors.New("unsupported snapshot section")
	}
	for _, volume := range source.Volumes {
		if volume.Kind == want {
			return volume.Name, want, nil
		}
	}
	return "", "", errors.New("snapshot volume is unavailable")
}

func snapshotTarCommand(volume string) []string {
	return []string{"docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", volume + ":/source:ro", SnapshotHelperImage, "tar", "-C", "/source", "-cf", "-", "."}
}

func restoreTarCommand(volume string) []string {
	return []string{"docker", "run", "--rm", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-i", "-v", volume + ":/target", SnapshotHelperImage, "tar", "-C", "/target", "-xf", "-"}
}

type countWriter struct {
	Writer io.Writer
	Bytes  int64
}

func (writer *countWriter) Write(value []byte) (int, error) {
	count, err := writer.Writer.Write(value)
	writer.Bytes += int64(count)
	return count, err
}

func hasTeam(values []Team, teamID, ownerID string) bool {
	for _, value := range values {
		if value.TeamID == teamID && value.OwnerUserID == ownerID {
			return true
		}
	}
	return false
}
func hasAgent(values []Agent, agentID, teamID, ownerID string) bool {
	for _, value := range values {
		if value.AgentID == agentID && value.TeamID == teamID && value.OwnerUserID == ownerID {
			return true
		}
	}
	return false
}
