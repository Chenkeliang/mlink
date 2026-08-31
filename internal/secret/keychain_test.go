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

type secretWriterCall struct {
	args   []string
	secret []byte
}

type fakeSecretWriter struct {
	calls []secretWriterCall
	err   error
}

func (writer *fakeSecretWriter) Write(_ context.Context, args []string, secret []byte) error {
	writer.calls = append(writer.calls, secretWriterCall{
		args: append([]string(nil), args...), secret: append([]byte(nil), secret...),
	})
	return writer.err
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

func TestKeychainPutPassesSecretOnlyToProtectedWriter(t *testing.T) {
	writer := &fakeSecretWriter{}
	store := Keychain{Writer: writer}
	secret := []byte("actual-secret")
	if err := store.Put(context.Background(), "connection/local/token", secret); err != nil {
		t.Fatal(err)
	}
	if len(writer.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(writer.calls))
	}
	call := writer.calls[0]
	wantArgs := []string{"/usr/bin/security", "add-generic-password", "-U", "-s", "dev.mlink", "-a", "connection/local/token", "-w"}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", call.args, wantArgs)
	}
	if string(call.secret) == "actual-secret" || !strings.HasPrefix(string(call.secret), "mlink:v1:") {
		t.Fatalf("encoded secret = %q", call.secret)
	}
	if strings.Contains(strings.Join(call.args, " "), string(secret)) {
		t.Fatal("secret leaked into command arguments")
	}
}

func TestKeychainRoundTripsBinarySecretThroughVersionedEncoding(t *testing.T) {
	writer := &fakeSecretWriter{}
	store := Keychain{Writer: writer}
	want := []byte{0x00, 0x01, '\n', '\r', 0x7f, 0x80, 0xff}
	if err := store.Put(context.Background(), "identity/hmac-key", want); err != nil {
		t.Fatal(err)
	}
	encoded := append(append([]byte(nil), writer.calls[0].secret...), '\n')
	store.Runner = &fakeRunner{output: encoded}
	got, err := store.Get(context.Background(), "identity/hmac-key")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("round trip = %x, %v", got, err)
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

func TestKeychainRejectsMalformedVersionedSecret(t *testing.T) {
	store := Keychain{Runner: &fakeRunner{output: []byte("mlink:v1:not+base64\n")}}
	if _, err := store.Get(context.Background(), "identity/hmac-key"); err == nil {
		t.Fatal("Get() error = nil for malformed encoded secret")
	}
}

func TestKeychainRejectsInvalidInputBeforeRunner(t *testing.T) {
	writer := &fakeSecretWriter{}
	store := Keychain{Writer: writer}
	if err := store.Put(context.Background(), "", []byte("token")); err == nil {
		t.Fatal("Put() error = nil for empty account")
	}
	if len(writer.calls) != 0 {
		t.Fatalf("writer calls = %d, want 0", len(writer.calls))
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

func TestKeychainBindingValueNeverAppearsInArgv(t *testing.T) {
	writer := &fakeSecretWriter{}
	store := Keychain{Writer: writer}
	value := []byte("on_actual_binding")
	if err := store.Put(context.Background(), "identity/binding/owner-feishu-union-1", value); err != nil {
		t.Fatal(err)
	}
	if len(writer.calls) != 1 || string(writer.calls[0].secret) == "on_actual_binding" || !strings.HasPrefix(string(writer.calls[0].secret), "mlink:v1:") {
		t.Fatalf("calls = %#v", writer.calls)
	}
	if strings.Contains(strings.Join(writer.calls[0].args, " "), string(value)) {
		t.Fatal("binding value leaked into argv")
	}
}
