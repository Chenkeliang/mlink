package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"gopkg.in/yaml.v3"

	"mlink/internal/config"
)

type Status struct {
	Installed    bool           `json:"installed"`
	ActivePlanID string         `json:"active_plan_id,omitempty"`
	ConnectionID string         `json:"connection_id,omitempty"`
	Adapters     map[Agent]bool `json:"adapters"`
}

func (service *Service) Status(ctx context.Context) (Status, error) {
	if service == nil || service.Target == nil {
		return Status{}, errors.New("installation target is required")
	}
	status := Status{Adapters: make(map[Agent]bool)}
	content, _, err := service.Target.Read(ctx, service.Paths.Config)
	if errors.Is(err, fs.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("read MLink status config: %w", err)
	}
	var configuration config.Config
	if err := yaml.Unmarshal(content, &configuration); err != nil {
		return Status{}, fmt.Errorf("parse MLink status config: %w", err)
	}
	status.Installed = true
	status.ConnectionID = configuration.ActiveConnectionID
	for _, agent := range []Agent{Codex, Pi, Hermes, Cursor} {
		adapter, exists := configuration.Adapters[string(agent)]
		status.Adapters[agent] = exists && adapter.Enabled
	}
	if active, ok := service.Ledger.(ActiveInstallStore); ok {
		planID, err := active.ActiveInstallPlan(ctx)
		if err == nil {
			status.ActivePlanID = planID
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Status{}, err
		}
	}
	return status, nil
}
