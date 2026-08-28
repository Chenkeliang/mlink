package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"mlink/internal/app"
)

type configDiffApplication interface {
	ConfigDiff(context.Context) (app.DriftReport, error)
}

func runConfig(ctx context.Context, args []string, deps Dependencies) int {
	jsonOutput := slices.Equal(args, []string{"diff", "--json"})
	if !slices.Equal(args, []string{"diff"}) && !jsonOutput {
		writeLine(deps.Stderr, "invalid mlink config command")
		return 2
	}
	application, ok := deps.App.(configDiffApplication)
	if !ok {
		writeLine(deps.Stderr, "mlink config diff is unavailable")
		return 1
	}
	report, err := application.ConfigDiff(ctx)
	if err != nil {
		writeLine(deps.Stderr, "mlink config diff failed")
		return 1
	}
	if jsonOutput {
		encoder := json.NewEncoder(deps.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return 1
		}
		return 0
	}
	for _, resource := range report.Resources {
		_, _ = fmt.Fprintf(deps.Stdout, "%s\t%s\t%s\n", resource.State, resource.OwnerID, resource.Target)
	}
	return 0
}
