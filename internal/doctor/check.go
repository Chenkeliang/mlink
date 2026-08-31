package doctor

import (
	"errors"

	"mlink/internal/config"
	"mlink/internal/identity"
)

type State string

const (
	StatePassed        State = "passed"
	StatePendingAction State = "pending_action"
	StateFailed        State = "failed"
)

type Check struct {
	ID      string `json:"id"`
	State   State  `json:"state"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type Report struct {
	Checks []Check `json:"checks"`
}

func (report Report) ExitCode() int {
	pending := false
	for _, check := range report.Checks {
		switch check.State {
		case StateFailed:
			return 1
		case StatePendingAction:
			pending = true
		}
	}
	if pending {
		return 4
	}
	return 0
}

func IdentityChecks(configuration config.Config, identityKey []byte, bindingErr error) []Check {
	checks := make([]Check, 0, 5)
	keyCheck := Check{ID: "identity.key", State: StatePassed, Code: "active"}
	if len(identityKey) == 0 {
		keyCheck.State, keyCheck.Code = StateFailed, "missing"
	} else if len(identityKey) != 32 {
		keyCheck.State, keyCheck.Code = StateFailed, "invalid"
	}
	checks = append(checks, keyCheck)
	ownerCheck := Check{ID: "identity.owner", State: StatePassed, Code: "active"}
	owner, ownerExists := configuration.Principals["owner"]
	activeBinding := false
	for _, binding := range configuration.Bindings {
		activeBinding = activeBinding || binding.PrincipalID == "owner" && binding.Status == config.BindingActive
	}
	switch {
	case !ownerExists || owner.CanonicalUserID == "":
		ownerCheck.State, ownerCheck.Code = StateFailed, "principal_missing"
	case errors.Is(bindingErr, identity.ErrBindingConflict):
		ownerCheck.State, ownerCheck.Code = StateFailed, "binding_conflict"
	case bindingErr != nil || !activeBinding:
		ownerCheck.State, ownerCheck.Code = StateFailed, "binding_missing"
	}
	checks = append(checks, ownerCheck)
	for _, item := range []struct{ id, checkID string }{
		{"personal-owner", "spaces.personal"}, {"hermes-private", "spaces.hermes_private"}, {"hermes-groups", "spaces.hermes_groups"},
	} {
		check := Check{ID: item.checkID, State: StatePassed, Code: "active"}
		if _, exists := configuration.Spaces[item.id]; !exists {
			check.State, check.Code = StateFailed, "invalid"
		}
		checks = append(checks, check)
	}
	return checks
}
