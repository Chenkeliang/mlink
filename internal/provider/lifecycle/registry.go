package lifecycle

import (
	"errors"
	"fmt"
	"regexp"
)

var ErrBackendUnavailable = errors.New("Provider backend lifecycle is unavailable")

var providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,127}$`)

type Registration struct {
	ProviderID string
	Lifecycle  BackendLifecycle
}

type Registry struct{ values map[string]BackendLifecycle }

func NewRegistry(registrations ...Registration) (*Registry, error) {
	registry := &Registry{values: make(map[string]BackendLifecycle, len(registrations))}
	for _, registration := range registrations {
		if !providerIDPattern.MatchString(registration.ProviderID) || registration.Lifecycle == nil {
			return nil, errors.New("valid Provider lifecycle registration is required")
		}
		if _, exists := registry.values[registration.ProviderID]; exists {
			return nil, fmt.Errorf("duplicate Provider lifecycle %q", registration.ProviderID)
		}
		registry.values[registration.ProviderID] = registration.Lifecycle
	}
	return registry, nil
}

func (registry *Registry) Get(providerID string) (BackendLifecycle, error) {
	if registry == nil {
		return nil, ErrBackendUnavailable
	}
	value, exists := registry.values[providerID]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrBackendUnavailable, providerID)
	}
	return value, nil
}
