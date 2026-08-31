package secret

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

type runnerCall struct {
	args  []string
	stdin string
}

type fakeRunner struct {
	calls  []runnerCall
	output []byte
	err    error
}

type exitCodeError int

func (err exitCodeError) Error() string { return "command failed" }
func (err exitCodeError) ExitCode() int { return int(err) }

func (r *fakeRunner) Run(_ context.Context, args []string, stdin io.Reader) ([]byte, error) {
	var input bytes.Buffer
	if stdin != nil {
		_, _ = input.ReadFrom(stdin)
	}
	r.calls = append(r.calls, runnerCall{args: append([]string(nil), args...), stdin: input.String()})
	return append([]byte(nil), r.output...), r.err
}

func TestKeychainPutPassesSecretOnlyOnStdin(t *testing.T) {
	runner := &fakeRunner{}
	store := Keychain{Runner: runner}
	secret := []byte("actual-secret")
	if err := store.Put(context.Background(), "connection/local/token", secret); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	wantArgs := []string{"/usr/bin/security", "add-generic-password", "-U", "-s", "dev.mlink", "-a", "connection/local/token", "-w"}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", call.args, wantArgs)
	}
	if call.stdin != "actual-secret\n" {
		t.Fatalf("stdin = %q", call.stdin)
	}
	if strings.Contains(strings.Join(call.args, " "), string(secret)) {
		t.Fatal("secret leaked into command arguments")
	}
}

func TestKeychainGetAndDeleteUseAccountOnly(t *testing.T) {
	runner := &fakeRunner{output: []byte("stored-token\n")}
	store := Keychain{Runner: runner}
	got, err := store.Get(context.Background(), "connection/local/token")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "stored-token" {
		t.Fatalf("Get() = %q", got)
	}
	if err := store.Delete(context.Background(), "connection/local/token"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"/usr/bin/security", "find-generic-password", "-s", "dev.mlink", "-a", "connection/local/token", "-w"},
		{"/usr/bin/security", "delete-generic-password", "-s", "dev.mlink", "-a", "connection/local/token"},
	}
	if len(runner.calls) != len(want) {
		t.Fatalf("calls = %d, want %d", len(runner.calls), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(runner.calls[i].args, want[i]) {
			t.Fatalf("call %d args = %#v, want %#v", i, runner.calls[i].args, want[i])
		}
	}
}

func TestKeychainRejectsInvalidInputBeforeRunner(t *testing.T) {
	runner := &fakeRunner{}
	store := Keychain{Runner: runner}
	if err := store.Put(context.Background(), "", []byte("token")); err == nil {
		t.Fatal("Put() error = nil for empty account")
	}
	if err := store.Put(context.Background(), "account", []byte("line-one\nline-two")); err == nil {
		t.Fatal("Put() error = nil for multiline secret")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %d, want 0", len(runner.calls))
	}
}

func TestKeychainGetMapsSecurityItemNotFound(t *testing.T) {
	store := Keychain{Runner: &fakeRunner{err: exitCodeError(44)}}
	if _, err := store.Get(context.Background(), "connection/local/token"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestKeychainDeleteMapsSecurityItemNotFound(t *testing.T) {
	store := Keychain{Runner: &fakeRunner{err: exitCodeError(44)}}
	if err := store.Delete(context.Background(), "identity/binding/owner-feishu-union-1"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestKeychainBindingValueAppearsOnlyOnStdin(t *testing.T) {
	runner := &fakeRunner{}
	store := Keychain{Runner: runner}
	value := []byte("on_actual_binding")
	if err := store.Put(context.Background(), "identity/binding/owner-feishu-union-1", value); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || runner.calls[0].stdin != "on_actual_binding\n" {
		t.Fatalf("calls = %#v", runner.calls)
	}
	if strings.Contains(strings.Join(runner.calls[0].args, " "), string(value)) {
		t.Fatal("binding value leaked into argv")
	}
}
