package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"mlink/internal/install"
)

type DriftState string

const (
	DriftMatching DriftState = "matching"
	DriftModified DriftState = "modified"
	DriftMissing  DriftState = "missing"
)

type DriftResource struct {
	OwnerID string     `json:"owner_id"`
	Target  string     `json:"target"`
	State   DriftState `json:"state"`
}

type DriftReport struct {
	Resources []DriftResource `json:"resources"`
}

type ownedResourceReader interface {
	ListOwned(context.Context) ([]install.OwnedResource, error)
}

func (service *Service) ConfigDiff(ctx context.Context) (DriftReport, error) {
	if service == nil || service.Target == nil {
		return DriftReport{}, errors.New("installation target is required")
	}
	reader, ok := service.Ledger.(ownedResourceReader)
	if !ok {
		return DriftReport{}, errors.New("installation ledger cannot list owned resources")
	}
	owned, err := reader.ListOwned(ctx)
	if err != nil {
		return DriftReport{}, err
	}
	report := DriftReport{Resources: make([]DriftResource, 0, len(owned))}
	for _, resource := range owned {
		current, _, err := service.Target.Read(ctx, resource.Target)
		state := DriftMatching
		if errors.Is(err, fs.ErrNotExist) {
			state = DriftMissing
		} else if err != nil {
			return DriftReport{}, fmt.Errorf("read owned resource %q: %w", resource.Target, err)
		} else if contentHash(current) != resource.PostApplyHash {
			state = DriftModified
		}
		report.Resources = append(report.Resources, DriftResource{OwnerID: resource.OwnerID, Target: resource.Target, State: state})
	}
	return report, nil
}
