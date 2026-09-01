package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"mlink/internal/install"
)

type rotationSecrets struct {
	values map[string][]byte
	puts   int
	failAt int
}

func (store *rotationSecrets) Get(_ context.Context, account string) ([]byte, error) {
	value, ok := store.values[account]
	if !ok {
		return nil, errors.New("missing")
	}
	return append([]byte(nil), value...), nil
}

func (store *rotationSecrets) Put(_ context.Context, account string, value []byte) error {
	store.puts++
	if store.failAt > 0 && store.puts == store.failAt {
		return errors.New("injected secret write failure")
	}
	store.values[account] = append([]byte(nil), value...)
	return nil
}

func (store *rotationSecrets) Delete(context.Context, string) error { return nil }

type rotationRestarter struct {
	brokerCalls, hermesCalls int
	failBroker, failHermes   bool
}

func (restarter *rotationRestarter) RestartBroker(context.Context) error {
	restarter.brokerCalls++
	if restarter.failBroker {
		restarter.failBroker = false
		return errors.New("broker restart failed")
	}
	return nil
}

func (restarter *rotationRestarter) RestartHermes(context.Context) error {
	restarter.hermesCalls++
	if restarter.failHermes {
		restarter.failHermes = false
		return errors.New("hermes restart failed")
	}
	return nil
}

type rotationVerifier struct {
	calls int
	fail  bool
	new   []byte
	old   []byte
}

func (verifier *rotationVerifier) VerifyHermesGrant(_ context.Context, _ string, newGrant, oldGrant []byte) error {
	verifier.calls++
	verifier.new = append([]byte(nil), newGrant...)
	verifier.old = append([]byte(nil), oldGrant...)
	if verifier.fail {
		return errors.New("verification failed")
	}
	return nil
}

func rotationFixture(t *testing.T) (*Service, *memoryTarget, *rotationSecrets, *rotationRestarter, *rotationVerifier) {
	t.Helper()
	path := "/home/test/.hermes/mlink.json"
	target := newMemoryTarget(map[string]memoryFile{path: {content: []byte(`{"endpoint":"http://192.168.139.1:8097","token":"old-secret"}` + "\n"), mode: 0o600}})
	secrets := &rotationSecrets{values: map[string][]byte{"adapter/hermes/token": []byte("old-secret")}}
	restarter := &rotationRestarter{}
	verifier := &rotationVerifier{}
	service := &Service{
		Target: target, Secrets: secrets, HermesConfigPath: path, Restarter: restarter, HermesGrantVerifier: verifier,
		RandomSource: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32)),
	}
	return service, target, secrets, restarter, verifier
}

func TestPlanHermesGrantRotationIsSecretFreeAndZeroWrite(t *testing.T) {
	service, target, secrets, _, _ := rotationFixture(t)
	plan, err := service.PlanHermesGrantRotation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(plan)
	if target.writes != 0 || secrets.puts != 0 {
		t.Fatalf("preview writes = target:%d secrets:%d", target.writes, secrets.puts)
	}
	if bytes.Contains(data, []byte("old-secret")) || strings.Contains(string(data), "Kioq") {
		t.Fatalf("plan leaked a grant: %s", data)
	}
}

func TestApplyHermesGrantRotationUpdatesBothStoresAndVerifies(t *testing.T) {
	service, target, secrets, restarter, verifier := rotationFixture(t)
	plan, err := service.PlanHermesGrantRotation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyHermesGrantRotation(context.Background(), plan.PlanID); err != nil {
		t.Fatal(err)
	}
	newGrant := secrets.values["adapter/hermes/token"]
	if bytes.Equal(newGrant, []byte("old-secret")) || len(newGrant) != 43 {
		t.Fatalf("new grant length/value = %d/%q", len(newGrant), newGrant)
	}
	if !bytes.Contains(target.files[service.HermesConfigPath].content, newGrant) || restarter.brokerCalls != 1 || restarter.hermesCalls != 1 || verifier.calls != 1 || !bytes.Equal(verifier.old, []byte("old-secret")) {
		t.Fatalf("rotation state = %#v %#v %#v", target, restarter, verifier)
	}
}

func TestHermesGrantRotationRestoresBothValuesAfterFailure(t *testing.T) {
	service, target, secrets, restarter, verifier := rotationFixture(t)
	verifier.fail = true
	plan, _ := service.PlanHermesGrantRotation(context.Background())
	if err := service.ApplyHermesGrantRotation(context.Background(), plan.PlanID); err == nil {
		t.Fatal("expected verification failure")
	}
	if string(secrets.values["adapter/hermes/token"]) != "old-secret" || !bytes.Contains(target.files[service.HermesConfigPath].content, []byte(`"token":"old-secret"`)) {
		t.Fatalf("rollback values = %q/%s", secrets.values["adapter/hermes/token"], target.files[service.HermesConfigPath].content)
	}
	if restarter.brokerCalls != 2 || restarter.hermesCalls != 2 {
		t.Fatalf("rollback restart calls = %d/%d", restarter.brokerCalls, restarter.hermesCalls)
	}
}

func TestHermesGrantRotationRejectsStalePlan(t *testing.T) {
	service, target, _, _, _ := rotationFixture(t)
	plan, _ := service.PlanHermesGrantRotation(context.Background())
	target.files[service.HermesConfigPath] = memoryFile{content: []byte(`{"endpoint":"http://192.168.139.1:8097","token":"changed"}`), mode: 0o600}
	if err := service.ApplyHermesGrantRotation(context.Background(), plan.PlanID); !errors.Is(err, install.ErrPlanStale) {
		t.Fatalf("error = %v", err)
	}
}

var _ io.Reader = (*bytes.Reader)(nil)
