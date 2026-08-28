package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildEnvironmentInheritsOnlyLocaleAndPath(t *testing.T) {
	revisionDir := filepath.Join(t.TempDir(), "revision-a")
	environment, paths, err := buildEnvironment([]string{
		"PATH=/usr/bin:/bin",
		"LANG=zh_CN.UTF-8",
		"LC_ALL=zh_CN.UTF-8",
		"HOME=/Users/private",
		"ANTHROPIC_BASE_URL=https://model.example",
		"OPENAI_API_KEY=agent-secret",
		"MLINK_TEST_MEMORYCORE_TOKEN=backend-secret",
	}, revisionDir)
	if err != nil {
		t.Fatalf("buildEnvironment() error = %v", err)
	}
	joined := strings.Join(environment, "\n")
	for _, want := range []string{"PATH=/usr/bin:/bin", "LANG=zh_CN.UTF-8", "LC_ALL=zh_CN.UTF-8", "HOME=" + paths.Home, "TMPDIR=" + paths.Temp} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment = %q, missing %q", joined, want)
		}
	}
	for _, forbidden := range []string{"/Users/private", "ANTHROPIC_BASE_URL", "OPENAI_API_KEY", "agent-secret", "backend-secret"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("environment leaked %q: %s", forbidden, joined)
		}
	}
	for _, path := range []string{revisionDir, paths.Home, paths.Temp} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("mode(%s) = %o, want 0700", path, info.Mode().Perm())
		}
	}
}

func TestBuildEnvironmentUsesLastAllowedValueOnce(t *testing.T) {
	environment, _, err := buildEnvironment([]string{"PATH=/first", "PATH=/second", "LANG=C"}, filepath.Join(t.TempDir(), "revision"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(environment, "\n")
	if strings.Count(joined, "PATH=") != 1 || !strings.Contains(joined, "PATH=/second") {
		t.Fatalf("environment = %q", joined)
	}
}
