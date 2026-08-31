package identity

import (
	"fmt"
	"time"
)

type Candidate struct {
	DisplayName string    `json:"display_name"`
	Kind        string    `json:"kind"`
	Value       []byte    `json:"-"`
	Suffix      string    `json:"suffix"`
	LastSeen    time.Time `json:"last_seen"`
}

func NewCandidate(displayName, kind, value string, lastSeen time.Time) Candidate {
	return Candidate{DisplayName: displayName, Kind: kind, Value: []byte(value), Suffix: candidateSuffix(value), LastSeen: lastSeen}
}

func (candidate Candidate) String() string {
	return fmt.Sprintf("Candidate{DisplayName:%q Kind:%q Suffix:%q LastSeen:%s Value:<redacted>}", candidate.DisplayName, candidate.Kind, candidate.Suffix, candidate.LastSeen.UTC().Format(time.RFC3339))
}

func (candidate Candidate) GoString() string { return candidate.String() }

func (candidate *Candidate) Wipe() {
	if candidate == nil {
		return
	}
	for index := range candidate.Value {
		candidate.Value[index] = 0
	}
	candidate.Value = nil
}

func candidateSuffix(value string) string {
	if len(value) <= 4 {
		return value
	}
	return value[len(value)-4:]
}
