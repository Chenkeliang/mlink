package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestVersionJSONRendersMachineReadableBuildMetadata(t *testing.T) {
	stdout := &bytes.Buffer{}
	code := Run(context.Background(), []string{"version", "--json"}, Dependencies{Stdout: stdout, Stderr: io.Discard})
	if code != 0 || !strings.Contains(stdout.String(), `"schema_min": 2`) || !strings.Contains(stdout.String(), `"schema_max": 3`) || !strings.Contains(stdout.String(), `"go_version"`) {
		t.Fatalf("code/output = %d/%s", code, stdout)
	}
}
