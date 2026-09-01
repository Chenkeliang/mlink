package app

import (
	"context"
	"errors"
	"fmt"

	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/panel"
)

type PanelControlStatus struct {
	ControlPlane journal.ControlPlaneState `json:"control_plane"`
	Panel        panel.Status              `json:"panel"`
}

func (service *Service) PlanPanelRuntime(ctx context.Context) (install.ChangeSet, error) {
	if service == nil || service.PanelRuntime == nil || service.ControlPlaneStates == nil {
		return install.ChangeSet{}, errors.New("Panel runtime and active control-plane state are required")
	}
	state, err := service.ControlPlaneStates.LoadControlPlane(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	if (state.State != "provisioned" && state.State != "active") || state.InstanceID != service.PanelDesired.InstanceID || state.OwnerUserID == "" || state.OwnerTeamID == "" || state.OwnerAgentID == "" || state.OwnerAssetID == "" {
		return install.ChangeSet{}, errors.New("Panel runtime requires the existing Core-generated identity")
	}
	return service.PanelRuntime.Plan(ctx, service.PanelDesired)
}

func (service *Service) ApplyPanelRuntime(ctx context.Context, planID string) error {
	plan, err := service.PlanPanelRuntime(ctx)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: Panel runtime plan changed", install.ErrPlanStale)
	}
	if service.Secrets == nil || service.PanelConnectionID == "" {
		return errors.New("Panel Gateway credential source is unavailable")
	}
	token, err := service.Secrets.Get(ctx, "connection/"+service.PanelConnectionID+"/token")
	if err != nil {
		return errors.New("load Panel Gateway credential")
	}
	defer wipe(token)
	return service.PanelRuntime.Apply(ctx, plan.PlanID, service.PanelDesired, token)
}

func (service *Service) PlanPanelProvision(ctx context.Context) (install.ChangeSet, error) {
	if service == nil || service.ControlProvisioner == nil || service.PanelRuntime == nil {
		return install.ChangeSet{}, errors.New("control-plane and Panel provisioning services are required")
	}
	controlPlan, err := service.ControlProvisioner.PlanProvision(ctx, service.ControlRequest)
	if err != nil {
		return install.ChangeSet{}, err
	}
	panelPlan, err := service.PanelRuntime.Plan(ctx, service.PanelDesired)
	if err != nil {
		return install.ChangeSet{}, err
	}
	return install.ComposeChangeSets(controlPlan, panelPlan)
}

func (service *Service) ApplyPanelProvision(ctx context.Context, planID string) error {
	plan, err := service.PlanPanelProvision(ctx)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: supplied Panel provision plan no longer matches", install.ErrPlanStale)
	}
	controlPlan, err := service.ControlProvisioner.PlanProvision(ctx, service.ControlRequest)
	if err != nil {
		return err
	}
	if _, err := service.ControlProvisioner.ApplyProvision(ctx, controlPlan.PlanID, service.ControlRequest); err != nil {
		return err
	}
	if service.Secrets == nil || service.PanelConnectionID == "" {
		return errors.New("Panel Gateway credential source is unavailable")
	}
	token, err := service.Secrets.Get(ctx, "connection/"+service.PanelConnectionID+"/token")
	if err != nil {
		return errors.New("load Panel Gateway credential")
	}
	defer wipe(token)
	panelPlan, err := service.PanelRuntime.Plan(ctx, service.PanelDesired)
	if err != nil {
		return err
	}
	return service.PanelRuntime.Apply(ctx, panelPlan.PlanID, service.PanelDesired, token)
}

func (service *Service) PanelControlStatus(ctx context.Context) (PanelControlStatus, error) {
	if service == nil || service.ControlPlaneStates == nil || service.PanelRuntime == nil {
		return PanelControlStatus{}, errors.New("Panel status dependencies are unavailable")
	}
	state, err := service.ControlPlaneStates.LoadControlPlane(ctx)
	if err != nil {
		return PanelControlStatus{}, err
	}
	status, err := service.PanelRuntime.Status(ctx, service.PanelDesired)
	if err != nil {
		return PanelControlStatus{}, err
	}
	return PanelControlStatus{ControlPlane: state, Panel: status}, nil
}
