package layout

import (
	"errors"
	"path/filepath"
)

// Paths contains the stable per-user locations owned by MLink.
type Paths struct {
	Home             string
	Binary           string
	SourceExecutable string
	Config           string
	Journal          string
	Run              string
	Socket           string
	Backups          string
}

// FromHome derives MLink's paths without reading process-global state.
func FromHome(home, executable string) (Paths, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(executable) {
		return Paths{}, errors.New("home and executable paths must be absolute")
	}
	mlinkHome := filepath.Join(home, ".mlink")
	run := filepath.Join(mlinkHome, "run")
	return Paths{
		Home:             mlinkHome,
		Binary:           filepath.Join(home, ".local", "bin", "mlink"),
		SourceExecutable: filepath.Clean(executable),
		Config:           filepath.Join(mlinkHome, "config.yaml"),
		Journal:          filepath.Join(mlinkHome, "journal.db"),
		Run:              run,
		Socket:           filepath.Join(run, "mlink.sock"),
		Backups:          filepath.Join(mlinkHome, "backups"),
	}, nil
}
