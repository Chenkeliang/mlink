package hermes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"mlink/internal/install"
)

var orbMachinePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type Detection struct {
	Machine    string
	HermesHome string
	ConfigPath string
	Version    string
}

type OrbTarget struct {
	machine    string
	hermesHome string
	runner     install.CommandRunner
}

func Detect(ctx context.Context, runner install.CommandRunner, machine string) (Detection, error) {
	if !orbMachinePattern.MatchString(machine) {
		return Detection{}, errors.New("invalid Orb machine name")
	}
	if runner == nil {
		runner = install.LocalTarget{}
	}
	configOutput, err := runner.Run(ctx, []string{"orb", "-m", machine, "hermes", "config", "path"}, nil)
	if err != nil {
		return Detection{}, fmt.Errorf("detect Hermes config path: %w", err)
	}
	configPath := filepath.Clean(strings.TrimSpace(string(configOutput)))
	if !filepath.IsAbs(configPath) || filepath.Base(configPath) != "config.yaml" {
		return Detection{}, fmt.Errorf("Hermes reported unsafe config path %q", configPath)
	}
	versionOutput, err := runner.Run(ctx, []string{"orb", "-m", machine, "hermes", "--version"}, nil)
	if err != nil {
		return Detection{}, fmt.Errorf("detect Hermes version: %w", err)
	}
	return Detection{
		Machine:    machine,
		HermesHome: filepath.Dir(configPath),
		ConfigPath: configPath,
		Version:    strings.TrimSpace(string(versionOutput)),
	}, nil
}

func NewOrbTarget(machine, hermesHome string, runner install.CommandRunner) (*OrbTarget, error) {
	if !orbMachinePattern.MatchString(machine) {
		return nil, errors.New("invalid Orb machine name")
	}
	home := filepath.Clean(hermesHome)
	if !filepath.IsAbs(home) || home == string(filepath.Separator) {
		return nil, errors.New("Hermes home must be an absolute non-root path")
	}
	if runner == nil {
		runner = install.LocalTarget{}
	}
	return &OrbTarget{machine: machine, hermesHome: home, runner: runner}, nil
}

func (target *OrbTarget) Read(ctx context.Context, path string) ([]byte, fs.FileMode, error) {
	clean, err := target.validatePath(path)
	if err != nil {
		return nil, 0, err
	}
	content, err := target.run(ctx, []string{"cat", clean}, nil)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such file") {
			return nil, 0, fs.ErrNotExist
		}
		return nil, 0, err
	}
	return content, 0o600, nil
}

func (target *OrbTarget) WriteAtomic(ctx context.Context, path string, content []byte, mode fs.FileMode) error {
	clean, err := target.validatePath(path)
	if err != nil {
		return err
	}
	if mode.Perm() != 0o600 {
		return fmt.Errorf("Hermes managed file %q must use mode 0600", clean)
	}
	directory := filepath.Dir(clean)
	temporary := filepath.Join(directory, ".mlink-"+filepath.Base(clean)+".tmp")
	steps := []struct {
		args  []string
		stdin io.Reader
	}{
		{args: []string{"mkdir", "-p", directory}},
		{args: []string{"tee", temporary}, stdin: strings.NewReader(string(content))},
		{args: []string{"chmod", "0600", temporary}},
		{args: []string{"mv", temporary, clean}},
	}
	for _, step := range steps {
		if _, err := target.run(ctx, step.args, step.stdin); err != nil {
			_, _ = target.run(ctx, []string{"rm", temporary}, nil)
			return err
		}
	}
	return nil
}

func (target *OrbTarget) Remove(ctx context.Context, path string) error {
	clean, err := target.validateOwnedPath(path)
	if err != nil {
		return err
	}
	_, err = target.run(ctx, []string{"rm", clean}, nil)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such file") {
		return nil
	}
	return err
}

func (target *OrbTarget) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	if len(args) == 0 || args[0] != "hermes" {
		return nil, errors.New("Orb target only permits direct Hermes service commands")
	}
	for _, arg := range args {
		if arg == "sh" || arg == "-c" {
			return nil, errors.New("shell execution is forbidden for Orb target")
		}
	}
	return target.run(ctx, args, stdin)
}

func (target *OrbTarget) validatePath(path string) (string, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean == target.hermesHome || !strings.HasPrefix(clean, target.hermesHome+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside detected Hermes home", path)
	}
	return clean, nil
}

func (target *OrbTarget) validateOwnedPath(path string) (string, error) {
	clean, err := target.validatePath(path)
	if err != nil {
		return "", err
	}
	providerRoot := filepath.Join(target.hermesHome, "plugins", "mlink")
	if clean != filepath.Join(target.hermesHome, "mlink.json") && clean != providerRoot && !strings.HasPrefix(clean, providerRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is not owned by MLink", path)
	}
	return clean, nil
}

func (target *OrbTarget) run(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	command := append([]string{"orb", "-m", target.machine}, args...)
	output, err := target.runner.Run(ctx, command, stdin)
	if err != nil {
		return nil, fmt.Errorf("run Orb command %q: %w", args[0], err)
	}
	return output, nil
}
