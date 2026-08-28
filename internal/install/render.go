package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/tabwriter"
)

func RenderJSON(changeSet ChangeSet) ([]byte, error) {
	data, err := json.MarshalIndent(changeSet, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render ChangeSet JSON: %w", err)
	}
	return append(data, '\n'), nil
}

func RenderText(changeSet ChangeSet) (string, error) {
	var output bytes.Buffer
	writer := tabwriter.NewWriter(&output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(writer, "Plan\t%s\nMLink\t%s\n", changeSet.PlanID, changeSet.MLinkVersion); err != nil {
		return "", err
	}
	for _, operation := range changeSet.Operations {
		if _, err := fmt.Fprintf(writer, "%s\t%s\n", operation.Action, operation.Target); err != nil {
			return "", err
		}
		for _, diff := range operation.SemanticDiff {
			if _, err := fmt.Fprintf(writer, "  %s\t%s -> %s\n", diff.Path, diff.Before, diff.After); err != nil {
				return "", err
			}
		}
		for _, invariant := range operation.ProtectedInvariants {
			if _, err := fmt.Fprintf(writer, "  protected:%s\tpreserved=%t\n", invariant.Name, invariant.Preserved); err != nil {
				return "", err
			}
		}
	}
	for _, warning := range changeSet.Warnings {
		if _, err := fmt.Fprintf(writer, "warning\t%s\n", warning); err != nil {
			return "", err
		}
	}
	if err := writer.Flush(); err != nil {
		return "", err
	}
	return output.String(), nil
}
