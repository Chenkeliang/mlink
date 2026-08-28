package connection

import (
	"encoding/json"
	"errors"
	"strings"
)

const maxRouteComponentBytes = 128

type ProviderRef struct {
	ID      string `json:"provider_id"`
	Version string `json:"provider_version"`
}

type RouteKey struct {
	ConnectionID    string `json:"connection_id"`
	ProviderID      string `json:"provider_id"`
	ProviderVersion string `json:"provider_version"`
	ConfigRevision  string `json:"config_revision"`
}

type ConnectionSnapshot struct {
	route      RouteKey
	config     json.RawMessage
	secretRefs map[string]string
}

func NewSnapshot(
	connectionID string,
	provider ProviderRef,
	configRevision string,
	config json.RawMessage,
	secretRefs map[string]string,
) (ConnectionSnapshot, error) {
	if !validStableID(connectionID) {
		return ConnectionSnapshot{}, errors.New("invalid connection_id")
	}
	if !validProviderID(provider.ID) {
		return ConnectionSnapshot{}, errors.New("invalid provider_id")
	}
	if !validSemVer(provider.Version) {
		return ConnectionSnapshot{}, errors.New("invalid provider_version")
	}
	if !validStableID(configRevision) {
		return ConnectionSnapshot{}, errors.New("invalid config_revision")
	}

	return ConnectionSnapshot{
		route: RouteKey{
			ConnectionID:    connectionID,
			ProviderID:      provider.ID,
			ProviderVersion: provider.Version,
			ConfigRevision:  configRevision,
		},
		config:     append(json.RawMessage(nil), config...),
		secretRefs: cloneStringMap(secretRefs),
	}, nil
}

func (s ConnectionSnapshot) RouteKey() RouteKey {
	return s.route
}

func (s ConnectionSnapshot) Config() json.RawMessage {
	return append(json.RawMessage(nil), s.config...)
}

func (s ConnectionSnapshot) SecretRefs() map[string]string {
	return cloneStringMap(s.secretRefs)
}

func validStableID(value string) bool {
	if len(value) == 0 || len(value) > maxRouteComponentBytes {
		return false
	}
	for i := range len(value) {
		char := value[i]
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validProviderID(value string) bool {
	if value != strings.TrimSpace(value) || len(value) == 0 || len(value) > maxRouteComponentBytes {
		return false
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			char := label[i]
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validSemVer(value string) bool {
	if value != strings.TrimSpace(value) || value == "" {
		return false
	}
	if strings.Count(value, "+") > 1 {
		return false
	}
	mainAndBuild := strings.SplitN(value, "+", 2)
	if len(mainAndBuild) == 2 && !validIdentifiers(mainAndBuild[1], false) {
		return false
	}
	coreAndPre := strings.SplitN(mainAndBuild[0], "-", 2)
	if len(coreAndPre) == 2 && !validIdentifiers(coreAndPre[1], true) {
		return false
	}
	core := strings.Split(coreAndPre[0], ".")
	if len(core) != 3 {
		return false
	}
	for _, part := range core {
		if !validNumericIdentifier(part) {
			return false
		}
	}
	return true
}

func validIdentifiers(value string, rejectNumericLeadingZero bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		numeric := true
		for i := range len(identifier) {
			char := identifier[i]
			if char < '0' || char > '9' {
				numeric = false
			}
			if (char >= 'a' && char <= 'z') ||
				(char >= 'A' && char <= 'Z') ||
				(char >= '0' && char <= '9') || char == '-' {
				continue
			}
			return false
		}
		if rejectNumericLeadingZero && numeric && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func validNumericIdentifier(value string) bool {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
