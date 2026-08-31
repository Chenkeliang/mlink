package identity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mlink/internal/config"
)

func TestEncryptedBundleRoundTripWrongPassphraseAndTamperDetection(t *testing.T) {
	bundle := fixtureBundle()
	passphrase := []byte("passphrase-12")
	encrypted, err := EncryptBundle(bundle, passphrase, bytes.NewReader(bytes.Repeat([]byte{0x11}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("on_owner")) || bytes.Contains(encrypted, bundle.IdentityKey) {
		t.Fatal("encrypted bundle contains plaintext identity material")
	}
	got, err := DecryptBundle(encrypted, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, bundle) {
		t.Fatal("decrypted bundle differs")
	}
	if _, err := DecryptBundle(encrypted, []byte("wrong-passphrase")); !errors.Is(err, ErrBundleAuthentication) {
		t.Fatalf("wrong passphrase error = %v", err)
	}
	var envelope encryptedEnvelope
	if err := json.Unmarshal(encrypted, &envelope); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[len(ciphertext)-1] ^= 1
	envelope.Ciphertext = base64.RawStdEncoding.EncodeToString(ciphertext)
	tampered, _ := json.Marshal(envelope)
	if _, err := DecryptBundle(tampered, passphrase); !errors.Is(err, ErrBundleAuthentication) {
		t.Fatalf("tamper error = %v", err)
	}
}

func TestWriteBundleAtomicUsesPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.mlink")
	if err := WriteBundleAtomic(path, []byte("encrypted")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestBundleFormattingRedactsIdentityMaterial(t *testing.T) {
	bundle := fixtureBundle()
	for _, rendered := range []string{bundle.String(), bundle.GoString(), bundle.Bindings[0].String(), bundle.Bindings[0].GoString()} {
		if strings.Contains(rendered, "on_owner") || strings.Contains(rendered, string(bundle.IdentityKey)) {
			t.Fatalf("rendered bundle = %q", rendered)
		}
	}
}

func TestControlPlaneBundleRoundTripsGeneratedIDsAndMappings(t *testing.T) {
	bundle := fixtureBundle()
	bundle.SchemaVersion = 2
	bundle.Spaces = nil
	bundle.Principals["owner"] = config.Principal{ID: "owner", CanonicalUserID: "usr-owner-generated", Kind: config.PrincipalPerson}
	bundle.ControlPlane = &BundleControlPlane{
		InstallationID: "installation-1", InstanceID: "default", OwnerUserID: "usr-owner-generated",
		OwnerTeamID: "team-owner-generated", OwnerAgentID: "agt-owner-generated",
		OwnerAssetID: "chat_memory-team-owner-generated-agt-owner-generated", PanelContainer: "mlink-memory-panel",
		PanelImage: "mlink-memory-panel:a5dcbe6", State: "active",
	}
	bundle.AdminUserKey = []byte("admin-key")
	bundle.OwnerUserKey = []byte("owner-key")
	bundle.PrincipalAgents = []BundlePrincipalAgent{{
		Fingerprint: "prn_aaaaaaaaaaaaaaaaaaaaaaaaaa", RouteKind: "hermes-private", BackendUserID: "usr-owner-generated",
		BackendTeamID: "team-owner-generated", BackendAgentID: "agt-private", BackendAssetID: "chat_memory-team-owner-generated-agt-private",
		DisplayLabel: "Feishu DM", State: "active",
	}}
	encrypted, err := EncryptBundle(bundle, []byte("passphrase-12"), bytes.NewReader(bytes.Repeat([]byte{0x22}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptBundle(encrypted, []byte("passphrase-12"))
	if err != nil || !reflect.DeepEqual(got, bundle) {
		t.Fatalf("round trip = %#v, %v", got, err)
	}
	plaintext, _ := json.Marshal(got)
	for _, forbidden := range []string{"on_raw", "oc_raw", "owner-key", "admin-key"} {
		if strings.Contains(string(plaintext), forbidden) {
			t.Fatalf("bundle leaked %q: %s", forbidden, plaintext)
		}
	}
}

func fixtureBundle() BundleV1 {
	return BundleV1{
		SchemaVersion: 1,
		Principals:    map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr_owner_keliang", Kind: config.PrincipalPerson}},
		Spaces: map[string]config.MemorySpace{
			"personal-owner": {ID: "personal-owner", ConnectionID: "local", TenantID: "personal", AgentID: "keliang-personal", IncludeAgentShared: true, PrincipalPolicy: config.PolicyFixed, PrincipalID: "owner"},
			"hermes-private": {ID: "hermes-private", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-private", PrincipalPolicy: config.PolicyExternalHMAC},
			"hermes-groups":  {ID: "hermes-groups", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-groups", PrincipalPolicy: config.PolicyGroupHMAC},
		},
		IdentityKey: bytes.Repeat([]byte{0x2a}, 32),
		Bindings: []BindingExport{{
			Ref:   config.BindingRef{ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner", SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: config.BindingActive},
			Value: []byte("on_owner"),
		}},
		CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}
}
