package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type countingWriter struct{ bytes int64 }

func (writer *countingWriter) Write(value []byte) (int, error) {
	writer.bytes += int64(len(value))
	return len(value), nil
}

func TestLocalStreamRunnerStreamsStdout(t *testing.T) {
	destination := &countingWriter{}
	err := (LocalStreamRunner{}).RunStream(context.Background(),
		[]string{"sh", "-c", "dd if=/dev/zero bs=1024 count=2048 2>/dev/null"}, nil, destination)
	if err != nil || destination.bytes != 2<<20 {
		t.Fatalf("bytes/error = %d/%v", destination.bytes, err)
	}
}

func TestLocalStreamRunnerHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := (LocalStreamRunner{}).RunStream(ctx, []string{"sh", "-c", "sleep 10"}, nil, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("error/elapsed = %v/%s", err, time.Since(started))
	}
}

func TestLimitedWriterStopsOversizedStream(t *testing.T) {
	writer := &LimitedWriter{Writer: io.Discard, Limit: 10}
	count, err := writer.Write([]byte("01234567890"))
	if count != 0 || !errors.Is(err, ErrStreamLimit) || writer.Written != 0 {
		t.Fatalf("count/error/written = %d/%v/%d", count, err, writer.Written)
	}
	if count, err := writer.Write([]byte("0123456789")); err != nil || count != 10 || writer.Written != 10 {
		t.Fatalf("bounded write = %d/%v/%d", count, err, writer.Written)
	}
}

func TestLocalEnvironmentRunnerPassesSecretOutsideArgv(t *testing.T) {
	secret := []byte("protected-value")
	argv := []string{"sh", "-c", `test -n "$MLINK_TEST_SECRET" && printf ok`}
	output, err := (LocalEnvironmentRunner{}).RunEnvironment(context.Background(), argv, map[string][]byte{"MLINK_TEST_SECRET": secret}, nil)
	if err != nil || string(output) != "ok" {
		t.Fatalf("output/error = %q/%v", output, err)
	}
	if strings.Contains(fmt.Sprint(argv), string(secret)) {
		t.Fatal("test command accidentally placed secret in argv")
	}
}

func TestLocalStreamRunnerRejectsMissingCommandOrOutput(t *testing.T) {
	for _, testCase := range []struct {
		argv   []string
		output io.Writer
	}{
		{output: io.Discard},
		{argv: []string{"true"}},
	} {
		if err := (LocalStreamRunner{}).RunStream(context.Background(), testCase.argv, nil, testCase.output); err == nil {
			t.Fatalf("RunStream(%#v) error = nil", testCase.argv)
		}
	}
}
