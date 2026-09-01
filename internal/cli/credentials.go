package cli

import (
	"context"
	"encoding/json"

	"mlink/internal/app"
)

type credentialsApplication interface {
	CredentialStatuses(context.Context) ([]app.CredentialStatus, error)
	CopyCredential(context.Context, app.CredentialRole) error
}

func runCredentials(ctx context.Context, args []string, deps Dependencies) int {
	application, ok := deps.App.(credentialsApplication)
	if !ok {
		writeLine(deps.Stderr, "mlink credentials are unavailable")
		return 1
	}
	if len(args) == 1 && args[0] == "status" || len(args) == 2 && args[0] == "status" && args[1] == "--json" {
		statuses, err := application.CredentialStatuses(ctx)
		if err != nil {
			writeLine(deps.Stderr, "mlink credential status failed")
			return 1
		}
		if len(args) == 2 {
			encoder := json.NewEncoder(deps.Stdout)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(statuses); err != nil {
				return 1
			}
			return 0
		}
		for _, status := range statuses {
			line := string(status.Role) + ": missing"
			if status.Present {
				line = string(status.Role) + ": present · fingerprint " + status.Fingerprint
			}
			writeLine(deps.Stdout, line)
		}
		return 0
	}
	if len(args) == 3 && args[0] == "copy" && args[2] == "--yes" {
		role := app.CredentialRole(args[1])
		if role != app.CredentialPanelOwner && role != app.CredentialPanelAdmin {
			return 2
		}
		writeLine(deps.Stdout, "Warning: the credential will remain in the system clipboard until replaced.")
		if err := application.CopyCredential(ctx, role); err != nil {
			writeLine(deps.Stderr, "mlink credential copy failed")
			return 1
		}
		writeLine(deps.Stdout, "Panel credential copied to the clipboard.")
		return 0
	}
	return 2
}
