package version

import (
	"runtime"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

const (
	SchemaMin = 2
	SchemaMax = 3
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	SchemaMin int    `json:"schema_min"`
	SchemaMax int    `json:"schema_max"`
}

func Current() Info {
	return Info{
		Version: Version, Commit: Commit, BuildDate: BuildDate, GoVersion: runtime.Version(),
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, SchemaMin: SchemaMin, SchemaMax: SchemaMax,
	}
}
