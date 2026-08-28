package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"mlink/internal/app"
)

type statusApplication interface {
	Status(context.Context) (app.Status, error)
}

func runStatus(ctx context.Context, args []string, deps Dependencies) int {
	jsonOutput := slices.Equal(args, []string{"--json"})
	if len(args) != 0 && !jsonOutput {
		writeLine(deps.Stderr, "invalid mlink status arguments")
		return 2
	}
	application, ok := deps.App.(statusApplication)
	if !ok {
		writeLine(deps.Stderr, "mlink status is unavailable")
		return 1
	}
	status, err := application.Status(ctx)
	if err != nil {
		writeLine(deps.Stderr, "mlink status failed")
		return 1
	}
	if jsonOutput {
		encoder := json.NewEncoder(deps.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(status); err != nil {
			return 1
		}
		return 0
	}
	state := "not installed"
	if status.Installed {
		state = "installed"
	}
	_, _ = fmt.Fprintf(deps.Stdout, "MLink\t%s\nConnection\t%s\nPlan\t%s\n", state, emptyDash(status.ConnectionID), emptyDash(status.ActivePlanID))
	for _, agent := range []app.Agent{app.Codex, app.Pi, app.Hermes} {
		_, _ = fmt.Fprintf(deps.Stdout, "%s\t%t\n", agent, status.Adapters[agent])
	}
	return 0
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
