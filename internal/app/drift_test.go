package app

import (
	"context"
	"testing"

	"mlink/internal/install"
)

func TestConfigDiffDistinguishesMatchingModifiedAndMissingResources(t *testing.T) {
	service, target, _ := newInstallFixture(t)
	service.Ledger.(*memoryLedger).owned = []install.OwnedResource{
		{OwnerID: "owner", Target: "/matching", PostApplyHash: contentHash([]byte("same"))},
		{OwnerID: "owner", Target: "/modified", PostApplyHash: contentHash([]byte("before"))},
		{OwnerID: "owner", Target: "/missing", PostApplyHash: contentHash([]byte("gone"))},
	}
	target.files["/matching"] = memoryFile{content: []byte("same"), mode: 0o600}
	target.files["/modified"] = memoryFile{content: []byte("after"), mode: 0o600}
	report, err := service.ConfigDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []DriftState{DriftMatching, DriftModified, DriftMissing}
	if len(report.Resources) != len(want) {
		t.Fatalf("report = %#v", report)
	}
	for index := range want {
		if report.Resources[index].State != want[index] {
			t.Fatalf("resource %d = %#v", index, report.Resources[index])
		}
	}
}

func TestConfigDiffDeduplicatesHistoricalOwnersAndIgnoresServiceActions(t *testing.T) {
	service, target, _ := newInstallFixture(t)
	service.Ledger.(*memoryLedger).owned = []install.OwnedResource{
		{OwnerID: "dev.mlink.restore", Target: "/shared", PostApplyHash: contentHash(nil)},
		{OwnerID: "dev.mlink.config", Target: "/shared", PostApplyHash: contentHash([]byte("current"))},
		{OwnerID: "dev.mlink.broker", Target: "service:kickstart:dev.mlink.broker", PostApplyHash: contentHash(nil)},
	}
	target.files["/shared"] = memoryFile{content: []byte("current"), mode: 0o600}
	report, err := service.ConfigDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Resources) != 1 || report.Resources[0].Target != "/shared" || report.Resources[0].OwnerID != "dev.mlink.config" || report.Resources[0].State != DriftMatching {
		t.Fatalf("report = %#v", report)
	}
}
