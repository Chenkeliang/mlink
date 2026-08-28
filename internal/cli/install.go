package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"mlink/internal/app"
	"mlink/internal/install"
	"mlink/internal/journal"
)

type mutationOptions struct {
	dryRun      bool
	json        bool
	yes         bool
	applyPlan   string
	tokenStdin  bool
	positionals []string
}

func runInstall(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	if deps.App == nil {
		writeLine(deps.Stderr, "mlink install is unavailable")
		return 1
	}
	options, err := parseMutationOptions(args, true)
	if err != nil {
		writeLine(deps.Stderr, "invalid mlink install arguments")
		return 2
	}
	request := cloneInstallRequest(deps.InstallRequest)
	request.Agents, err = parseAgents(options.positionals)
	if err != nil {
		writeLine(deps.Stderr, "invalid mlink Agent selection")
		return 2
	}
	if request.SecretInputs == nil {
		request.SecretInputs = make(map[string][]byte)
	}
	if options.tokenStdin {
		token, err := readBoundedLine(input, 16*1024)
		if err != nil || len(token) == 0 {
			wipeBytes(token)
			writeLine(deps.Stderr, "invalid MemoryCore token input")
			return 2
		}
		request.SecretInputs[app.MemoryCoreTokenSecret] = token
	}
	defer wipeBytes(request.SecretInputs[app.MemoryCoreTokenSecret])

	plan, err := deps.App.PlanInstall(ctx, request)
	if err != nil {
		writeLine(deps.Stderr, "mlink install planning failed")
		return exitCodeFor(err)
	}
	if err := renderPlan(deps.Stdout, plan, options.json); err != nil {
		writeLine(deps.Stderr, "mlink install preview failed")
		return 1
	}
	if options.dryRun || options.json && options.applyPlan == "" {
		return 0
	}
	if options.yes && options.applyPlan == "" {
		writeLine(deps.Stderr, "--yes requires --apply-plan")
		return 2
	}
	if options.applyPlan != "" {
		if !options.yes {
			writeLine(deps.Stderr, "--apply-plan requires --yes")
			return 2
		}
		if options.applyPlan != plan.PlanID {
			writeLine(deps.Stderr, "mlink install plan is stale")
			return 3
		}
	} else {
		confirmed, err := confirmApply(input, deps.Stdout)
		if err != nil {
			writeLine(deps.Stderr, "mlink install confirmation failed")
			return 1
		}
		if !confirmed {
			return 0
		}
	}
	if err := deps.App.ApplyInstall(ctx, plan.PlanID, request); err != nil {
		writeLine(deps.Stderr, "mlink install failed")
		return exitCodeFor(err)
	}
	return 0
}

func parseMutationOptions(args []string, allowToken bool) (mutationOptions, error) {
	var options mutationOptions
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--dry-run":
			options.dryRun = true
		case "--json":
			options.json = true
		case "--yes":
			options.yes = true
		case "--memorycore-token-stdin":
			if !allowToken {
				return mutationOptions{}, errors.New("token input is not valid for this command")
			}
			options.tokenStdin = true
		case "--apply-plan":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "--") {
				return mutationOptions{}, errors.New("--apply-plan requires a value")
			}
			options.applyPlan = args[index]
		default:
			if strings.HasPrefix(args[index], "-") {
				return mutationOptions{}, fmt.Errorf("unknown option %q", args[index])
			}
			options.positionals = append(options.positionals, args[index])
		}
	}
	if options.dryRun && options.applyPlan != "" {
		return mutationOptions{}, errors.New("dry-run cannot apply")
	}
	return options, nil
}

func parseAgents(values []string) ([]app.Agent, error) {
	if len(values) == 0 {
		return []app.Agent{app.Codex, app.Pi, app.Hermes}, nil
	}
	agents := make([]app.Agent, 0, len(values))
	for _, value := range values {
		switch app.Agent(value) {
		case app.Codex, app.Pi, app.Hermes:
			agents = append(agents, app.Agent(value))
		default:
			return nil, fmt.Errorf("unknown Agent %q", value)
		}
	}
	return agents, nil
}

func confirmApply(input *bufio.Reader, output io.Writer) (bool, error) {
	if output != nil {
		_, _ = io.WriteString(output, "Apply? [y/N] ")
	}
	line, err := readBoundedLine(input, 64)
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(string(line)))
	return answer == "y" || answer == "yes", nil
}

func readBoundedLine(input *bufio.Reader, limit int) ([]byte, error) {
	if input == nil {
		return nil, io.EOF
	}
	line, err := input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if len(line) > limit {
		return nil, errors.New("input is too long")
	}
	return []byte(line), nil
}

func cloneInstallRequest(input app.InstallRequest) app.InstallRequest {
	cloned := input
	cloned.Agents = append([]app.Agent(nil), input.Agents...)
	cloned.SecretInputs = make(map[string][]byte, len(input.SecretInputs))
	for key, value := range input.SecretInputs {
		cloned.SecretInputs[key] = append([]byte(nil), value...)
	}
	return cloned
}

func wipeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func exitCodeFor(err error) int {
	if errors.Is(err, install.ErrPlanStale) || errors.Is(err, app.ErrOwnedResourceChanged) || errors.Is(err, journal.ErrBlockingEvents) {
		return 3
	}
	return 1
}
