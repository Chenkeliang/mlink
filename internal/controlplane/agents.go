package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"mlink/internal/journal"
	"mlink/internal/provider/tencentdb"
	"mlink/internal/secret"
)

const (
	agentListPageSize  = 100
	maxRemoteAgentScan = 10_000
)

var (
	principalFingerprintPattern = regexp.MustCompile(`^prn_[a-z2-7]{26}$`)
	forbiddenExternalIDPattern  = regexp.MustCompile(`(?i)(?:ou_|oc_|on_|union_id|open_id|chat_id|thread_id)`)
)

type PrincipalIntent struct {
	Fingerprint  string
	RouteKind    string
	DisplayLabel string
}

type PrincipalAgentStore interface {
	LoadControlPlane(context.Context) (journal.ControlPlaneState, error)
	GetPrincipalAgent(context.Context, string) (journal.PrincipalAgent, error)
	PutPrincipalAgent(context.Context, journal.PrincipalAgent) error
	ListPrincipalAgents(context.Context) ([]journal.PrincipalAgent, error)
}

type AgentProvisioner struct {
	Metadata         tencentdb.MetadataClient
	Secrets          secret.Store
	Store            PrincipalAgentStore
	MaxDynamicAgents int
	Timeout          time.Duration

	mu      sync.Mutex
	flights map[string]*agentFlight
}

type agentFlight struct {
	route  string
	done   chan struct{}
	result journal.PrincipalAgent
	err    error
}

func (provisioner *AgentProvisioner) ResolveOrCreate(ctx context.Context, intent PrincipalIntent) (journal.PrincipalAgent, error) {
	clean, err := validatePrincipalIntent(intent)
	if err != nil {
		return journal.PrincipalAgent{}, err
	}
	if provisioner == nil || provisioner.Metadata == nil || provisioner.Secrets == nil || provisioner.Store == nil {
		return journal.PrincipalAgent{}, errors.New("dynamic Agent provisioner is unavailable")
	}
	if provisioner.MaxDynamicAgents <= 0 {
		return journal.PrincipalAgent{}, errors.New("explicit positive dynamic Agent limit is required")
	}
	if provisioner.Timeout <= 0 {
		return journal.PrincipalAgent{}, errors.New("positive dynamic Agent provisioning timeout is required")
	}
	ctx, cancel := context.WithTimeout(ctx, provisioner.Timeout)
	defer cancel()

	provisioner.mu.Lock()
	if provisioner.flights == nil {
		provisioner.flights = map[string]*agentFlight{}
	}
	if flight, ok := provisioner.flights[clean.Fingerprint]; ok {
		if flight.route != clean.RouteKind {
			provisioner.mu.Unlock()
			return journal.PrincipalAgent{}, errors.New("in-flight principal Agent route conflicts with requested route")
		}
		provisioner.mu.Unlock()
		select {
		case <-flight.done:
			return flight.result, flight.err
		case <-ctx.Done():
			return journal.PrincipalAgent{}, ctx.Err()
		}
	}
	flight := &agentFlight{route: clean.RouteKind, done: make(chan struct{})}
	provisioner.flights[clean.Fingerprint] = flight
	provisioner.mu.Unlock()

	flight.result, flight.err = provisioner.resolveOrCreate(ctx, clean)
	close(flight.done)
	provisioner.mu.Lock()
	delete(provisioner.flights, clean.Fingerprint)
	provisioner.mu.Unlock()
	return flight.result, flight.err
}

func (provisioner *AgentProvisioner) resolveOrCreate(ctx context.Context, intent PrincipalIntent) (journal.PrincipalAgent, error) {
	if existing, err := provisioner.Store.GetPrincipalAgent(ctx, intent.Fingerprint); err == nil {
		if existing.RouteKind != intent.RouteKind {
			return journal.PrincipalAgent{}, errors.New("existing principal Agent mapping conflicts with requested route")
		}
		if existing.State == "active" {
			return existing, nil
		}
	} else if !errors.Is(err, journal.ErrPrincipalAgentNotFound) {
		return journal.PrincipalAgent{}, fmt.Errorf("load principal Agent mapping: %w", err)
	}

	state, err := provisioner.Store.LoadControlPlane(ctx)
	if err != nil {
		return journal.PrincipalAgent{}, fmt.Errorf("load Owner control plane: %w", err)
	}
	if state.State != "provisioned" && state.State != "active" {
		return journal.PrincipalAgent{}, errors.New("Owner control plane is not provisioned")
	}
	ownerKey, err := provisioner.Secrets.Get(ctx, OwnerUserKeyAccount)
	if err != nil {
		return journal.PrincipalAgent{}, fmt.Errorf("load TencentDB Owner credential: %w", err)
	}
	defer wipe(ownerKey)

	remote, remoteDynamicCount, err := provisioner.findRemote(ctx, ownerKey, state, intent)
	if err != nil {
		return journal.PrincipalAgent{}, err
	}
	if remote.AgentID != "" {
		return provisioner.persistRemote(ctx, ownerKey, state, intent, remote)
	}
	localMappings, err := provisioner.Store.ListPrincipalAgents(ctx)
	if err != nil {
		return journal.PrincipalAgent{}, fmt.Errorf("count local dynamic Agents: %w", err)
	}
	localDynamicCount := 0
	for _, mapping := range localMappings {
		if mapping.State == "active" || mapping.State == "provisioning" {
			localDynamicCount++
		}
	}
	if localDynamicCount >= provisioner.MaxDynamicAgents || remoteDynamicCount >= provisioner.MaxDynamicAgents {
		return journal.PrincipalAgent{}, fmt.Errorf("dynamic Agent limit %d reached", provisioner.MaxDynamicAgents)
	}

	marker := principalMetadataMarker(state.InstallationID, intent)
	agent, err := provisioner.Metadata.CreateAgent(ctx, ownerKey, tencentdb.CreateAgentRequest{
		TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID, Name: principalAgentName(intent),
		Description: "MLink isolated " + intent.RouteKind + " memory", Visibility: "private", MetadataJSON: marker,
	})
	if err != nil {
		return journal.PrincipalAgent{}, fmt.Errorf("create TencentDB dynamic Agent: %w", err)
	}
	if err := validateRemoteAgent(agent, state, marker); err != nil {
		return journal.PrincipalAgent{}, err
	}
	return provisioner.persistRemote(ctx, ownerKey, state, intent, agent)
}

func (provisioner *AgentProvisioner) findRemote(ctx context.Context, ownerKey []byte, state journal.ControlPlaneState, intent PrincipalIntent) (tencentdb.Agent, int, error) {
	var matched tencentdb.Agent
	dynamicCount := 0
	offset := 0
	for offset < maxRemoteAgentScan {
		agents, err := provisioner.Metadata.ListAgents(ctx, ownerKey, tencentdb.ListAgentsRequest{
			TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID, Limit: agentListPageSize, Offset: offset,
		})
		if err != nil {
			return tencentdb.Agent{}, 0, fmt.Errorf("list TencentDB Owner Agents: %w", err)
		}
		for _, agent := range agents {
			marker, ok := parsePrincipalMetadataMarker(agent.MetadataJSON)
			if !ok || marker.InstallationID != state.InstallationID {
				continue
			}
			dynamicCount++
			if marker.Fingerprint != intent.Fingerprint {
				continue
			}
			if marker.RouteKind != intent.RouteKind {
				return tencentdb.Agent{}, 0, errors.New("remote principal Agent marker conflicts with requested route")
			}
			if matched.AgentID != "" {
				return tencentdb.Agent{}, 0, errors.New("duplicate remote principal Agent marker")
			}
			if err := validateRemoteAgent(agent, state, principalMetadataMarker(state.InstallationID, intent)); err != nil {
				return tencentdb.Agent{}, 0, err
			}
			matched = agent
		}
		if len(agents) < agentListPageSize {
			return matched, dynamicCount, nil
		}
		offset += len(agents)
	}
	return tencentdb.Agent{}, 0, errors.New("TencentDB Agent scan exceeded safe bound")
}

func (provisioner *AgentProvisioner) persistRemote(ctx context.Context, ownerKey []byte, state journal.ControlPlaneState, intent PrincipalIntent, agent tencentdb.Agent) (journal.PrincipalAgent, error) {
	assetID := "chat_memory-" + state.OwnerTeamID + "-" + agent.AgentID
	asset, err := provisioner.Metadata.GetAsset(ctx, ownerKey, assetID)
	if err != nil {
		return journal.PrincipalAgent{}, fmt.Errorf("load dynamic Agent Chat Memory Asset: %w", err)
	}
	if asset.AssetID != assetID || asset.TeamID != state.OwnerTeamID || asset.OwnerUserID != state.OwnerUserID || asset.AssetType != "chat_memory" {
		return journal.PrincipalAgent{}, errors.New("TencentDB returned an invalid dynamic Agent Chat Memory Asset")
	}
	mapping := journal.PrincipalAgent{
		Fingerprint: intent.Fingerprint, RouteKind: intent.RouteKind, BackendUserID: state.OwnerUserID,
		BackendTeamID: state.OwnerTeamID, BackendAgentID: agent.AgentID, BackendAssetID: asset.AssetID,
		DisplayLabel: intent.DisplayLabel, State: "active",
	}
	if err := provisioner.Store.PutPrincipalAgent(ctx, mapping); err != nil {
		return journal.PrincipalAgent{}, fmt.Errorf("persist principal Agent mapping: %w", err)
	}
	return mapping, nil
}

type principalMarker struct {
	InstallationID string `json:"installation_id"`
	Fingerprint    string `json:"principal_fingerprint"`
	Role           string `json:"role"`
	RouteKind      string `json:"route_kind"`
	SchemaVersion  int    `json:"schema_version"`
}

func principalMetadataMarker(installationID string, intent PrincipalIntent) string {
	document := struct {
		MLink principalMarker `json:"mlink"`
	}{MLink: principalMarker{
		InstallationID: installationID, Fingerprint: intent.Fingerprint, Role: "dynamic-agent",
		RouteKind: intent.RouteKind, SchemaVersion: 1,
	}}
	encoded, _ := json.Marshal(document)
	return string(encoded)
}

func parsePrincipalMetadataMarker(raw string) (principalMarker, bool) {
	var document struct {
		MLink principalMarker `json:"mlink"`
	}
	if json.Unmarshal([]byte(raw), &document) != nil || document.MLink.Role != "dynamic-agent" ||
		document.MLink.SchemaVersion != 1 || !principalFingerprintPattern.MatchString(document.MLink.Fingerprint) ||
		document.MLink.RouteKind != "hermes-private" && document.MLink.RouteKind != "hermes-group" {
		return principalMarker{}, false
	}
	return document.MLink, true
}

func validateRemoteAgent(agent tencentdb.Agent, state journal.ControlPlaneState, marker string) error {
	if strings.TrimSpace(agent.AgentID) == "" || agent.TeamID != state.OwnerTeamID || agent.OwnerUserID != state.OwnerUserID || agent.MetadataJSON != marker {
		return errors.New("TencentDB returned an Agent outside the Owner control plane")
	}
	return nil
}

func validatePrincipalIntent(intent PrincipalIntent) (PrincipalIntent, error) {
	if !principalFingerprintPattern.MatchString(intent.Fingerprint) || intent.RouteKind != "hermes-private" && intent.RouteKind != "hermes-group" ||
		forbiddenExternalIDPattern.MatchString(intent.DisplayLabel) {
		return PrincipalIntent{}, errors.New("safe fingerprinted principal intent is required")
	}
	intent.DisplayLabel = sanitizeDisplayLabel(intent.DisplayLabel)
	if intent.DisplayLabel == "" {
		return PrincipalIntent{}, errors.New("principal display label is required")
	}
	return intent, nil
}

func sanitizeDisplayLabel(value string) string {
	value = strings.TrimSpace(value)
	var result []rune
	lastSpace := false
	for _, current := range []rune(value) {
		if unicode.IsLetter(current) || unicode.IsNumber(current) || current == '-' || current == '_' || current == '.' {
			result = append(result, current)
			lastSpace = false
		} else if unicode.IsSpace(current) && !lastSpace && len(result) > 0 {
			result = append(result, ' ')
			lastSpace = true
		}
		if len(result) == 64 {
			break
		}
	}
	return strings.TrimSpace(string(result))
}

func principalAgentName(intent PrincipalIntent) string {
	route := "DM"
	if intent.RouteKind == "hermes-group" {
		route = "Group"
	}
	suffix := strings.TrimPrefix(intent.Fingerprint, "prn_")
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return fmt.Sprintf("MLink %s · %s · %s", route, intent.DisplayLabel, suffix)
}
