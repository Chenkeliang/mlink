package doctor

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
