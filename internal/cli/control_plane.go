package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/install"
)

type controlPlaneApplication interface {
	PlanControlPlaneProvision(context.Context, int) (install.ChangeSet, error)
	ApplyControlPlaneProvision(context.Context, string, int) error
}

type controlPlaneBootstrapApplication interface {
	PlanControlPlaneBootstrap(context.Context, app.ControlPlaneBootstrapRequest) (install.ChangeSet, error)
	ApplyControlPlaneBootstrap(context.Context, string, app.ControlPlaneBootstrapRequest) error
}

func runControlPlane(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	application, ok := deps.App.(controlPlaneApplication)
	if !ok || len(args) == 0 || args[0] != "provision" {
		return 2
	}
	limit := 0
	var applyPlan, endpoint, serviceID, installationID, owner string
	var yes, dryRun, jsonOutput, secretsStdin bool
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "--dynamic-agent-limit", "--apply-plan", "--endpoint", "--service-id", "--installation-id", "--owner":
			flag := args[index]
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "--") {
				return 2
			}
			switch flag {
			case "--dynamic-agent-limit":
				limit, _ = strconv.Atoi(args[index])
			case "--apply-plan":
				applyPlan = args[index]
			case "--endpoint":
				endpoint = args[index]
			case "--service-id":
				serviceID = args[index]
			case "--installation-id":
				installationID = args[index]
			case "--owner":
				owner = args[index]
			}
		case "--secrets-stdin":
			secretsStdin = true
		case "--yes":
			yes = true
		case "--dry-run":
			dryRun = true
		case "--json":
			jsonOutput = true
		default:
			return 2
		}
	}
	if limit <= 0 || limit > 10_000 || dryRun && applyPlan != "" || yes && applyPlan == "" {
		return 2
	}
	bootstrapRequested := endpoint != "" || serviceID != "" || installationID != "" || owner != "" || secretsStdin
	var bootstrapRequest app.ControlPlaneBootstrapRequest
	var plan install.ChangeSet
	var err error
	bootstrap, bootstrapOK := deps.App.(controlPlaneBootstrapApplication)
	if bootstrapRequested {
		if !bootstrapOK || endpoint == "" || serviceID == "" || installationID == "" || owner == "" || !secretsStdin {
			return 2
		}
		secretLine, readErr := readBoundedLine(input, 32*1024)
		if readErr != nil || len(secretLine) == 0 {
			wipeBytes(secretLine)
			return 2
		}
		var protected struct {
			GatewayToken string `json:"gateway_token"`
		}
		decoder := json.NewDecoder(bytes.NewReader(secretLine))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&protected)
		if decodeErr == nil {
			decodeErr = decoder.Decode(&struct{}{})
			if errors.Is(decodeErr, io.EOF) {
				decodeErr = nil
			}
		}
		wipeBytes(secretLine)
		if decodeErr != nil || protected.GatewayToken == "" {
			return 2
		}
		bootstrapRequest = app.ControlPlaneBootstrapRequest{
			Connection: config.Connection{
				ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "bootstrap-1",
				ProviderConfig: map[string]any{"base_url": endpoint, "service_id": serviceID, "timeout_ms": 5000}, TenantID: installationID,
			},
			OwnerSlug: owner, GatewayToken: []byte(protected.GatewayToken), DynamicAgentLimit: limit,
		}
		protected.GatewayToken = ""
		defer bootstrapRequest.Wipe()
		plan, err = bootstrap.PlanControlPlaneBootstrap(ctx, bootstrapRequest)
	} else {
		plan, err = application.PlanControlPlaneProvision(ctx, limit)
	}
	if err != nil {
		writeLine(deps.Stderr, "mlink control-plane planning failed")
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
	if bootstrapRequested {
		err = bootstrap.ApplyControlPlaneBootstrap(ctx, plan.PlanID, bootstrapRequest)
	} else {
		err = application.ApplyControlPlaneProvision(ctx, plan.PlanID, limit)
	}
	if err != nil {
		writeLine(deps.Stderr, "mlink control-plane provision failed")
		return exitCodeFor(err)
	}
	return 0
}
