package app

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"sort"
	"testing"

	"mlink/internal/install"
)

func (ledger *memoryLedger) ListBackups(_ context.Context, planID string) ([]install.Backup, error) {
	var backups []install.Backup
	for _, backup := range ledger.backups {
		if backup.PlanID == planID {
			backup.Content = append([]byte(nil), backup.Content...)
			backups = append(backups, backup)
		}
	}
	if len(backups) == 0 {
		return nil, fs.ErrNotExist
	}
	sort.Slice(backups, func(left, right int) bool { return backups[left].Target < backups[right].Target })
	return backups, nil
}

func (ledger *memoryLedger) LatestBackupID(context.Context) (string, error) {
	for _, backup := range ledger.backups {
		return backup.PlanID, nil
	}
	return "", fs.ErrNotExist
}

func TestInstallThenRestorePreservesLaterUnownedAgentConfig(t *testing.T) {
	service, target, _ := newInstallFixture(t)
	request := fixtureInstallRequest()
	installPlan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), installPlan.PlanID, request); err != nil {
		t.Fatal(err)
	}

	hermesPath := "/home/test/.hermes/config.yaml"
	hermesConfig := append([]byte(nil), target.files[hermesPath].content...)
	hermesConfig = append(hermesConfig, []byte("later_user_setting: keep\n")...)
	target.files[hermesPath] = memoryFile{content: hermesConfig, mode: 0o600}
	codexPath := "/Users/test/.codex/hooks.json"
	codexConfig := target.files[codexPath].content
	codexConfig = bytes.Replace(codexConfig, []byte(`"hooks": {`), []byte(`"later": "keep", "hooks": {`), 1)
	target.files[codexPath] = memoryFile{content: codexConfig, mode: 0o600}

	restorePlan, err := service.PlanRestore(context.Background(), RestoreRequest{BackupID: installPlan.PlanID})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyRestore(context.Background(), restorePlan.PlanID, RestoreRequest{BackupID: installPlan.PlanID}); err != nil {
		t.Fatal(err)
	}
	if _, exists := target.files[service.Paths.Binary]; exists {
		t.Fatal("installed binary still exists after restore")
	}
	if _, exists := target.files["/home/test/.hermes/plugins/mlink/__init__.py"]; exists {
		t.Fatal("Hermes Provider still exists after restore")
	}
	if got := target.files[hermesPath].content; !bytes.Contains(got, []byte("provider: hy-memory")) || !bytes.Contains(got, []byte("later_user_setting: keep")) {
		t.Fatalf("restored Hermes config = %s", got)
	}
	if got := target.files[codexPath].content; !bytes.Contains(got, []byte(`"later": "keep"`)) || bytes.Contains(got, []byte("hook codex")) {
		t.Fatalf("restored Codex config = %s", got)
	}
}

func TestRestoreRejectsModifiedOwnedWholeFile(t *testing.T) {
	service, target, _ := newInstallFixture(t)
	request := fixtureInstallRequest()
	installPlan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), installPlan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	piPath := "/Users/test/.pi/agent/extensions/mlink.ts"
	target.files[piPath] = memoryFile{content: []byte("user changed owned file"), mode: 0o600}
	if _, err := service.PlanRestore(context.Background(), RestoreRequest{BackupID: installPlan.PlanID}); !errors.Is(err, ErrOwnedResourceChanged) {
		t.Fatalf("PlanRestore() error = %v", err)
	}
}
