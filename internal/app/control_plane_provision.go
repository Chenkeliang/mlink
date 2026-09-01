package app

import (
	"context"
	"errors"
	"fmt"

	"mlink/internal/install"
)

func (service *Service) PlanControlPlaneProvision(ctx context.Context) (install.ChangeSet, error) {
	if service == nil || service.ControlProvisioner == nil {
		return install.ChangeSet{}, errors.New("control-plane provisioning service is required")
	}
	return service.ControlProvisioner.PlanProvision(ctx, service.ControlRequest)
}

func (service *Service) ApplyControlPlaneProvision(ctx context.Context, planID string) error {
	plan, err := service.PlanControlPlaneProvision(ctx)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: control-plane provision changed after preview", install.ErrPlanStale)
	}
	_, err = service.ControlProvisioner.ApplyProvision(ctx, plan.PlanID, service.ControlRequest)
	return err
}
