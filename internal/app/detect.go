package app

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
)

type Detection struct {
	Agents map[Agent]bool
}

func (service *Service) Detect(ctx context.Context) (Detection, error) {
	if service == nil || service.Target == nil {
		return Detection{}, errors.New("installation target is required")
	}
	home := filepath.Dir(service.Paths.Home)
	paths := map[Agent]string{
		Codex:  filepath.Join(home, ".codex", "hooks.json"),
		Pi:     filepath.Join(home, ".pi", "agent", "extensions"),
		Hermes: filepath.Join(home, ".hermes", "config.yaml"),
	}
	result := Detection{Agents: make(map[Agent]bool, len(paths))}
	for agent, path := range paths {
		_, _, err := service.Target.Read(ctx, path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Detection{}, err
		}
		result.Agents[agent] = true
	}
	return result, nil
}
