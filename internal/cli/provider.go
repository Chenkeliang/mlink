package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"mlink/internal/install"
	"mlink/internal/provider/lifecycle"
)

type providerLifecycleApplication interface {
	ProviderStatus(context.Context) (lifecycle.BackendStatus, error)
	PlanProviderInstall(context.Context, lifecycle.BackendInstallRequest) (install.ChangeSet, error)
	ApplyProviderInstall(context.Context, string, lifecycle.BackendInstallRequest) error
}

func runProvider(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	application, ok := deps.App.(providerLifecycleApplication)
	if !ok || len(args) == 0 {
		return 2
	}
	if args[0] == "status" {
		jsonOutput := len(args) == 2 && args[1] == "--json"
		if len(args) != 1 && !jsonOutput {
			return 2
		}
		status, err := application.ProviderStatus(ctx)
		if err != nil {
			writeLine(deps.Stderr, "mlink Provider status failed")
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
		writeLine(deps.Stdout, status.ProviderID+"\t"+string(status.State)+"\t"+status.Endpoint)
		return 0
	}
	if len(args) < 2 || args[0] != "install" || args[1] != "tencentdb" {
		return 2
	}
	var request lifecycle.BackendInstallRequest
	request.ProviderID = "dev.mlink.tencentdb"
	var applyPlan string
	var yes, dryRun, jsonOutput, secretsStdin bool
	for index := 2; index < len(args); index++ {
		switch args[index] {
		case "--endpoint", "--llm-base-url", "--llm-model", "--apply-plan":
			flag := args[index]
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "--") {
				return 2
			}
			switch flag {
			case "--endpoint":
				request.Endpoint = args[index]
			case "--llm-base-url":
				request.LLMBaseURL = args[index]
			case "--llm-model":
				request.LLMModel = args[index]
			case "--apply-plan":
				applyPlan = args[index]
			}
		case "--secrets-stdin":
			secretsStdin = true
		case "--dry-run":
			dryRun = true
		case "--json":
			jsonOutput = true
		case "--yes":
			yes = true
		default:
			return 2
		}
	}
	if request.Endpoint == "" || request.LLMBaseURL == "" || request.LLMModel == "" || !secretsStdin || dryRun && applyPlan != "" || yes && applyPlan == "" {
		return 2
	}
	secretLine, err := readBoundedLine(input, 64*1024)
	if err != nil || len(secretLine) == 0 {
		wipeBytes(secretLine)
		return 2
	}
	var secretInput struct {
		GatewayToken string `json:"gateway_token"`
		LLMAPIKey    string `json:"llm_api_key"`
	}
	decoder := json.NewDecoder(bytes.NewReader(secretLine))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&secretInput)
	if decodeErr == nil {
		decodeErr = decoder.Decode(&struct{}{})
		if errors.Is(decodeErr, io.EOF) {
			decodeErr = nil
		}
	}
	wipeBytes(secretLine)
	if decodeErr != nil || secretInput.GatewayToken == "" || secretInput.LLMAPIKey == "" {
		return 2
	}
	request.GatewayToken = []byte(secretInput.GatewayToken)
	request.LLMAPIKey = []byte(secretInput.LLMAPIKey)
	secretInput.GatewayToken, secretInput.LLMAPIKey = "", ""
	defer request.Wipe()
	plan, err := application.PlanProviderInstall(ctx, request)
	if err != nil {
		writeLine(deps.Stderr, "mlink Provider install planning failed")
		return exitCodeFor(err)
	}
	if err := renderPlan(deps.Stdout, plan, jsonOutput); err != nil {
		return 1
	}
	if applyPlan == "" {
		return 0
	}
	if !yes || applyPlan != plan.PlanID {
		return 3
	}
	if err := application.ApplyProviderInstall(ctx, plan.PlanID, request); err != nil {
		writeLine(deps.Stderr, "mlink Provider install failed")
		return exitCodeFor(err)
	}
	return 0
}
