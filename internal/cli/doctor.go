package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"mlink/internal/app"
	"mlink/internal/doctor"
)

type doctorApplication interface {
	Doctor(context.Context, []app.Agent) (doctor.Report, error)
}

func runDoctor(ctx context.Context, args []string, deps Dependencies) int {
	jsonOutput := false
	var names []string
	for _, argument := range args {
		if argument == "--json" {
			if jsonOutput {
				writeLine(deps.Stderr, "invalid mlink doctor arguments")
				return 2
			}
			jsonOutput = true
			continue
		}
		names = append(names, argument)
	}
	if len(names) > 1 {
		writeLine(deps.Stderr, "invalid mlink doctor arguments")
		return 2
	}
	var agents []app.Agent
	if len(names) == 1 {
		parsed, err := parseAgents(names)
		if err != nil {
			writeLine(deps.Stderr, "invalid mlink doctor Agent")
			return 2
		}
		agents = parsed
	}
	application, ok := deps.App.(doctorApplication)
	if !ok {
		writeLine(deps.Stderr, "mlink doctor is unavailable")
		return 1
	}
	report, err := application.Doctor(ctx, agents)
	if err != nil {
		writeLine(deps.Stderr, "mlink doctor failed")
		return 1
	}
	if jsonOutput {
		encoder := json.NewEncoder(deps.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return 1
		}
	} else {
		for _, check := range report.Checks {
			_, _ = fmt.Fprintf(deps.Stdout, "%s\t%s\t%s\n", check.ID, check.State, check.Code)
		}
	}
	return report.ExitCode()
}
