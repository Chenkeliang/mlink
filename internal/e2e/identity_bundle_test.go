package e2e

import (
	"bytes"
	"context"
	"testing"

	"mlink/internal/identity"
)

func TestInstalledIdentityExportsWithStableCanonicalOwner(t *testing.T) {
	service, _, request := fixture(t)
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	encrypted, err := service.ExportIdentity(context.Background(), []byte("passphrase-12"), bytes.NewReader(bytes.Repeat([]byte{0x31}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := identity.DecryptBundle(encrypted, []byte("passphrase-12"))
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Wipe()
	if bundle.SchemaVersion != 2 || bundle.Principals["owner"].CanonicalUserID != "usr-owner-generated" || len(bundle.Bindings) != 1 {
		t.Fatal("identity Bundle did not preserve owner")
	}
}
