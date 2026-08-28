package install

import (
	"io/fs"
	"time"
)

type Action string

const (
	ActionCreate        Action = "create"
	ActionSemanticMerge Action = "semantic_merge"
	ActionRemoveOwned   Action = "remove_owned"
	ActionService       Action = "service_action"
	ActionUnchanged     Action = "unchanged"
)

type ChangeSet struct {
	PlanID              string      `json:"plan_id"`
	GeneratedAt         time.Time   `json:"generated_at"`
	MLinkVersion        string      `json:"mlink_version"`
	DetectedAgents      []string    `json:"detected_agents,omitempty"`
	SelectedConnection  string      `json:"selected_connection,omitempty"`
	Operations          []Operation `json:"operations"`
	ProtectedInvariants []Invariant `json:"protected_invariants,omitempty"`
	Warnings            []string    `json:"warnings,omitempty"`
}

type Operation struct {
	ID                  string             `json:"operation_id"`
	OwnerID             string             `json:"owner_id"`
	Target              string             `json:"target"`
	Action              Action             `json:"action"`
	BeforeHash          string             `json:"before_hash"`
	ProposedHash        string             `json:"proposed_hash"`
	SemanticDiff        []SemanticDiff     `json:"semantic_diff,omitempty"`
	ProtectedInvariants []Invariant        `json:"protected_invariants,omitempty"`
	RollbackAction      string             `json:"rollback_action"`
	Content             []byte             `json:"-"`
	Mode                fs.FileMode        `json:"-"`
	Command             []string           `json:"-"`
	CommandInput        []byte             `json:"-"`
	Verify              func([]byte) error `json:"-"`
	beforeExists        bool
	beforeMode          fs.FileMode
}

type DesiredResource struct {
	OwnerID             string
	Target              string
	Action              Action
	Content             []byte
	Mode                fs.FileMode
	Command             []string
	CommandInput        []byte
	SemanticDiff        []SemanticDiff
	ProtectedInvariants []Invariant
	Verify              func([]byte) error
}

type SemanticDiff struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type Invariant struct {
	Name         string `json:"name"`
	BeforeHash   string `json:"before_hash,omitempty"`
	ProposedHash string `json:"proposed_hash,omitempty"`
	Preserved    bool   `json:"preserved"`
}
