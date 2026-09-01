package app

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"testing"

	"mlink/internal/install"
	"mlink/internal/layout"
	"mlink/internal/version"
)

type upgradeLoader struct {
	candidate UpgradeCandidate
	err       error
}

func (loader upgradeLoader) LoadUpgradeCandidate(context.Context, string, int) (UpgradeCandidate, error) {
	return loader.candidate, loader.err
}

type installedVerifier struct {
	target *memoryTarget
	fail   bool
	calls  int
}

func (verifier *installedVerifier) VerifyInstalledBinary(_ context.Context, path, hash string) error {
	verifier.calls++
	if verifier.fail {
		return errors.New("self-test failed")
	}
	if fingerprintBytes(verifier.target.files[path].content) != hash {
		return errors.New("hash mismatch")
	}
	return nil
}

func upgradeFixture() (*Service, *memoryTarget, *memoryLedger, *installedVerifier, *rotationRestarter) {
	target := newMemoryTarget(map[string]memoryFile{"/Users/test/.local/bin/mlink": {content: []byte("old-binary"), mode: 0o700}})
	ledger := newMemoryLedger()
	verifier := &installedVerifier{target: target}
	restarter := &rotationRestarter{}
	service := &Service{
		Paths: layout.Paths{Binary: "/Users/test/.local/bin/mlink"}, UID: 501, Target: target, Ledger: ledger, ActiveSchema: 3,
		UpgradeCandidates: upgradeLoader{candidate: UpgradeCandidate{Path: "/tmp/mlink-new", Content: []byte("new-binary"), Mode: 0o700, Info: version.Info{Version: "1.0.0", SchemaMin: 2, SchemaMax: 3}}},
		InstalledVerifier: verifier, Restarter: restarter,
	}
	return service, target, ledger, verifier, restarter
}

func TestPlanUpgradeValidatesSchemaAndProducesExactBinaryHash(t *testing.T) {
	service, target, _, _, _ := upgradeFixture()
	plan, err := service.PlanUpgrade(context.Background(), UpgradeRequest{CandidatePath: "/tmp/mlink-new"})
	if err != nil {
		t.Fatal(err)
	}
	if target.writes != 0 || len(plan.Operations) != 1 || plan.Operations[0].ProposedHash != fingerprintBytes([]byte("new-binary")) {
		t.Fatalf("plan/writes = %#v/%d", plan, target.writes)
	}
	service.UpgradeCandidates = upgradeLoader{candidate: UpgradeCandidate{Path: "/tmp/mlink-new", Content: []byte("bad"), Mode: 0o700, Info: version.Info{Version: "1.0.0", SchemaMin: 4, SchemaMax: 4}}}
	if _, err := service.PlanUpgrade(context.Background(), UpgradeRequest{CandidatePath: "/tmp/mlink-new"}); err == nil {
		t.Fatal("accepted schema-incompatible candidate")
	}
}

func TestApplyUpgradeReplacesVerifiesAndRestarts(t *testing.T) {
	service, target, ledger, verifier, restarter := upgradeFixture()
	plan, _ := service.PlanUpgrade(context.Background(), UpgradeRequest{CandidatePath: "/tmp/mlink-new"})
	if err := service.ApplyUpgrade(context.Background(), plan.PlanID, UpgradeRequest{CandidatePath: "/tmp/mlink-new"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(target.files[service.Paths.Binary].content, []byte("new-binary")) || verifier.calls != 1 || restarter.brokerCalls != 1 || len(ledger.owned) != 1 {
		t.Fatalf("upgrade state = %#v %#v %#v", target, verifier, restarter)
	}
}

func TestApplyUpgradeRestoresByteIdenticalBinaryOnFailure(t *testing.T) {
	service, target, _, verifier, restarter := upgradeFixture()
	verifier.fail = true
	plan, _ := service.PlanUpgrade(context.Background(), UpgradeRequest{CandidatePath: "/tmp/mlink-new"})
	if err := service.ApplyUpgrade(context.Background(), plan.PlanID, UpgradeRequest{CandidatePath: "/tmp/mlink-new"}); err == nil {
		t.Fatal("expected self-test failure")
	}
	file := target.files[service.Paths.Binary]
	if !bytes.Equal(file.content, []byte("old-binary")) || file.mode.Perm() != fs.FileMode(0o700) || restarter.brokerCalls != 1 {
		t.Fatalf("rollback = %#v restarts=%d", file, restarter.brokerCalls)
	}
}

func TestApplyUpgradeRejectsStalePlan(t *testing.T) {
	service, target, _, _, _ := upgradeFixture()
	plan, _ := service.PlanUpgrade(context.Background(), UpgradeRequest{CandidatePath: "/tmp/mlink-new"})
	target.files[service.Paths.Binary] = memoryFile{content: []byte("changed"), mode: 0o700}
	if err := service.ApplyUpgrade(context.Background(), plan.PlanID, UpgradeRequest{CandidatePath: "/tmp/mlink-new"}); !errors.Is(err, install.ErrPlanStale) {
		t.Fatalf("error = %v", err)
	}
}
