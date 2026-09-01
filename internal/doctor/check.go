package doctor

import (
	"errors"

	"mlink/internal/config"
	"mlink/internal/identity"
	"mlink/internal/journal"
	"mlink/internal/version"
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
	items := []struct{ id, checkID string }{
		{"personal-owner", "spaces.personal"}, {"hermes-private", "spaces.hermes_private"}, {"hermes-groups", "spaces.hermes_groups"},
	}
	if configuration.SchemaVersion == 3 {
		items = []struct{ id, checkID string }{
			{"owner", "routing.owner"}, {"hermes-private", "routing.hermes_private"}, {"hermes-groups", "routing.hermes_groups"},
		}
	}
	for _, item := range items {
		check := Check{ID: item.checkID, State: StatePassed, Code: "active"}
		_, exists := configuration.Spaces[item.id]
		if configuration.SchemaVersion == 3 {
			_, exists = configuration.RoutingPolicies[item.id]
		}
		if !exists {
			check.State, check.Code = StateFailed, "invalid"
		}
		checks = append(checks, check)
	}
	return checks
}

func CompatibilityChecks(info version.Info, activeSchema int, goos, goarch string) []Check {
	versionCheck := Check{ID: "runtime.binary", State: StatePassed, Code: "compatible", Message: info.Version}
	if info.Version == "" || info.SchemaMin <= 0 || info.SchemaMax < info.SchemaMin {
		versionCheck.State, versionCheck.Code = StateFailed, "metadata_invalid"
	}
	platformCheck := Check{ID: "runtime.platform", State: StatePassed, Code: "compatible", Message: info.GOOS + "/" + info.GOARCH}
	if info.GOOS != goos || info.GOARCH != goarch {
		platformCheck.State, platformCheck.Code = StateFailed, "architecture_mismatch"
	}
	schemaCheck := Check{ID: "config.schema", State: StatePassed, Code: "compatible"}
	if activeSchema < info.SchemaMin || activeSchema > info.SchemaMax {
		schemaCheck.State, schemaCheck.Code = StateFailed, "schema_unsupported"
	}
	return []Check{versionCheck, platformCheck, schemaCheck}
}

func JournalCheck(summary journal.QueueStatus) Check {
	switch {
	case summary.Ambiguous > 0:
		return Check{ID: "journal.queue", State: StateFailed, Code: "ambiguous", Message: "run: mlink maintenance journal list --json"}
	case summary.Permanent > 0:
		return Check{ID: "journal.queue", State: StateFailed, Code: "permanent_failure", Message: "run: mlink maintenance journal list --json"}
	case summary.Retrying > 0 || summary.Queued > 0:
		return Check{ID: "journal.queue", State: StatePendingAction, Code: "retrying", Message: "capture delivery is pending"}
	default:
		return Check{ID: "journal.queue", State: StatePassed, Code: "clean"}
	}
}
