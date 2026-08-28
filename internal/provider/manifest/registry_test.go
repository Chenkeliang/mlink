package manifest

import "testing"

func TestResolveBundledTencentDB(t *testing.T) {
	manifest := Manifest{
		ProviderID:       "dev.mlink.tencentdb",
		Version:          "0.1.0",
		Entrypoint:       []string{"mlink", "provider", "run", "tencentdb"},
		ExecutableSHA256: "bundled",
	}
	got, err := ResolveBundled(manifest, "/Applications/MLink/mlink")
	if err != nil {
		t.Fatalf("ResolveBundled() error = %v", err)
	}
	want := []string{"/Applications/MLink/mlink", "provider", "run", "tencentdb"}
	if len(got) != len(want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveBundledRejectsUnregisteredCommand(t *testing.T) {
	base := Manifest{
		ProviderID:       "dev.mlink.tencentdb",
		Version:          "0.1.0",
		Entrypoint:       []string{"mlink", "provider", "run", "tencentdb"},
		ExecutableSHA256: "bundled",
	}
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "wrong provider", mutate: func(m *Manifest) { m.ProviderID = "dev.mlink.other" }},
		{name: "wrong version", mutate: func(m *Manifest) { m.Version = "0.2.0" }},
		{name: "wrong subcommand", mutate: func(m *Manifest) { m.Entrypoint = []string{"mlink", "doctor"} }},
		{name: "extra argument", mutate: func(m *Manifest) { m.Entrypoint = append(m.Entrypoint, "--unsafe") }},
		{name: "not bundled", mutate: func(m *Manifest) { m.ExecutableSHA256 = "abc123" }},
		{name: "empty executable", mutate: func(*Manifest) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := base
			manifest.Entrypoint = append([]string(nil), base.Entrypoint...)
			tt.mutate(&manifest)
			executable := "/Applications/MLink/mlink"
			if tt.name == "empty executable" {
				executable = ""
			}
			if _, err := ResolveBundled(manifest, executable); err == nil {
				t.Fatal("ResolveBundled() error = nil, want registry rejection")
			}
		})
	}
}
