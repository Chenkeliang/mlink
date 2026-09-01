package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

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
	grouped := make(map[string][]install.OwnedResource)
	var targets []string
	for _, resource := range owned {
		if strings.HasPrefix(resource.Target, "service:") {
			continue
		}
		if _, exists := grouped[resource.Target]; !exists {
			targets = append(targets, resource.Target)
		}
		grouped[resource.Target] = append(grouped[resource.Target], resource)
	}
	report := DriftReport{Resources: make([]DriftResource, 0, len(targets))}
	for _, target := range targets {
		resources := grouped[target]
		ownerID := preferredOwner(resources)
		current, _, err := service.Target.Read(ctx, target)
		state := DriftMatching
		if errors.Is(err, fs.ErrNotExist) {
			state = DriftMissing
		} else if err != nil {
			return DriftReport{}, fmt.Errorf("read owned resource %q: %w", target, err)
		} else {
			currentHash := contentHash(current)
			state = DriftModified
			for _, resource := range resources {
				if resource.PostApplyHash == currentHash {
					state = DriftMatching
					ownerID = resource.OwnerID
					if resource.OwnerID != "dev.mlink.restore" {
						break
					}
				}
			}
		}
		report.Resources = append(report.Resources, DriftResource{OwnerID: ownerID, Target: target, State: state})
	}
	return report, nil
}

func preferredOwner(resources []install.OwnedResource) string {
	for _, resource := range resources {
		if resource.OwnerID != "dev.mlink.restore" {
			return resource.OwnerID
		}
	}
	if len(resources) != 0 {
		return resources[0].OwnerID
	}
	return ""
}
