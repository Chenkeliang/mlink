package app

import (
	"context"
	"io"
	"io/fs"
	"reflect"
	"testing"
)

type routeTarget struct {
	reads   []string
	writes  []string
	removes []string
	runs    [][]string
}

func (target *routeTarget) Read(_ context.Context, path string) ([]byte, fs.FileMode, error) {
	target.reads = append(target.reads, path)
	return []byte(path), 0o600, nil
}

func (target *routeTarget) WriteAtomic(_ context.Context, path string, _ []byte, _ fs.FileMode) error {
	target.writes = append(target.writes, path)
	return nil
}

func (target *routeTarget) Remove(_ context.Context, path string) error {
	target.removes = append(target.removes, path)
	return nil
}

func (target *routeTarget) Run(_ context.Context, args []string, _ io.Reader) ([]byte, error) {
	target.runs = append(target.runs, append([]string(nil), args...))
	return nil, nil
}

func TestRoutingTargetSeparatesHermesHomeAndLocalCommands(t *testing.T) {
	local := &routeTarget{}
	hermes := &routeTarget{}
	target, err := NewRoutingTarget(local, hermes, "/home/test/.hermes")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, _, _ = target.Read(ctx, "/Users/test/.codex/hooks.json")
	_, _, _ = target.Read(ctx, "/home/test/.hermes/config.yaml")
	_ = target.WriteAtomic(ctx, "/home/test/.hermes/mlink.json", []byte("x"), 0o600)
	_, _ = target.Run(ctx, []string{"launchctl", "kickstart"}, nil)
	_, _ = target.Run(ctx, []string{"hermes", "status"}, nil)
	if !reflect.DeepEqual(local.reads, []string{"/Users/test/.codex/hooks.json"}) || !reflect.DeepEqual(hermes.reads, []string{"/home/test/.hermes/config.yaml"}) {
		t.Fatalf("local/hermes reads = %#v / %#v", local.reads, hermes.reads)
	}
	if !reflect.DeepEqual(hermes.writes, []string{"/home/test/.hermes/mlink.json"}) {
		t.Fatalf("Hermes writes = %#v", hermes.writes)
	}
	if len(local.runs) != 1 || len(hermes.runs) != 1 {
		t.Fatalf("local/hermes runs = %#v / %#v", local.runs, hermes.runs)
	}
}

func TestRoutingTargetRejectsInvalidHermesHome(t *testing.T) {
	if _, err := NewRoutingTarget(&routeTarget{}, &routeTarget{}, ".hermes"); err == nil {
		t.Fatal("NewRoutingTarget() error = nil")
	}
}
