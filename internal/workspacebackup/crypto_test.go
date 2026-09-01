package workspacebackup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"mlink/internal/version"
)

func TestEncryptedEnvelopeRoundTripAndAuthentication(t *testing.T) {
	passphrase := []byte("correct horse battery")
	var encrypted bytes.Buffer
	if err := Encrypt(&encrypted, passphrase, strings.NewReader("private workspace")); err != nil {
		t.Fatal(err)
	}
	header, err := InspectHeader(bytes.NewReader(encrypted.Bytes()))
	if err != nil || header.Format != FormatV1 {
		t.Fatalf("header/error = %#v/%v", header, err)
	}
	var clear bytes.Buffer
	if err := Decrypt(&clear, passphrase, bytes.NewReader(encrypted.Bytes())); err != nil {
		t.Fatal(err)
	}
	if clear.String() != "private workspace" {
		t.Fatalf("clear = %q", clear.String())
	}

	tampered := append([]byte(nil), encrypted.Bytes()...)
	tampered[len(tampered)-1] ^= 1
	if err := Decrypt(io.Discard, passphrase, bytes.NewReader(tampered)); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("tamper error = %v", err)
	}
	if err := Decrypt(io.Discard, []byte("wrong passphrase"), bytes.NewReader(encrypted.Bytes())); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong passphrase error = %v", err)
	}
	if err := Decrypt(io.Discard, passphrase, bytes.NewReader(encrypted.Bytes()[:len(encrypted.Bytes())-8])); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("truncated error = %v", err)
	}
}

func TestEncryptedEnvelopeRejectsUnsafeInputs(t *testing.T) {
	for name, testCase := range map[string]struct {
		destination io.Writer
		passphrase  []byte
		source      io.Reader
	}{
		"nil destination":  {passphrase: []byte("twelve-byte-passphrase"), source: strings.NewReader("x")},
		"short passphrase": {destination: io.Discard, passphrase: []byte("short"), source: strings.NewReader("x")},
		"long passphrase":  {destination: io.Discard, passphrase: bytes.Repeat([]byte{'x'}, 4097), source: strings.NewReader("x")},
		"nil source":       {destination: io.Discard, passphrase: []byte("twelve-byte-passphrase")},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Encrypt(testCase.destination, testCase.passphrase, testCase.source); err == nil {
				t.Fatal("Encrypt() error = nil")
			}
		})
	}
	if _, err := InspectHeader(strings.NewReader("not-a-backup")); !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("InspectHeader() error = %v", err)
	}
}

func TestManifestFormattingRedactsIdentity(t *testing.T) {
	manifest := Manifest{
		Format: FormatV1, CreatedAt: time.Unix(1, 0).UTC(), MLink: version.Info{Version: "1.0.0"},
		Provider: ProviderManifest{ProviderID: "dev.mlink.tencentdb", InstanceID: "instance-secret"},
		ControlPlane: ControlPlaneManifest{
			OwnerUserID: "usr-secret", OwnerTeamID: "team-secret", OwnerAgentID: "agt-secret", OwnerAssetID: "asset-secret",
		},
		PrincipalAgents: []PrincipalAgentManifest{{Fingerprint: "prn_abcdefghijklmnopqrstuvwxyz", BackendAgentID: "dynamic-secret"}},
	}
	for _, rendered := range []string{fmt.Sprint(manifest), fmt.Sprintf("%#v", manifest)} {
		for _, forbidden := range []string{"instance-secret", "usr-secret", "team-secret", "agt-secret", "asset-secret", "dynamic-secret", "abcdefghijklmnopqrstuvwxyz"} {
			if strings.Contains(rendered, forbidden) {
				t.Fatalf("manifest leaked %q: %s", forbidden, rendered)
			}
		}
		if !strings.Contains(rendered, "<redacted>") {
			t.Fatalf("manifest has no redaction marker: %s", rendered)
		}
	}
}

func TestSectionAndVolumeKindsRejectUnknownValues(t *testing.T) {
	if Section("unknown").Valid() || VolumeKind("unknown").Valid() {
		t.Fatal("unknown manifest enum was accepted")
	}
	for _, section := range []Section{SectionManifestData, SectionChecksums, SectionCore, SectionKnowledge, SectionMLink, SectionIdentity, SectionSecrets, SectionAgents} {
		if !section.Valid() {
			t.Fatalf("section %q is invalid", section)
		}
	}
}
