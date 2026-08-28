package manifest

import (
	"errors"
	"fmt"
)

const (
	defaultMaxInFlight = 4
	maximumMaxInFlight = 64
)

type CapabilityDescriptor struct {
	Version         int      `json:"version" yaml:"version"`
	Scopes          []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
	Roles           []string `json:"roles,omitempty" yaml:"roles,omitempty"`
	MaxRequestBytes int64    `json:"max_request_bytes,omitempty" yaml:"max_request_bytes,omitempty"`
	MaxResultItems  int      `json:"max_result_items,omitempty" yaml:"max_result_items,omitempty"`
	MaxInFlight     int      `json:"max_in_flight,omitempty" yaml:"max_in_flight,omitempty"`
	ReplaySafe      bool     `json:"replay_safe,omitempty" yaml:"replay_safe,omitempty"`
	Ordering        string   `json:"ordering,omitempty" yaml:"ordering,omitempty"`
}

func ValidateRuntime(
	declared map[string]CapabilityDescriptor,
	runtime map[string]CapabilityDescriptor,
) (map[string]CapabilityDescriptor, error) {
	normalizedDeclared, err := normalizeCapabilities(declared)
	if err != nil {
		return nil, fmt.Errorf("invalid declared capabilities: %w", err)
	}
	normalizedRuntime, err := normalizeCapabilities(runtime)
	if err != nil {
		return nil, fmt.Errorf("invalid runtime capabilities: %w", err)
	}
	for name, actual := range normalizedRuntime {
		limit, exists := normalizedDeclared[name]
		if !exists {
			return nil, fmt.Errorf("runtime capability %q was not declared", name)
		}
		if actual.Version > limit.Version ||
			actual.MaxRequestBytes > limit.MaxRequestBytes ||
			actual.MaxResultItems > limit.MaxResultItems ||
			actual.MaxInFlight > limit.MaxInFlight {
			return nil, fmt.Errorf("runtime capability %q exceeds declared numeric limits", name)
		}
		if !stringSubset(actual.Scopes, limit.Scopes) || !stringSubset(actual.Roles, limit.Roles) {
			return nil, fmt.Errorf("runtime capability %q exceeds declared values", name)
		}
		if actual.Ordering != "" && actual.Ordering != limit.Ordering {
			return nil, fmt.Errorf("runtime capability %q changes ordering", name)
		}
		if actual.ReplaySafe && !limit.ReplaySafe {
			return nil, fmt.Errorf("runtime capability %q elevates replay safety", name)
		}
	}
	return cloneCapabilities(normalizedRuntime), nil
}

func normalizeCapabilities(source map[string]CapabilityDescriptor) (map[string]CapabilityDescriptor, error) {
	if source == nil {
		return map[string]CapabilityDescriptor{}, nil
	}
	normalized := make(map[string]CapabilityDescriptor, len(source))
	for name, descriptor := range source {
		if descriptor.MaxInFlight == 0 {
			descriptor.MaxInFlight = defaultMaxInFlight
		}
		if descriptor.Version <= 0 || descriptor.MaxInFlight < 1 || descriptor.MaxInFlight > maximumMaxInFlight {
			return nil, fmt.Errorf("capability %q has invalid version or max_in_flight", name)
		}
		if descriptor.MaxRequestBytes < 0 || descriptor.MaxResultItems < 0 {
			return nil, fmt.Errorf("capability %q has negative limits", name)
		}
		if err := validateUniqueValues(descriptor.Scopes); err != nil {
			return nil, fmt.Errorf("capability %q scopes: %w", name, err)
		}
		if err := validateUniqueValues(descriptor.Roles); err != nil {
			return nil, fmt.Errorf("capability %q roles: %w", name, err)
		}
		switch name {
		case "capture_turn":
			if len(descriptor.Roles) == 0 || descriptor.MaxRequestBytes <= 0 || descriptor.Ordering == "" {
				return nil, errors.New("capture_turn requires roles, max_request_bytes, and ordering")
			}
		case "recall":
			if len(descriptor.Scopes) == 0 || descriptor.MaxRequestBytes <= 0 || descriptor.MaxResultItems <= 0 {
				return nil, errors.New("recall requires scopes, max_request_bytes, and max_result_items")
			}
		case "health":
		default:
			return nil, fmt.Errorf("unknown capability %q", name)
		}
		descriptor.Scopes = append([]string(nil), descriptor.Scopes...)
		descriptor.Roles = append([]string(nil), descriptor.Roles...)
		normalized[name] = descriptor
	}
	return normalized, nil
}

func validateUniqueValues(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return errors.New("empty value")
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate value %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func stringSubset(values, allowed []string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		set[value] = struct{}{}
	}
	for _, value := range values {
		if _, exists := set[value]; !exists {
			return false
		}
	}
	return true
}

func cloneCapabilities(source map[string]CapabilityDescriptor) map[string]CapabilityDescriptor {
	cloned := make(map[string]CapabilityDescriptor, len(source))
	for name, descriptor := range source {
		descriptor.Scopes = append([]string(nil), descriptor.Scopes...)
		descriptor.Roles = append([]string(nil), descriptor.Roles...)
		cloned[name] = descriptor
	}
	return cloned
}
