package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mlink/internal/app"
	"mlink/internal/controlplane"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/model"
	"mlink/internal/panel"
	"mlink/internal/provider/lifecycle"
	"mlink/internal/provider/tencentdb"
	"mlink/internal/workspacebackup"
)

func TestLiveFullWorkspaceBackupRestore(t *testing.T) {
	if os.Getenv("MLINK_TEST_FULL_RESTORE") != "1" {
		t.Skip("set MLINK_TEST_FULL_RESTORE=1 for the explicit isolated restore acceptance")
	}
	bundlePath := os.Getenv("MLINK_TEST_FULL_BACKUP_PATH")
	passphrase := []byte(os.Getenv("MLINK_TEST_FULL_BACKUP_PASSPHRASE"))
	if bundlePath == "" || len(passphrase) < 12 {
		wipeRuntimeSecret(passphrase)
		t.Fatal("MLINK_TEST_FULL_BACKUP_PATH and protected passphrase are required")
	}
	defer wipeRuntimeSecret(passphrase)
	prefix := fmt.Sprintf("mlink-restore-e2e-%d", os.Getpid())
	coreContainer, coreVolume := prefix+"-core", prefix+"-core-data"
	knowledgeVolume, network := prefix+"-knowledge", prefix+"-network"
	for _, forbidden := range []string{tencentdb.MemoryCoreContainerName, tencentdb.MemoryCoreVolumeName, panel.VolumeName, tencentdb.MemoryCoreNetworkName} {
		if coreContainer == forbidden || coreVolume == forbidden || knowledgeVolume == forbidden || network == forbidden {
			t.Fatal("isolated restore resolved a formal resource name")
		}
	}
	cleanup := func() {
		commands := [][]string{{"docker", "rm", "-f", coreContainer}, {"docker", "volume", "rm", coreVolume}, {"docker", "volume", "rm", knowledgeVolume}, {"docker", "network", "rm", network}}
		for _, command := range commands {
			_ = exec.Command(command[0], command[1:]...).Run()
		}
	}
	cleanup()
	defer cleanup()

	home := t.TempDir()
	paths, err := layout.FromHome(home, filepath.Join(home, "candidate-mlink"))
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(paths.Home, "tmp")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	keys := &restoreTestSecrets{values: map[string][]byte{}}
	agents := &restoreTestAgents{}
	local := &localWorkspaceRestorer{
		paths: paths, secrets: keys, passphrase: append([]byte(nil), passphrase...), agents: agents,
		bundle: workspacebackup.Packer{},
		provider: lifecycle.RestoreRequest{
			CoreContainer: coreContainer, CoreVolume: coreVolume, KnowledgeVolume: knowledgeVolume, CoreNetwork: network,
			CoreConfigPath: filepath.Join(paths.Home, "memorycore", "tdai-gateway.yaml"), Endpoint: "http://127.0.0.1:18421",
		},
	}
	target := install.LocalTarget{}
	driver := &tencentdb.SnapshotDriver{
		Runner: target, Stream: install.LocalStreamRunner{}, Target: target, Environment: install.LocalEnvironmentRunner{},
		Metadata: restoreMetadataClient{restorer: local}, InstanceID: "default",
		CoreContainer: coreContainer, CoreVolume: coreVolume, KnowledgeVolume: knowledgeVolume,
	}
	service := &app.Service{
		Paths: paths, UID: os.Getuid(), Target: target, Ledger: &restoreMemoryLedger{}, Secrets: keys,
		SnapshotDriver: driver, WorkspacePacker: workspacebackup.Packer{StagingParent: staging}, WorkspaceFingerprinter: workspacebackup.Packer{}, WorkspaceRestorer: local,
	}
	request := app.WorkspaceRestoreRequest{BundlePath: bundlePath, Passphrase: append([]byte(nil), passphrase...)}
	defer request.Wipe()
	plan, err := service.PlanWorkspaceRestore(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyWorkspaceRestore(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.InspectWorkspaceBackup(context.Background(), bundlePath, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	gateway := keys.values["connection/local/token"]
	client, err := tencentdb.NewClient(tencentdb.Config{BaseURL: "http://127.0.0.1:18421", Token: string(gateway), ServiceID: manifest.Provider.InstanceID})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := tencentdb.NewProvider(client).Recall(context.Background(), model.RecallRequest{
		Identity: model.IdentityScope{ConnectionID: "local", TenantID: manifest.ControlPlane.OwnerTeamID, UserID: manifest.ControlPlane.OwnerUserID, AgentID: manifest.ControlPlane.OwnerAgentID},
		Query:    "墨绿色", MaxItems: 5, IncludeAgentShared: true,
	})
	if err != nil || len(bundle.Items) == 0 {
		t.Fatalf("restored memory recall items/error = %d/%v", len(bundle.Items), err)
	}

	stateStore, err := journal.Open(context.Background(), paths.Journal)
	if err != nil {
		t.Fatal(err)
	}
	metadata := tencentdb.NewMetadataClient(client)
	provisioner := &controlplane.AgentProvisioner{Metadata: metadata, Secrets: keys, Store: stateStore, MaxDynamicAgents: 500, Timeout: 20 * time.Second}
	for index, route := range []string{"hermes-private", "hermes-group"} {
		fingerprint := "prn_" + strings.Repeat(string(rune('a'+index)), 26)
		if _, err := provisioner.ResolveOrCreate(context.Background(), controlplane.PrincipalIntent{Fingerprint: fingerprint, RouteKind: route, DisplayLabel: "live-isolated"}); err != nil {
			_ = stateStore.Close()
			t.Fatal(err)
		}
	}
	if err := stateStore.Close(); err != nil {
		t.Fatal(err)
	}

	dynamicBundle := filepath.Join(t.TempDir(), "dynamic.mlink-backup")
	backupStore, err := journal.Open(context.Background(), paths.Journal)
	if err != nil {
		t.Fatal(err)
	}
	backupDriver := &tencentdb.SnapshotDriver{
		Runner: target, Stream: install.LocalStreamRunner{}, Target: target, InstanceID: "default",
		CoreContainer: coreContainer, CoreVolume: coreVolume, HubContainer: prefix + "-hub-absent", KnowledgeVolume: knowledgeVolume,
	}
	backupService := &app.Service{
		Paths: paths, UID: os.Getuid(), Target: target, Ledger: &restoreMemoryLedger{}, Secrets: keys,
		ControlPlaneStates: backupStore, PrincipalAgentStates: backupStore, SnapshotDriver: backupDriver,
		WorkspacePacker: workspacebackup.Packer{StagingParent: staging}, WorkspaceArchiver: app.LocalWorkspaceArchiver{},
	}
	hostHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	backupService.WorkspaceResumer = runtimeWorkspaceResumer{
		target: target, driver: backupDriver, uid: os.Getuid(),
		plist: filepath.Join(hostHome, "Library", "LaunchAgents", "dev.mlink.broker.plist"),
	}
	backupRequest := app.WorkspaceBackupRequest{OutputPath: dynamicBundle, Passphrase: append([]byte(nil), passphrase...), ApproveExternalSource: true}
	backupPlan, err := backupService.PlanWorkspaceBackup(context.Background(), backupRequest)
	if err == nil {
		err = backupService.ApplyWorkspaceBackup(context.Background(), backupPlan.PlanID, backupRequest)
	}
	backupRequest.Wipe()
	if closeErr := backupStore.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	dynamicManifest, err := backupService.InspectWorkspaceBackup(context.Background(), dynamicBundle, passphrase)
	if err != nil || len(dynamicManifest.PrincipalAgents) != 2 {
		t.Fatalf("dynamic backup manifest mappings/error = %d/%v", len(dynamicManifest.PrincipalAgents), err)
	}

	secondContainer, secondCoreVolume := prefix+"-core-2", prefix+"-core-data-2"
	secondKnowledge, secondNetwork := prefix+"-knowledge-2", prefix+"-network-2"
	defer func() {
		for _, command := range [][]string{{"docker", "rm", "-f", secondContainer}, {"docker", "volume", "rm", secondCoreVolume}, {"docker", "volume", "rm", secondKnowledge}, {"docker", "network", "rm", secondNetwork}} {
			_ = exec.Command(command[0], command[1:]...).Run()
		}
	}()
	secondHome := t.TempDir()
	secondPaths, err := layout.FromHome(secondHome, filepath.Join(secondHome, "candidate-mlink"))
	if err != nil {
		t.Fatal(err)
	}
	secondStaging := filepath.Join(secondPaths.Home, "tmp")
	if err := os.MkdirAll(secondStaging, 0o700); err != nil {
		t.Fatal(err)
	}
	secondKeys := &restoreTestSecrets{values: map[string][]byte{}}
	secondLocal := &localWorkspaceRestorer{
		paths: secondPaths, secrets: secondKeys, passphrase: append([]byte(nil), passphrase...), agents: &restoreTestAgents{},
		bundle: workspacebackup.Packer{},
		provider: lifecycle.RestoreRequest{
			CoreContainer: secondContainer, CoreVolume: secondCoreVolume, KnowledgeVolume: secondKnowledge, CoreNetwork: secondNetwork,
			CoreConfigPath: filepath.Join(secondPaths.Home, "memorycore", "tdai-gateway.yaml"), Endpoint: "http://127.0.0.1:18422",
		},
	}
	secondDriver := &tencentdb.SnapshotDriver{
		Runner: target, Stream: install.LocalStreamRunner{}, Target: target, Environment: install.LocalEnvironmentRunner{},
		Metadata: restoreMetadataClient{restorer: secondLocal}, InstanceID: "default",
		CoreContainer: secondContainer, CoreVolume: secondCoreVolume, KnowledgeVolume: secondKnowledge,
	}
	secondService := &app.Service{
		Paths: secondPaths, UID: os.Getuid(), Target: target, Ledger: &restoreMemoryLedger{}, Secrets: secondKeys,
		SnapshotDriver: secondDriver, WorkspacePacker: workspacebackup.Packer{StagingParent: secondStaging}, WorkspaceFingerprinter: workspacebackup.Packer{}, WorkspaceRestorer: secondLocal,
	}
	secondRequest := app.WorkspaceRestoreRequest{BundlePath: dynamicBundle, Passphrase: append([]byte(nil), passphrase...)}
	secondPlan, err := secondService.PlanWorkspaceRestore(context.Background(), secondRequest)
	if err == nil {
		err = secondService.ApplyWorkspaceRestore(context.Background(), secondPlan.PlanID, secondRequest)
	}
	secondRequest.Wipe()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLiveWorkspaceBackupCurrentMachine(t *testing.T) {
	if os.Getenv("MLINK_TEST_FULL_BACKUP") != "1" {
		t.Skip("set MLINK_TEST_FULL_BACKUP=1 for the explicit live backup acceptance")
	}
	dependencies, err := defaultDependencies(io.Reader(nil), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runtime, ok := dependencies.App.(*runtimeApplication)
	if !ok {
		t.Fatal("runtime application is unavailable")
	}
	path := os.Getenv("MLINK_TEST_FULL_BACKUP_PATH")
	passphrase := []byte(os.Getenv("MLINK_TEST_FULL_BACKUP_PASSPHRASE"))
	if path == "" || len(passphrase) < 12 {
		wipeRuntimeSecret(passphrase)
		t.Fatal("MLINK_TEST_FULL_BACKUP_PATH and protected passphrase are required")
	}
	defer wipeRuntimeSecret(passphrase)
	request := app.WorkspaceBackupRequest{OutputPath: path, Passphrase: append([]byte(nil), passphrase...)}
	defer request.Wipe()
	plan, err := runtime.PlanWorkspaceBackup(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ApplyWorkspaceBackup(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
}
