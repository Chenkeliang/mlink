package workspacebackup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mlink/internal/version"
)

func TestEncryptSectionStagesCiphertextOnly(t *testing.T) {
	staging := t.TempDir()
	source := SectionSource{Name: SectionCore, Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("CANARY-CLEAR")), nil
	}}
	staged, err := EncryptSection(context.Background(), staging, []byte("twelve-byte-passphrase"), source)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(staged.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte("CANARY-CLEAR")) || staged.Name != SectionCore || staged.CipherBytes != int64(len(content)) || staged.CipherSHA256 == "" {
		t.Fatalf("staged = %#v content=%q", staged, content)
	}
	info, _ := os.Stat(staged.Path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("staged mode = %o", info.Mode().Perm())
	}
}

func TestPackerFingerprintChangesWithBundleContentAndRejectsSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.mlink-backup")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := (Packer{}).Fingerprint(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := (Packer{}).Fingerprint(context.Background(), path)
	if err != nil || first == second {
		t.Fatalf("fingerprints/error = %s/%s/%v", first, second, err)
	}
	if _, cleanup, err := (Packer{}).Stage(context.Background(), path, first); err == nil {
		cleanup()
		t.Fatal("staging accepted bundle content that differed from the confirmed fingerprint")
	}
	link := filepath.Join(t.TempDir(), "bundle-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (Packer{}).Fingerprint(context.Background(), link); err == nil {
		t.Fatal("bundle symlink was accepted")
	}
}

func TestPackAndOpenRoundTripCleansCiphertextStaging(t *testing.T) {
	staging := t.TempDir()
	output := filepath.Join(t.TempDir(), "workspace.mlink-backup")
	passphrase := []byte("twelve-byte-passphrase")
	want := map[Section]string{
		SectionCore: "core-private", SectionKnowledge: "knowledge-private", SectionMLink: "mlink-private",
		SectionIdentity: "identity-private", SectionSecrets: "secrets-private", SectionAgents: "agents-private",
	}
	sources := make([]SectionSource, 0, len(want))
	for _, section := range []Section{SectionCore, SectionKnowledge, SectionMLink, SectionIdentity, SectionSecrets, SectionAgents} {
		section, content := section, want[section]
		sources = append(sources, SectionSource{Name: section, Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(content)), nil
		}})
	}
	manifestInput := fixtureManifest()
	if err := (Packer{StagingParent: staging}).Pack(context.Background(), output, passphrase, &manifestInput, sources...); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(staging)
	if len(entries) != 0 {
		t.Fatalf("staging entries = %#v", entries)
	}
	info, err := os.Lstat(output)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("output info/error = %#v/%v", info, err)
	}
	encrypted, _ := os.ReadFile(output)
	for _, clear := range want {
		if bytes.Contains(encrypted, []byte(clear)) {
			t.Fatalf("output leaked %q", clear)
		}
	}
	got := map[Section]string{}
	manifest, err := Open(context.Background(), output, passphrase, func(section Section, reader io.Reader) error {
		content, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		got[section] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ControlPlane.OwnerAgentID != "agt-owner" || manifest.Provider.InstanceID != "default" || len(manifest.Sections) != len(want) {
		t.Fatalf("manifest = %#v", manifest)
	}
	for section, content := range want {
		if got[section] != content {
			t.Fatalf("section %q = %q", section, got[section])
		}
	}
}

func TestPackRejectsMissingDuplicateAndUnknownSections(t *testing.T) {
	valid := fixtureSources()
	for name, sources := range map[string][]SectionSource{
		"missing required": valid[:len(valid)-1],
		"duplicate":        append(append([]SectionSource(nil), valid...), valid[0]),
		"unknown":          append(append([]SectionSource(nil), valid...), SectionSource{Name: Section("unknown"), Open: valid[0].Open}),
	} {
		t.Run(name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "bundle.mlink-backup")
			manifest := fixtureManifest()
			if err := (Packer{StagingParent: t.TempDir()}).Pack(context.Background(), output, []byte("twelve-byte-passphrase"), &manifest, sources...); err == nil {
				t.Fatal("Pack() error = nil")
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("output exists after refusal: %v", err)
			}
		})
	}
}

func TestPackRefusesSymlinkOutput(t *testing.T) {
	directory := t.TempDir()
	victim := filepath.Join(directory, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "bundle.mlink-backup")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	manifest := fixtureManifest()
	if err := (Packer{StagingParent: t.TempDir()}).Pack(context.Background(), link, []byte("twelve-byte-passphrase"), &manifest, fixtureSources()...); err == nil {
		t.Fatal("Pack() accepted symlink output")
	}
	content, _ := os.ReadFile(victim)
	if string(content) != "keep" {
		t.Fatalf("victim = %q", content)
	}
}

func TestOpenRejectsWrongPassphraseAndTruncatedBundle(t *testing.T) {
	output := filepath.Join(t.TempDir(), "bundle.mlink-backup")
	passphrase := []byte("twelve-byte-passphrase")
	manifest := fixtureManifest()
	if err := (Packer{StagingParent: t.TempDir()}).Pack(context.Background(), output, passphrase, &manifest, fixtureSources()...); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), output, []byte("wrong-passphrase"), func(Section, io.Reader) error { return nil }); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong passphrase error = %v", err)
	}
	content, _ := os.ReadFile(output)
	truncated := filepath.Join(t.TempDir(), "truncated.mlink-backup")
	if err := os.WriteFile(truncated, content[:len(content)-16], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), truncated, passphrase, func(Section, io.Reader) error { return nil }); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("truncated error = %v", err)
	}
	tampered := append([]byte(nil), content...)
	tampered[len(tampered)-1] ^= 1
	tamperedPath := filepath.Join(t.TempDir(), "tampered.mlink-backup")
	if err := os.WriteFile(tamperedPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), tamperedPath, passphrase, func(Section, io.Reader) error { return nil }); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("outer tamper error = %v", err)
	}
}

func TestVerifyManifestRejectsIncompleteOrUnsafeValues(t *testing.T) {
	valid := fixtureManifest()
	if err := VerifyManifest(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Manifest){
		"format":   func(value *Manifest) { value.Format = "other" },
		"time":     func(value *Manifest) { value.CreatedAt = time.Time{} },
		"provider": func(value *Manifest) { value.Provider.ProviderID = "" },
		"instance": func(value *Manifest) { value.Provider.InstanceID = "../escape" },
		"control":  func(value *Manifest) { value.ControlPlane.OwnerAgentID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			if err := VerifyManifest(value); err == nil {
				t.Fatal("VerifyManifest() error = nil")
			}
		})
	}
}

func fixtureManifest() Manifest {
	return Manifest{
		Format: FormatV1, CreatedAt: time.Unix(10, 0).UTC(),
		MLink: version.Info{Version: "0.1.0", Commit: "abc", GOOS: "darwin", GOARCH: "arm64", SchemaMin: 2, SchemaMax: 3},
		Provider: ProviderManifest{
			ProviderID: "dev.mlink.tencentdb", DriverVersion: "1", InstanceID: "default",
			CoreImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			HubImageDigest:  "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		ControlPlane: ControlPlaneManifest{
			InstallationID: "personal", InstanceID: "default", OwnerUserID: "usr-owner", OwnerTeamID: "team-owner",
			OwnerAgentID: "agt-owner", OwnerAssetID: "chat_memory-team-owner-agt-owner", DynamicAgentLimit: 500,
		},
		Agents: []string{"codex", "cursor"},
	}
}

func fixtureSources() []SectionSource {
	sections := []Section{SectionCore, SectionMLink, SectionIdentity, SectionSecrets, SectionAgents}
	result := make([]SectionSource, 0, len(sections))
	for _, section := range sections {
		section := section
		result = append(result, SectionSource{Name: section, Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(string(section) + "-content")), nil
		}})
	}
	return result
}
