package cli

import (
	"encoding/json"
	"fmt"

	"mlink/internal/version"
)

func runVersion(args []string, deps Dependencies) int {
	jsonOutput := len(args) == 1 && args[0] == "--json"
	if len(args) != 0 && !jsonOutput {
		return 2
	}
	info := version.Current()
	if jsonOutput {
		encoder := json.NewEncoder(deps.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(info); err != nil {
			return 1
		}
		return 0
	}
	_, err := fmt.Fprintf(deps.Stdout, "MLink %s (%s) %s/%s schema %d-%d\n", info.Version, info.Commit, info.GOOS, info.GOARCH, info.SchemaMin, info.SchemaMax)
	if err != nil {
		return 1
	}
	return 0
}
