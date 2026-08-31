package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mlink/internal/app"
	"mlink/internal/identity"
	"mlink/internal/install"
)

type identityFakeApplication struct {
	fakeApplication
	identityPlan  install.ChangeSet
	identityApply int
	lastBind      app.IdentityBindRequest
}

type identityBundleFakeApplication struct {
	identityFakeApplication
	encrypted   []byte
	importPlan  install.ChangeSet
	importApply int
}

func (application *identityBundleFakeApplication) ExportIdentity(context.Context, []byte) ([]byte, error) {
	return append([]byte(nil), application.encrypted...), nil
}

func (application *identityBundleFakeApplication) PlanIdentityImport(context.Context, identity.BundleV1) (install.ChangeSet, error) {
	return application.importPlan, nil
}

func (application *identityBundleFakeApplication) ApplyIdentityImport(context.Context, string, identity.BundleV1) error {
	application.importApply++
	return nil
}

func (application *identityFakeApplication) PlanIdentityBind(_ context.Context, request app.IdentityBindRequest) (install.ChangeSet, error) {
	request.Value = append([]byte(nil), request.Value...)
	application.lastBind = request
	return application.identityPlan, nil
}

func (application *identityFakeApplication) ApplyIdentityBind(_ context.Context, _ string, request app.IdentityBindRequest) error {
	request.Value = append([]byte(nil), request.Value...)
	application.lastBind = request
	application.identityApply++
	return nil
}

func (application *identityFakeApplication) PlanIdentityRebind(context.Context, app.IdentityRebindRequest) (install.ChangeSet, error) {
	return application.identityPlan, nil
}

func (application *identityFakeApplication) ApplyIdentityRebind(context.Context, string, app.IdentityRebindRequest) error {
	application.identityApply++
	return nil
}

func (application *identityFakeApplication) PlanIdentityRevoke(context.Context, app.IdentityRevokeRequest) (install.ChangeSet, error) {
	return application.identityPlan, nil
}

func (application *identityFakeApplication) ApplyIdentityRevoke(context.Context, string, app.IdentityRevokeRequest) error {
	application.identityApply++
	return nil
}

func (application *identityFakeApplication) IdentityList(context.Context) ([]app.IdentityDescriptor, error) {
	return []app.IdentityDescriptor{{SlotID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner", Status: "active", Fingerprint: "bind_abcd", Suffix: "1234"}}, nil
}

func TestIdentityBindReadsValueOnlyFromStdinAndPreviews(t *testing.T) {
	application := &identityFakeApplication{identityPlan: fixtureInstallPlan()}
	stdout := new(bytes.Buffer)
	code := Run(context.Background(), []string{"identity", "bind", "--principal", "owner", "--source", "feishu", "--kind", "union_id", "--slot", "owner-feishu-union-2", "--stdin", "--dry-run", "--json"}, Dependencies{
		App: application, Stdin: strings.NewReader("on_new\n"), Stdout: stdout, Stderr: io.Discard,
	})
	if code != 0 || application.identityApply != 0 || bytes.Contains(stdout.Bytes(), []byte("on_new")) || string(application.lastBind.Value) != "on_new" {
		t.Fatalf("code/apply/output/request = %d/%d/%s/%#v", code, application.identityApply, stdout.Bytes(), application.lastBind)
	}
}

func TestIdentityRejectsExternalIDInArgv(t *testing.T) {
	code := Run(context.Background(), []string{"identity", "bind", "--value", "on_secret"}, Dependencies{Stderr: io.Discard})
	if code != 2 {
		t.Fatalf("code = %d", code)
	}
}

func TestInstallSecretsJSONCarriesTokenAndOwnerBindingWithoutEcho(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	template := fixtureCLIInstallRequest()
	template.SecretInputs = nil
	stdout := new(bytes.Buffer)
	input := `{"memorycore_token":"token-secret","owner_binding":{"kind":"union_id","value":"on_owner"}}` + "\n"
	code := Run(context.Background(), []string{"install", "--dry-run", "hermes", "--install-secrets-stdin", "--json"}, Dependencies{
		App: application, InstallRequest: template, Stdin: strings.NewReader(input), Stdout: stdout, Stderr: io.Discard,
	})
	if code != 0 || bytes.Contains(stdout.Bytes(), []byte("token-secret")) || bytes.Contains(stdout.Bytes(), []byte("on_owner")) {
		t.Fatalf("code/output = %d/%s", code, stdout.Bytes())
	}
}

func TestIdentityExportReadsPassphraseFromStdinAndWritesPrivateFile(t *testing.T) {
	application := &identityBundleFakeApplication{encrypted: []byte("encrypted-bundle")}
	path := filepath.Join(t.TempDir(), "identity.mlink")
	code := Run(context.Background(), []string{"identity", "export", "--output", path, "--passphrase-stdin"}, Dependencies{
		App: application, Stdin: strings.NewReader("passphrase-12\n"), Stdout: io.Discard, Stderr: io.Discard,
	})
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "encrypted-bundle" {
		t.Fatalf("bundle = %q, %v", data, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestIdentityRejectsPassphraseInArgv(t *testing.T) {
	code := Run(context.Background(), []string{"identity", "export", "--output", "/tmp/identity", "--passphrase", "secret"}, Dependencies{Stderr: io.Discard})
	if code != 2 {
		t.Fatalf("code = %d", code)
	}
}
