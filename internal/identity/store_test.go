package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"

	"mlink/internal/config"
)

type memorySecretStore struct {
	Values    map[string][]byte
	Puts      int
	Deletes   int
	FailPutAt int
}

func newMemorySecretStore() *memorySecretStore {
	return &memorySecretStore{Values: make(map[string][]byte)}
}

func (store *memorySecretStore) Put(_ context.Context, account string, value []byte) error {
	store.Puts++
	if store.FailPutAt > 0 && store.Puts == store.FailPutAt {
		return errors.New("injected put failure")
	}
	store.Values[account] = append([]byte(nil), value...)
	return nil
}

func (store *memorySecretStore) Get(_ context.Context, account string) ([]byte, error) {
	value, exists := store.Values[account]
	if !exists {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), value...), nil
}

func (store *memorySecretStore) Delete(_ context.Context, account string) error {
	store.Deletes++
	if _, exists := store.Values[account]; !exists {
		return fs.ErrNotExist
	}
	delete(store.Values, account)
	return nil
}

func TestPlanBindDoesNotPersistOrExposeExternalID(t *testing.T) {
	secrets := newMemorySecretStore()
	repository := Repository{Secrets: secrets, IdentityKey: bytes.Repeat([]byte{0x2a}, 32)}
	plan, err := repository.PlanBind(BindRequest{
		SlotID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner", Value: []byte("on_actual"),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.Puts != 0 || bytes.Contains(raw, []byte("on_actual")) {
		t.Fatalf("unsafe plan: puts=%d json=%s", secrets.Puts, raw)
	}
	if plan.Account != "identity/binding/owner-feishu-union-1" || plan.Ref.SecretRef != "keychain://dev.mlink/identity/binding/owner-feishu-union-1" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestApplyChangesFailureRestoresPreviousKeychainValue(t *testing.T) {
	secrets := newMemorySecretStore()
	secrets.Values["identity/binding/owner-feishu-union-1"] = []byte("on_old")
	secrets.FailPutAt = 2
	repository := Repository{Secrets: secrets, IdentityKey: bytes.Repeat([]byte{0x2a}, 32)}
	err := repository.ApplyChanges(context.Background(), []Change{
		{Account: "identity/binding/owner-feishu-union-1", Value: []byte("on_new")},
		{Account: "identity/binding/owner-feishu-union-2", Value: []byte("on_second")},
	})
	if err == nil || string(secrets.Values["identity/binding/owner-feishu-union-1"]) != "on_old" {
		t.Fatalf("rollback = %q, %v", secrets.Values["identity/binding/owner-feishu-union-1"], err)
	}
	if _, exists := secrets.Values["identity/binding/owner-feishu-union-2"]; exists {
		t.Fatal("failed new secret survived rollback")
	}
}

func TestRepositoryLoadsActiveAndRevokedBindings(t *testing.T) {
	secrets := newMemorySecretStore()
	secrets.Values["identity/binding/owner-feishu-union-1"] = []byte("on_active")
	secrets.Values["identity/binding/owner-feishu-union-2"] = []byte("on_revoked")
	repository := Repository{Secrets: secrets, IdentityKey: bytes.Repeat([]byte{0x2a}, 32)}
	refs := map[string]config.BindingRef{
		"owner-feishu-union-1": {ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner", SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: config.BindingActive},
		"owner-feishu-union-2": {ID: "owner-feishu-union-2", Source: "feishu", Kind: "union_id", PrincipalID: "owner", SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-2", Status: config.BindingRevoked},
	}
	got, err := repository.Load(context.Background(), refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Values) != 2 || got.Values[0].RefID != "owner-feishu-union-1" || got.Values[0].Revoked || !got.Values[1].Revoked {
		t.Fatalf("bindings = %#v", got.Values)
	}
}
