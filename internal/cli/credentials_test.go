package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"mlink/internal/app"
)

func TestCredentialsStatusJSONIsRedacted(t *testing.T) {
	application := &fakeApplication{credentialStatuses: []app.CredentialStatus{{
		Role: app.CredentialPanelOwner, Present: true, Fingerprint: "0123456789ab", CopyAllowed: true,
	}}}
	stdout := &bytes.Buffer{}
	code := Run(context.Background(), []string{"credentials", "status", "--json"}, Dependencies{App: application, Stdout: stdout, Stderr: io.Discard})
	if code != 0 || !strings.Contains(stdout.String(), `"fingerprint": "0123456789ab"`) || strings.Contains(stdout.String(), "owner-secret") {
		t.Fatalf("code/output = %d/%s", code, stdout)
	}
}

func TestCredentialsCopyRequiresPanelRoleAndConfirmation(t *testing.T) {
	application := &fakeApplication{}
	stdout := &bytes.Buffer{}
	code := Run(context.Background(), []string{"credentials", "copy", "panel-owner", "--yes"}, Dependencies{App: application, Stdout: stdout, Stderr: io.Discard})
	if code != 0 || application.credentialCopies != 1 || application.copiedCredential != app.CredentialPanelOwner {
		t.Fatalf("code/copies/role = %d/%d/%s", code, application.credentialCopies, application.copiedCredential)
	}
	if !strings.Contains(stdout.String(), "clipboard") || strings.Contains(stdout.String(), "owner-secret") {
		t.Fatalf("copy output = %s", stdout)
	}
	for _, args := range [][]string{{"credentials", "copy", "panel-owner"}, {"credentials", "copy", "gateway", "--yes"}} {
		if code := Run(context.Background(), args, Dependencies{App: application, Stdout: io.Discard, Stderr: io.Discard}); code != 2 {
			t.Fatalf("Run(%q) = %d", args, code)
		}
	}
}
