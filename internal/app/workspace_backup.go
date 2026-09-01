package app

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"mlink/internal/config"
	"mlink/internal/controlplane"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/panel"
	"mlink/internal/provider/lifecycle"
	"mlink/internal/version"
	"mlink/internal/workspacebackup"
)

type WorkspaceBackupRequest struct {
	OutputPath            string
	Passphrase            []byte
	ApproveExternalSource bool
}

func (request WorkspaceBackupRequest) String() string {
	return fmt.Sprintf("WorkspaceBackupRequest{Output:%q Passphrase:<redacted> ApproveExternalSource:%t}", filepath.Base(request.OutputPath), request.ApproveExternalSource)
}

func (request WorkspaceBackupRequest) GoString() string { return request.String() }

func (request *WorkspaceBackupRequest) Wipe() {
	if request == nil {
		return
	}
	wipe(request.Passphrase)
	request.Passphrase = nil
}

type workspaceSecretMaterial struct {
	GatewayToken []byte `json:"gateway_token"`
	LLMAPIKey    []byte `json:"memory_llm_api_key"`
	HermesGrant  []byte `json:"hermes_grant,omitempty"`
	AdminUserKey []byte `json:"admin_user_key"`
	OwnerUserKey []byte `json:"owner_user_key"`
}

func (material *workspaceSecretMaterial) Wipe() {
	if material == nil {
		return
	}
	for _, value := range [][]byte{material.GatewayToken, material.LLMAPIKey, material.HermesGrant, material.AdminUserKey, material.OwnerUserKey} {
		wipe(value)
	}
	*material = workspaceSecretMaterial{}
}

func (service *Service) PlanWorkspaceBackup(ctx context.Context, request WorkspaceBackupRequest) (install.ChangeSet, error) {
	if service == nil || service.Target == nil || service.Ledger == nil || service.SnapshotDriver == nil || service.WorkspacePacker == nil ||
		service.WorkspaceArchiver == nil || service.Secrets == nil || service.ControlPlaneStates == nil || service.PrincipalAgentStates == nil {
		return install.ChangeSet{}, errors.New("full workspace backup dependencies are required")
	}
	if !filepath.IsAbs(request.OutputPath) || len(request.Passphrase) < 12 || len(request.Passphrase) > 4096 {
		return install.ChangeSet{}, errors.New("absolute backup output and protected passphrase are required")
	}
	if _, err := os.Lstat(request.OutputPath); err == nil {
		return install.ChangeSet{}, errors.New("workspace backup output already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return install.ChangeSet{}, errors.New("inspect workspace backup output")
	}
	if _, err := service.loadWorkspaceBackupConfig(ctx); err != nil {
		return install.ChangeSet{}, err
	}
	snapshotPlan, err := service.SnapshotDriver.PlanBackup(ctx, lifecycle.BackupRequest{
		ProviderID: "dev.mlink.tencentdb", ApproveExternalSource: request.ApproveExternalSource,
	})
	if err != nil {
		return install.ChangeSet{}, err
	}
	brokerPlan, err := install.BuildChangeSet(service.Target, []install.DesiredResource{
		backupBrokerPauseResource(service.Paths, service.UID),
		{
			OwnerID: "dev.mlink.workspace-backup", Target: "artifact:workspace-backup:" + pathFingerprint(request.OutputPath), Action: install.ActionUnchanged,
			SemanticDiff: []install.SemanticDiff{{Path: "backup.output", Before: "absent", After: filepath.Base(request.OutputPath)}},
		},
	})
	if err != nil {
		return install.ChangeSet{}, err
	}
	return install.ComposeChangeSets(brokerPlan, snapshotPlan)
}

func (service *Service) ApplyWorkspaceBackup(ctx context.Context, planID string, request WorkspaceBackupRequest) error {
	plan, err := service.PlanWorkspaceBackup(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: workspace backup plan changed", install.ErrPlanStale)
	}
	transaction := install.NewTransaction(service.Target, service.Ledger)
	if _, err := transaction.ApplyDeferredOwnership(ctx, plan); err != nil {
		return err
	}
	backupErr := service.createWorkspaceBackup(ctx, request)
	resumeErr := transaction.Rollback(ctx, plan)
	if resumeErr != nil && service.WorkspaceResumer != nil {
		if verifiedErr := service.WorkspaceResumer.ResumeWorkspaceServices(ctx); verifiedErr == nil {
			resumeErr = nil
		} else {
			resumeErr = errors.Join(resumeErr, verifiedErr)
		}
	}
	if backupErr != nil || resumeErr != nil {
		return errors.Join(backupErr, resumeErr)
	}
	if service.WorkspaceEvidence != nil {
		fingerprint, err := workspaceFileFingerprint(request.OutputPath)
		if err != nil {
			return err
		}
		if err := service.WorkspaceEvidence.RecordWorkspaceBackupEvidence(ctx, journal.WorkspaceBackupEvidence{BundleFingerprint: fingerprint, Format: workspacebackup.FormatV1}); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) InspectWorkspaceBackup(ctx context.Context, path string, passphrase []byte) (workspacebackup.Manifest, error) {
	if service == nil || service.WorkspacePacker == nil {
		return workspacebackup.Manifest{}, errors.New("workspace backup inspector is unavailable")
	}
	return service.WorkspacePacker.Open(ctx, path, passphrase, func(workspacebackup.Section, io.Reader) error { return nil })
}

func (service *Service) createWorkspaceBackup(ctx context.Context, request WorkspaceBackupRequest) error {
	configuration, err := service.loadWorkspaceBackupConfig(ctx)
	if err != nil {
		return err
	}
	state, err := service.ControlPlaneStates.LoadControlPlane(ctx)
	if err != nil {
		return err
	}
	mappings, err := service.PrincipalAgentStates.ListPrincipalAgents(ctx)
	if err != nil {
		return err
	}
	source, err := service.SnapshotDriver.Detect(ctx)
	if err != nil {
		return err
	}
	if !source.Owned && !request.ApproveExternalSource {
		return errors.New("compatible external MemoryCore requires explicit snapshot approval")
	}
	hubImage := source.HubImage
	if hubImage == "" {
		hubImage = panel.ImageReference
	}
	manifest := workspacebackup.Manifest{
		Format: workspacebackup.FormatV1, CreatedAt: time.Now().UTC(), MLink: version.Current(),
		Provider: workspacebackup.ProviderManifest{
			ProviderID: source.ProviderID, DriverVersion: source.DriverVersion, InstanceID: source.InstanceID,
			CoreImageDigest: imageDigest(source.CoreImage), HubImageDigest: imageDigest(hubImage),
		},
		ControlPlane: workspacebackup.ControlPlaneManifest{
			InstallationID: state.InstallationID, InstanceID: state.InstanceID, OwnerUserID: state.OwnerUserID,
			OwnerTeamID: state.OwnerTeamID, OwnerAgentID: state.OwnerAgentID, OwnerAssetID: state.OwnerAssetID,
			DynamicAgentLimit: state.DynamicAgentLimit,
		},
		PrincipalAgents: workspacePrincipalManifests(mappings),
		Agents:          enabledAgentIDs(configuration),
	}

	identityBundle, err := service.ExportIdentity(ctx, request.Passphrase, service.RandomSource)
	if err != nil {
		return err
	}
	defer wipe(identityBundle)
	secrets, err := service.workspaceBackupSecrets(ctx, configuration)
	if err != nil {
		return err
	}
	defer secrets.Wipe()
	secretData, err := json.Marshal(secrets)
	if err != nil {
		return errors.New("encode protected workspace backup secrets")
	}
	defer wipe(secretData)
	agentData, err := json.Marshal(struct {
		Agents []string `json:"agents"`
	}{Agents: manifest.Agents})
	if err != nil {
		return errors.New("encode workspace backup Agent selection")
	}

	snapshotRequest := lifecycle.BackupRequest{ProviderID: source.ProviderID, ApproveExternalSource: request.ApproveExternalSource}
	sources := []workspacebackup.SectionSource{
		{Name: workspacebackup.SectionMLink, Open: func(ctx context.Context) (io.ReadCloser, error) {
			return service.WorkspaceArchiver.Open(ctx, service.Paths)
		}},
		byteSection(workspacebackup.SectionIdentity, identityBundle),
		byteSection(workspacebackup.SectionSecrets, secretData),
		byteSection(workspacebackup.SectionAgents, agentData),
		service.snapshotSectionSource(snapshotRequest, workspacebackup.SectionCore, &manifest),
	}
	for _, volume := range source.Volumes {
		if volume.Kind == workspacebackup.VolumeKnowledge {
			sources = append(sources, service.snapshotSectionSource(snapshotRequest, workspacebackup.SectionKnowledge, &manifest))
			break
		}
	}
	return service.WorkspacePacker.Pack(ctx, request.OutputPath, request.Passphrase, &manifest, sources...)
}

func (service *Service) snapshotSectionSource(request lifecycle.BackupRequest, section workspacebackup.Section, manifest *workspacebackup.Manifest) workspacebackup.SectionSource {
	return workspacebackup.SectionSource{Name: section, Open: func(ctx context.Context) (io.ReadCloser, error) {
		reader, writer := io.Pipe()
		go func() {
			volume, err := service.SnapshotDriver.StreamSection(ctx, request, section, writer)
			if err == nil {
				manifest.Provider.Volumes = append(manifest.Provider.Volumes, volume)
			}
			_ = writer.CloseWithError(err)
		}()
		return reader, nil
	}}
}

func (service *Service) loadWorkspaceBackupConfig(ctx context.Context) (config.Config, error) {
	content, _, err := service.Target.Read(ctx, service.Paths.Config)
	if err != nil {
		return config.Config{}, err
	}
	configuration, err := config.Decode(content)
	if err != nil || configuration.SchemaVersion != 3 || configuration.ControlPlane == nil {
		return config.Config{}, errors.New("active Schema v3 MLink configuration is required for full backup")
	}
	return configuration, nil
}

func (service *Service) workspaceBackupSecrets(ctx context.Context, configuration config.Config) (workspaceSecretMaterial, error) {
	connection := configuration.Connections[configuration.ActiveConnectionID]
	const prefix = "keychain://dev.mlink/"
	ref := connection.SecretRefs["token"]
	if !strings.HasPrefix(ref, prefix) || len(ref) == len(prefix) {
		return workspaceSecretMaterial{}, errors.New("MemoryCore Gateway secret reference is invalid")
	}
	accounts := []string{
		strings.TrimPrefix(ref, prefix), "provider/tencentdb/llm-api-key",
		controlplane.AdminUserKeyAccount, controlplane.OwnerUserKeyAccount,
	}
	values := make([][]byte, len(accounts))
	for index, account := range accounts {
		value, err := service.Secrets.Get(ctx, account)
		if err != nil {
			for _, item := range values {
				wipe(item)
			}
			return workspaceSecretMaterial{}, fmt.Errorf("load required workspace backup secret %q", account)
		}
		values[index] = value
	}
	material := workspaceSecretMaterial{
		GatewayToken: values[0], LLMAPIKey: values[1], AdminUserKey: values[2], OwnerUserKey: values[3],
	}
	if adapter, exists := configuration.Adapters[string(Hermes)]; exists && adapter.Enabled {
		grant, err := service.Secrets.Get(ctx, "adapter/hermes/token")
		if err != nil {
			material.Wipe()
			return workspaceSecretMaterial{}, errors.New("load required Hermes backup credential")
		}
		material.HermesGrant = grant
	}
	return material, nil
}

type LocalWorkspaceArchiver struct{}

func (LocalWorkspaceArchiver) Open(ctx context.Context, paths layout.Paths) (io.ReadCloser, error) {
	if err := journal.CheckpointAndVerify(ctx, paths.Journal); err != nil {
		return nil, err
	}
	for _, required := range []string{paths.Config, paths.Journal, filepath.Join(paths.Home, "memorycore", "tdai-gateway.yaml")} {
		info, err := os.Lstat(required)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			return nil, errors.New("complete regular MLink state files are required")
		}
	}
	reader, writer := io.Pipe()
	go func() { _ = writer.CloseWithError(writeWorkspaceStateTar(ctx, writer, paths)) }()
	return reader, nil
}

func writeWorkspaceStateTar(ctx context.Context, destination io.Writer, paths layout.Paths) error {
	archive := tar.NewWriter(destination)
	entries := []struct {
		source, name string
		optional     bool
	}{
		{paths.Config, "config.yaml", false}, {paths.Journal, "journal.db", false},
		{filepath.Join(paths.Home, "memorycore", "tdai-gateway.yaml"), "memorycore/tdai-gateway.yaml", false},
		{paths.PanelRegistry, "panel/metadata-instances.json", true},
	}
	for _, entry := range entries {
		if err := writeWorkspaceFile(ctx, archive, entry.source, entry.name, entry.optional); err != nil {
			return err
		}
	}
	if info, err := os.Lstat(paths.Backups); err == nil && info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		if err := filepath.WalkDir(paths.Backups, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
				return errors.New("unsafe MLink backup artifact")
			}
			relative, err := filepath.Rel(paths.Backups, path)
			if err != nil || strings.HasPrefix(relative, "..") {
				return errors.New("unsafe MLink backup artifact path")
			}
			return writeWorkspaceFile(ctx, archive, path, filepath.ToSlash(filepath.Join("backups", relative)), false)
		}); err != nil {
			return err
		}
	}
	return archive.Close()
}

func writeWorkspaceFile(ctx context.Context, archive *tar.Writer, source, name string, optional bool) error {
	info, err := os.Lstat(source)
	if errors.Is(err, fs.ErrNotExist) && optional {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("safe MLink state file is required")
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name, header.Mode = name, int64(info.Mode().Perm())
	if err := archive.WriteHeader(header); err != nil {
		return err
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(archive, &contextReader{Context: ctx, Reader: file})
	closeErr := file.Close()
	return errors.Join(copyErr, closeErr)
}

type contextReader struct {
	Context context.Context
	Reader  io.Reader
}

func (reader *contextReader) Read(value []byte) (int, error) {
	if err := reader.Context.Err(); err != nil {
		return 0, err
	}
	return reader.Reader.Read(value)
}

func backupBrokerPauseResource(paths layout.Paths, uid int) install.DesiredResource {
	domain := "gui/" + strconv.Itoa(uid)
	service := domain + "/dev.mlink.broker"
	plist := filepath.Join(filepath.Dir(paths.Home), "Library", "LaunchAgents", "dev.mlink.broker.plist")
	return install.DesiredResource{
		OwnerID: "dev.mlink.workspace-backup", Target: "service:backup-pause:dev.mlink.broker", Action: install.ActionService,
		Command: []string{"launchctl", "bootout", service}, RollbackCommand: []string{"launchctl", "bootstrap", domain, plist},
		SemanticDiff: []install.SemanticDiff{{Path: "backup:broker", Before: "running", After: "paused for consistent snapshot"}},
	}
}

func workspacePrincipalManifests(values []journal.PrincipalAgent) []workspacebackup.PrincipalAgentManifest {
	result := make([]workspacebackup.PrincipalAgentManifest, 0, len(values))
	for _, value := range values {
		result = append(result, workspacebackup.PrincipalAgentManifest{
			Fingerprint: value.Fingerprint, RouteKind: value.RouteKind, BackendUserID: value.BackendUserID,
			BackendTeamID: value.BackendTeamID, BackendAgentID: value.BackendAgentID, BackendAssetID: value.BackendAssetID, State: value.State,
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Fingerprint < result[right].Fingerprint })
	return result
}

func enabledAgentIDs(configuration config.Config) []string {
	var result []string
	for _, agent := range []Agent{Codex, Cursor, Pi, Hermes} {
		if adapter, exists := configuration.Adapters[string(agent)]; exists && adapter.Enabled {
			result = append(result, string(agent))
		}
	}
	return result
}

func byteSection(name workspacebackup.Section, value []byte) workspacebackup.SectionSource {
	return workspacebackup.SectionSource{Name: name, Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(value)), nil
	}}
}

func pathFingerprint(path string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(digest[:])[:16]
}

func imageDigest(reference string) string {
	_, digest, found := strings.Cut(reference, "@")
	if !found {
		return reference
	}
	return digest
}

func workspaceFileFingerprint(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("open completed workspace backup")
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", errors.New("hash completed workspace backup")
	}
	return hex.EncodeToString(digest.Sum(nil))[:16], nil
}
