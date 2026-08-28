package hermes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"reflect"
	"testing"
)

type recordedOrbCall struct {
	args  []string
	stdin string
}

type fakeOrbRunner struct {
	calls   []recordedOrbCall
	results map[string][]byte
	errors  map[string]error
}

type orbExitError int

func (err orbExitError) Error() string { return "Orb command failed" }
func (err orbExitError) ExitCode() int { return int(err) }

func (runner *fakeOrbRunner) Run(_ context.Context, args []string, stdin io.Reader) ([]byte, error) {
	var input []byte
	if stdin != nil {
		input, _ = io.ReadAll(stdin)
	}
	call := recordedOrbCall{args: append([]string(nil), args...), stdin: string(input)}
	runner.calls = append(runner.calls, call)
	key := joinArgs(args)
	return append([]byte(nil), runner.results[key]...), runner.errors[key]
}

func TestDetectUsesHermesOfficialConfigPath(t *testing.T) {
	runner := &fakeOrbRunner{results: map[string][]byte{
		"orb\x00-m\x00hermes-agent-env\x00hermes\x00config\x00path": []byte("/home/test/.hermes/config.yaml\n"),
		"orb\x00-m\x00hermes-agent-env\x00hermes\x00--version":      []byte("Hermes Agent 0.13.0\n"),
	}}
	detection, err := Detect(context.Background(), runner, "hermes-agent-env")
	if err != nil {
		t.Fatal(err)
	}
	if detection.HermesHome != "/home/test/.hermes" || detection.ConfigPath != "/home/test/.hermes/config.yaml" || detection.Version != "Hermes Agent 0.13.0" {
		t.Fatalf("detection = %#v", detection)
	}
	want := [][]string{
		{"orb", "-m", "hermes-agent-env", "hermes", "config", "path"},
		{"orb", "-m", "hermes-agent-env", "hermes", "--version"},
	}
	if got := callArgs(runner.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v", got)
	}
}

func TestDetectBridgeSelectsHostAddressOnGuestSubnet(t *testing.T) {
	runner := &fakeOrbRunner{results: map[string][]byte{
		"orb\x00-m\x00hermes-agent-env\x00ip\x00-4\x00route\x00show\x00dev\x00eth0\x00scope\x00link": []byte("192.168.139.0/24 proto kernel scope link src 192.168.139.219\n"),
	}}
	bridge, err := DetectBridge(context.Background(), runner, "hermes-agent-env", []net.Addr{
		&net.IPNet{IP: net.ParseIP("192.168.129.233"), Mask: net.CIDRMask(22, 32)},
		&net.IPNet{IP: net.ParseIP("192.168.139.3"), Mask: net.CIDRMask(23, 32)},
	}, 8097)
	if err != nil {
		t.Fatal(err)
	}
	if bridge.ListenAddress != "192.168.139.3:8097" || bridge.Endpoint != "http://192.168.139.3:8097" {
		t.Fatalf("bridge = %#v", bridge)
	}
}

func TestDetectBridgeFailsClosedWithoutMatchingHostAddress(t *testing.T) {
	runner := &fakeOrbRunner{results: map[string][]byte{
		"orb\x00-m\x00hermes-agent-env\x00ip\x00-4\x00route\x00show\x00dev\x00eth0\x00scope\x00link": []byte("192.168.139.0/24 proto kernel scope link src 192.168.139.219\n"),
	}}
	if _, err := DetectBridge(context.Background(), runner, "hermes-agent-env", []net.Addr{
		&net.IPNet{IP: net.ParseIP("192.168.129.233"), Mask: net.CIDRMask(22, 32)},
	}, 8097); err == nil {
		t.Fatal("DetectBridge() error = nil")
	}
}

func TestOrbTargetUsesDirectArgvAtomicWriteAndPrivateMode(t *testing.T) {
	runner := &fakeOrbRunner{}
	target, err := NewOrbTarget("hermes-agent-env", "/home/test/.hermes", runner)
	if err != nil {
		t.Fatal(err)
	}
	path := "/home/test/.hermes/mlink.json"
	if err := target.WriteAtomic(context.Background(), path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []recordedOrbCall{
		{args: []string{"orb", "-m", "hermes-agent-env", "mkdir", "-p", "/home/test/.hermes"}},
		{args: []string{"orb", "-m", "hermes-agent-env", "tee", "/home/test/.hermes/.mlink-mlink.json.tmp"}, stdin: "secret"},
		{args: []string{"orb", "-m", "hermes-agent-env", "chmod", "0600", "/home/test/.hermes/.mlink-mlink.json.tmp"}},
		{args: []string{"orb", "-m", "hermes-agent-env", "mv", "/home/test/.hermes/.mlink-mlink.json.tmp", path}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
	for _, call := range runner.calls {
		for _, arg := range call.args {
			if arg == "sh" || arg == "-c" {
				t.Fatalf("shell invocation found: %#v", call.args)
			}
		}
	}
}

func TestOrbTargetReadAndRemoveUseDirectArgv(t *testing.T) {
	path := "/home/test/.hermes/plugins/mlink/__init__.py"
	runner := &fakeOrbRunner{results: map[string][]byte{
		joinArgs([]string{"orb", "-m", "hermes-agent-env", "cat", path}): []byte("provider"),
	}}
	target, err := NewOrbTarget("hermes-agent-env", "/home/test/.hermes", runner)
	if err != nil {
		t.Fatal(err)
	}
	content, mode, err := target.Read(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "provider" || mode.Perm() != 0o600 {
		t.Fatalf("read = %q mode=%o", content, mode)
	}
	if err := target.Remove(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"orb", "-m", "hermes-agent-env", "test", "-e", path},
		{"orb", "-m", "hermes-agent-env", "cat", path},
		{"orb", "-m", "hermes-agent-env", "rm", path},
	}
	if got := callArgs(runner.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v", got)
	}
}

func TestOrbTargetMapsFailedExistenceProbeToNotExist(t *testing.T) {
	path := "/home/test/.hermes/plugins/mlink/plugin.yaml"
	key := joinArgs([]string{"orb", "-m", "hermes-agent-env", "test", "-e", path})
	runner := &fakeOrbRunner{errors: map[string]error{key: orbExitError(1)}}
	target, err := NewOrbTarget("hermes-agent-env", "/home/test/.hermes", runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := target.Read(context.Background(), path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read() error = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestOrbTargetRejectsPathsOutsideHermesHome(t *testing.T) {
	runner := &fakeOrbRunner{}
	target, err := NewOrbTarget("hermes-agent-env", "/home/test/.hermes", runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.WriteAtomic(context.Background(), "/home/test/.ssh/config", []byte("x"), 0o600); err == nil {
		t.Fatal("WriteAtomic() error = nil")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unsafe path executed calls: %#v", runner.calls)
	}
}

func TestOrbTargetMapsMissingCatToNotExist(t *testing.T) {
	path := "/home/test/.hermes/mlink.json"
	key := joinArgs([]string{"orb", "-m", "hermes-agent-env", "cat", path})
	runner := &fakeOrbRunner{errors: map[string]error{key: errors.New("cat: No such file or directory")}}
	target, err := NewOrbTarget("hermes-agent-env", "/home/test/.hermes", runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := target.Read(context.Background(), path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read() error = %v", err)
	}
}

func (runner *fakeOrbRunner) reset() {
	runner.calls = nil
}

func callArgs(calls []recordedOrbCall) [][]string {
	result := make([][]string, 0, len(calls))
	for _, call := range calls {
		result = append(result, call.args)
	}
	return result
}

func joinArgs(args []string) string {
	return string(bytes.Join(func() [][]byte {
		parts := make([][]byte, len(args))
		for index := range args {
			parts[index] = []byte(args[index])
		}
		return parts
	}(), []byte{0}))
}
