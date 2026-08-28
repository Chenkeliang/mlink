package tencentdb

import (
	"reflect"
	"testing"

	"mlink/internal/provider/manifest"
)

func TestBundledManifestMatchesRegisteredCommand(t *testing.T) {
	bundled := BundledManifest()
	if bundled.ProviderID != "dev.mlink.tencentdb" || bundled.Version != "0.1.0" {
		t.Fatalf("identity = %s@%s", bundled.ProviderID, bundled.Version)
	}
	argv, err := manifest.ResolveBundled(bundled, "/Applications/MLink/mlink")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/Applications/MLink/mlink", "provider", "run", "tencentdb"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %#v, want %#v", argv, want)
	}
	if bundled.DeclaredCapabilities["capture_turn"].ReplaySafe {
		t.Fatal("TencentDB capture must not claim replay safety")
	}
}
