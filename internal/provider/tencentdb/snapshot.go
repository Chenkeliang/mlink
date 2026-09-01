package tencentdb

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
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
	Environment     install.EnvironmentRunner
	WaitHealthy     func(context.Context, string) error
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
	if err := driver.validateVolumeSQLite(ctx, volume); err != nil {
		return workspacebackup.VolumeManifest{}, err
	}
	files, logicalBytes, err := driver.volumeStats(ctx, volume)
	if err != nil {
		return workspacebackup.VolumeManifest{}, err
	}
	contentDigest, err := driver.volumeContentDigest(ctx, volume)
	if err != nil {
		return workspacebackup.VolumeManifest{}, err
	}
	written := &countWriter{Writer: destination}
	if err := driver.Stream.RunStream(ctx, snapshotTarCommand(volume), nil, written); err != nil {
		return workspacebackup.VolumeManifest{}, err
	}
	return workspacebackup.VolumeManifest{
		Kind: kind, Name: volume, LogicalBytes: logicalBytes, FileCount: files,
		SHA256: contentDigest,
	}, nil
}

func (driver SnapshotDriver) PlanRestore(ctx context.Context, request lifecycle.RestoreRequest, provider workspacebackup.ProviderManifest) (install.ChangeSet, error) {
	if driver.Target == nil || request.ProviderID != providerID || provider.ProviderID != providerID || provider.DriverVersion != SnapshotDriverVersion ||
		provider.InstanceID == "" || provider.CoreImageDigest != imageDigestReference(MemoryCoreImageReference) || provider.HubImageDigest != imageDigestReference(panel.ImageReference) {
		return install.ChangeSet{}, errors.New("compatible TencentDB restore request and manifest are required")
	}
	if request.CoreContainer == "" || request.CoreVolume == "" || request.KnowledgeVolume == "" || request.CoreNetwork == "" {
		return install.ChangeSet{}, errors.New("complete TencentDB restore resource names are required")
	}
	if _, err := ValidateDeploymentEndpoint(request.Endpoint); err != nil {
		return install.ChangeSet{}, err
	}
	if err := driver.verifyRestoreCapacity(ctx, provider.Volumes); err != nil {
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
		{
			OwnerID: "dev.mlink.memorycore.container", Target: "service:restore-runtime:" + request.CoreContainer, Action: install.ActionService,
			Command: []string{"true"}, RollbackCommand: []string{"docker", "rm", "-f", request.CoreContainer},
			SemanticDiff: []install.SemanticDiff{{Path: "memorycore.restore-runtime", Before: "absent", After: "official restored Core; secrets supplied through process environment"}},
		},
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
	if err := driver.Stream.RunStream(ctx, restoreTarCommand(volume), source, io.Discard); err != nil {
		return err
	}
	return driver.validateVolumeSQLite(ctx, volume)
}

func (driver SnapshotDriver) VerifyRestore(ctx context.Context, request lifecycle.RestoreRequest, manifest workspacebackup.Manifest) error {
	if driver.Metadata == nil || len(request.OwnerUserKey) == 0 {
		return errors.New("TencentDB restore metadata verification is unavailable")
	}
	if err := driver.verifyRestoredVolumes(ctx, request, manifest.Provider.Volumes); err != nil {
		return err
	}
	started := false
	if driver.Environment != nil {
		if err := driver.startRestoredCore(ctx, request); err != nil {
			return err
		}
		started = true
	}
	fail := func(message string) error {
		if started && driver.Runner != nil {
			_, _ = driver.Runner.Run(context.Background(), []string{"docker", "rm", "-f", request.CoreContainer}, nil)
		}
		return errors.New(message)
	}
	control := manifest.ControlPlane
	owner, err := driver.Metadata.VerifyUser(ctx, request.OwnerUserKey)
	if err != nil || owner.UserID != control.OwnerUserID || owner.UserType != "normal" {
		return fail("restored Owner identity mismatch")
	}
	teams, err := driver.Metadata.ListTeams(ctx, request.OwnerUserKey, ListTeamsRequest{UserID: control.OwnerUserID, Limit: 100})
	if err != nil || !hasTeam(teams, control.OwnerTeamID, control.OwnerUserID) {
		return fail("restored Owner Team mismatch")
	}
	agents, err := driver.listAllRestoreAgents(ctx, request.OwnerUserKey, control)
	if err != nil || !hasAgent(agents, control.OwnerAgentID, control.OwnerTeamID, control.OwnerUserID) {
		return fail("restored Owner Agent mismatch")
	}
	asset, err := driver.Metadata.GetAsset(ctx, request.OwnerUserKey, control.OwnerAssetID)
	if err != nil || asset.AssetID != control.OwnerAssetID || asset.TeamID != control.OwnerTeamID || asset.OwnerUserID != control.OwnerUserID {
		return fail("restored Owner Asset mismatch")
	}
	for _, mapping := range manifest.PrincipalAgents {
		agent, found := findAgent(agents, mapping.BackendAgentID, mapping.BackendTeamID, mapping.BackendUserID)
		if !found || mapping.Fingerprint != "" && !agentMatchesPrincipalManifest(agent, mapping) {
			return fail("restored dynamic Agent mismatch")
		}
		asset, err := driver.Metadata.GetAsset(ctx, request.OwnerUserKey, mapping.BackendAssetID)
		if err != nil || asset.AssetID != mapping.BackendAssetID || asset.TeamID != mapping.BackendTeamID || asset.OwnerUserID != mapping.BackendUserID {
			return fail("restored dynamic Asset mismatch")
		}
	}
	return nil
}

func (driver SnapshotDriver) listAllRestoreAgents(ctx context.Context, ownerKey []byte, control workspacebackup.ControlPlaneManifest) ([]Agent, error) {
	const pageSize = 100
	maximum := control.DynamicAgentLimit + 1
	if maximum <= 1 || maximum > 10_001 {
		return nil, errors.New("restored dynamic Agent limit is invalid")
	}
	result := make([]Agent, 0, min(maximum, pageSize))
	seen := map[string]bool{}
	for offset := 0; offset < maximum; offset += pageSize {
		page, err := driver.Metadata.ListAgents(ctx, ownerKey, ListAgentsRequest{TeamID: control.OwnerTeamID, OwnerUserID: control.OwnerUserID, Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, agent := range page {
			if agent.AgentID == "" || seen[agent.AgentID] {
				return nil, errors.New("restored Agent listing is invalid")
			}
			seen[agent.AgentID] = true
			result = append(result, agent)
			if len(result) > maximum {
				return nil, errors.New("restored Agent listing exceeds declared capacity")
			}
		}
		if len(page) < pageSize {
			return result, nil
		}
	}
	return nil, errors.New("restored Agent listing exceeded pagination bound")
}

func findAgent(values []Agent, agentID, teamID, ownerID string) (Agent, bool) {
	for _, value := range values {
		if value.AgentID == agentID && value.TeamID == teamID && value.OwnerUserID == ownerID {
			return value, true
		}
	}
	return Agent{}, false
}

func agentMatchesPrincipalManifest(agent Agent, mapping workspacebackup.PrincipalAgentManifest) bool {
	var document struct {
		MLink struct {
			Fingerprint string `json:"principal_fingerprint"`
			RouteKind   string `json:"route_kind"`
			Role        string `json:"role"`
		} `json:"mlink"`
	}
	return json.Unmarshal([]byte(agent.MetadataJSON), &document) == nil && document.MLink.Role == "dynamic-agent" &&
		document.MLink.Fingerprint == mapping.Fingerprint && document.MLink.RouteKind == mapping.RouteKind
}

func (driver SnapshotDriver) validateVolumeSQLite(ctx context.Context, volume string) error {
	if driver.Runner == nil || volume == "" {
		return errors.New("snapshot SQLite validation dependencies are required")
	}
	script := `const fs=require('fs'),path=require('path'),sqlite=require('node:sqlite');let n=0;function walk(p){for(const e of fs.readdirSync(p,{withFileTypes:true})){const f=path.join(p,e.name);if(e.isDirectory())walk(f);else if(/\.(db|sqlite|sqlite3)$/.test(e.name)){const t='/tmp/db-'+n++;fs.copyFileSync(f,t);for(const s of ['-wal','-shm'])if(fs.existsSync(f+s))fs.copyFileSync(f+s,t+s);const d=new sqlite.DatabaseSync(t,{readOnly:true});const r=d.prepare('PRAGMA quick_check').all();d.close();for(const s of ['', '-wal','-shm'])try{fs.unlinkSync(t+s)}catch{};if(r.length!==1||r[0].quick_check!=='ok')throw new Error('quick_check');}}}walk('/source');`
	_, err := driver.Runner.Run(ctx, []string{
		"docker", "run", "--rm", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", volume + ":/source:ro", MemoryCoreImageReference, "node", "--experimental-sqlite", "-e", script,
	}, nil)
	if err != nil {
		return errors.New("snapshot SQLite quick_check failed")
	}
	return nil
}

func (driver SnapshotDriver) verifyRestoredVolumes(ctx context.Context, request lifecycle.RestoreRequest, volumes []workspacebackup.VolumeManifest) error {
	if len(volumes) == 0 {
		return nil
	}
	if driver.Stream == nil {
		return errors.New("restored volume verification stream is unavailable")
	}
	for _, expected := range volumes {
		volume := request.CoreVolume
		if expected.Kind == workspacebackup.VolumeKnowledge {
			volume = request.KnowledgeVolume
		}
		if volume == "" {
			return errors.New("restored volume target is missing")
		}
		files, logicalBytes, err := driver.volumeStats(ctx, volume)
		if err != nil || files != expected.FileCount || logicalBytes != expected.LogicalBytes {
			return errors.New("restored volume statistics differ from manifest")
		}
		contentDigest, err := driver.volumeContentDigest(ctx, volume)
		if err != nil || contentDigest != expected.SHA256 {
			return errors.New("restored volume content hash differs from manifest")
		}
		if err := driver.validateVolumeSQLite(ctx, volume); err != nil {
			return err
		}
	}
	return nil
}

func (driver SnapshotDriver) volumeContentDigest(ctx context.Context, volume string) (string, error) {
	if driver.Runner == nil || volume == "" {
		return "", errors.New("volume content fingerprint dependencies are required")
	}
	script := `const fs=require('fs'),path=require('path'),crypto=require('crypto');const files=[];function walk(p){for(const e of fs.readdirSync(p,{withFileTypes:true})){const f=path.join(p,e.name);if(e.isDirectory())walk(f);else if(e.isFile())files.push(f);}}walk('/source');files.sort();(async()=>{const h=crypto.createHash('sha256');for(const f of files){const r=path.relative('/source',f),s=fs.statSync(f);h.update(r);h.update('\0');h.update(String(s.size));h.update('\0');for await(const chunk of fs.createReadStream(f))h.update(chunk);}process.stdout.write(h.digest('hex'));})().catch(()=>process.exit(1));`
	output, err := driver.Runner.Run(ctx, []string{
		"docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", volume + ":/source:ro", MemoryCoreImageReference, "node", "-e", script,
	}, nil)
	value := strings.TrimSpace(string(output))
	if err != nil || len(value) != 64 {
		return "", errors.New("compute canonical volume content fingerprint")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("canonical volume content fingerprint is invalid")
	}
	return value, nil
}

func (driver SnapshotDriver) verifyRestoreCapacity(ctx context.Context, volumes []workspacebackup.VolumeManifest) error {
	var logicalBytes int64
	for _, volume := range volumes {
		if volume.LogicalBytes < 0 || logicalBytes > (1<<62)-volume.LogicalBytes {
			return errors.New("restore volume size is invalid")
		}
		logicalBytes += volume.LogicalBytes
	}
	if logicalBytes == 0 {
		return nil
	}
	if logicalBytes > (1<<62)-1 {
		return errors.New("restore volume size exceeds safe headroom calculation")
	}
	output, err := driver.Runner.Run(ctx, []string{
		"docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		SnapshotHelperImage, "sh", "-c", `df -Pk / | awk 'NR==2 {printf "%.0f", $4*1024}'`,
	}, nil)
	available, parseErr := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || parseErr != nil || available < logicalBytes*2 {
		return errors.New("restore target lacks required capacity and rollback headroom")
	}
	return nil
}

func (driver SnapshotDriver) startRestoredCore(ctx context.Context, request lifecycle.RestoreRequest) error {
	if driver.Environment == nil || request.CoreContainer == "" || request.CoreVolume == "" || request.CoreNetwork == "" ||
		!filepath.IsAbs(request.CoreConfigPath) || len(request.GatewayToken) == 0 || len(request.LLMAPIKey) == 0 {
		return errors.New("complete protected restored Core runtime is required")
	}
	endpoint, local, err := deploymentEndpoint(request.Endpoint)
	if err != nil || !local || endpoint.Scheme != "http" || endpoint.Port() == "" || endpoint.Path != "" {
		return errors.New("restored Core verification endpoint must be an explicit loopback HTTP port")
	}
	command := []string{
		"docker", "run", "-d", "--name", request.CoreContainer, "--restart", "unless-stopped",
		"--label", "dev.mlink.component=memory-core", "--network", request.CoreNetwork,
		"-p", "127.0.0.1:" + endpoint.Port() + ":8420", "-v", request.CoreVolume + ":/data/tdai-memory",
		"-v", request.CoreConfigPath + ":/data/config/tdai-gateway.yaml:ro",
		"-e", "TDAI_GATEWAY_API_KEY", "-e", "TDAI_LLM_API_KEY",
		"-e", "TDAI_GATEWAY_PORT=8420", "-e", "TDAI_GATEWAY_HOST=0.0.0.0", "-e", "TDAI_DATA_DIR=/data/tdai-memory",
		MemoryCoreImageReference,
	}
	if _, err := driver.Environment.RunEnvironment(ctx, command, map[string][]byte{
		"TDAI_GATEWAY_API_KEY": request.GatewayToken, "TDAI_LLM_API_KEY": request.LLMAPIKey,
	}, nil); err != nil {
		return errors.New("start restored MemoryCore")
	}
	waitHealthy := driver.WaitHealthy
	if waitHealthy == nil {
		waitHealthy = (Deployment{}).waitHealthy
	}
	if err := waitHealthy(ctx, request.Endpoint); err != nil {
		if driver.Runner != nil {
			_, _ = driver.Runner.Run(context.Background(), []string{"docker", "rm", "-f", request.CoreContainer}, nil)
		}
		return err
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
	_, found := findAgent(values, agentID, teamID, ownerID)
	return found
}

func imageDigestReference(reference string) string {
	_, digest, found := strings.Cut(reference, "@")
	if !found {
		return reference
	}
	return digest
}
