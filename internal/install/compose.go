package install

import (
	"errors"
	"time"
)

func ComposeChangeSets(changeSets ...ChangeSet) (ChangeSet, error) {
	if len(changeSets) == 0 {
		return ChangeSet{}, errors.New("at least one ChangeSet is required")
	}
	combined := ChangeSet{GeneratedAt: time.Now().UTC(), MLinkVersion: "dev"}
	targets := map[string]bool{}
	operationIDs := []string{}
	for _, changeSet := range changeSets {
		if changeSet.PlanID == "" {
			return ChangeSet{}, errors.New("cannot compose an unplanned ChangeSet")
		}
		for _, operation := range changeSet.Operations {
			if targets[operation.Target] {
				return ChangeSet{}, errors.New("composed ChangeSets contain a duplicate target")
			}
			targets[operation.Target] = true
			combined.Operations = append(combined.Operations, operation)
			combined.ProtectedInvariants = append(combined.ProtectedInvariants, operation.ProtectedInvariants...)
			operationIDs = append(operationIDs, operation.ID)
		}
		combined.Warnings = append(combined.Warnings, changeSet.Warnings...)
		if combined.SelectedConnection == "" {
			combined.SelectedConnection = changeSet.SelectedConnection
		} else if changeSet.SelectedConnection != "" && changeSet.SelectedConnection != combined.SelectedConnection {
			return ChangeSet{}, errors.New("composed ChangeSets select different connections")
		}
	}
	combined.PlanID = "plan_" + hashParts(operationIDs...)[:26]
	return combined, nil
}
