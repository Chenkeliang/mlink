package host

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func buildEnvironment(parent []string, revisionDir string) ([]string, revisionPaths, error) {
	if revisionDir == "" {
		return nil, revisionPaths{}, fmt.Errorf("provider revision directory is empty")
	}
	absoluteRevision, err := filepath.Abs(revisionDir)
	if err != nil {
		return nil, revisionPaths{}, fmt.Errorf("resolve provider revision directory: %w", err)
	}
	paths := revisionPaths{
		Home: filepath.Join(absoluteRevision, "home"),
		Temp: filepath.Join(absoluteRevision, "tmp"),
	}
	for _, path := range []string{absoluteRevision, paths.Home, paths.Temp} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, revisionPaths{}, fmt.Errorf("create provider runtime directory: %w", err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return nil, revisionPaths{}, fmt.Errorf("protect provider runtime directory: %w", err)
		}
	}

	allowed := make(map[string]string)
	for _, item := range parent {
		name, value, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		if name == "PATH" || name == "LANG" || strings.HasPrefix(name, "LC_") {
			allowed[name] = value
		}
	}
	keys := make([]string, 0, len(allowed))
	for name := range allowed {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys)+2)
	for _, name := range keys {
		environment = append(environment, name+"="+allowed[name])
	}
	environment = append(environment, "HOME="+paths.Home, "TMPDIR="+paths.Temp)
	return environment, paths, nil
}
