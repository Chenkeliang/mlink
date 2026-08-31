package layout

import "testing"

func TestFromHomeUsesStableUserPaths(t *testing.T) {
	p, err := FromHome("/Users/test", "/tmp/mlink-build")
	if err != nil {
		t.Fatal(err)
	}
	if p.Home != "/Users/test/.mlink" {
		t.Fatalf("Home = %q", p.Home)
	}
	if p.Binary != "/Users/test/.local/bin/mlink" {
		t.Fatalf("Binary = %q", p.Binary)
	}
	if p.Socket != "/Users/test/.mlink/run/mlink.sock" {
		t.Fatalf("Socket = %q", p.Socket)
	}
	if p.SourceExecutable != "/tmp/mlink-build" {
		t.Fatalf("SourceExecutable = %q", p.SourceExecutable)
	}
	if p.PanelHome != "/Users/test/.mlink/panel" || p.PanelRegistry != "/Users/test/.mlink/panel/metadata-instances.json" {
		t.Fatalf("Panel paths = %q/%q", p.PanelHome, p.PanelRegistry)
	}
}

func TestFromHomeRejectsRelativeInputs(t *testing.T) {
	for _, tc := range []struct {
		home       string
		executable string
	}{
		{home: "relative", executable: "/tmp/mlink"},
		{home: "/Users/test", executable: "relative"},
	} {
		if _, err := FromHome(tc.home, tc.executable); err == nil {
			t.Fatalf("FromHome(%q, %q) error = nil", tc.home, tc.executable)
		}
	}
}
