//go:build darwin

package secret

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPromptedWriterSuppliesSecretTwiceWithoutArgv(t *testing.T) {
	t.Setenv("MLINK_KEYCHAIN_PROMPT_HELPER", "1")
	secret := []byte("secret-via-terminal")
	args := []string{os.Args[0], "-test.run=TestPromptedWriterHelperProcess"}
	if err := (promptedWriter{timeout: 3 * time.Second}).Write(context.Background(), args, secret); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), string(secret)) {
		t.Fatal("secret leaked into helper arguments")
	}
}

func TestPromptedWriterAgainstTemporaryLoginKeychainItem(t *testing.T) {
	if os.Getenv("MLINK_TEST_KEYCHAIN_INTEGRATION") != "1" {
		t.Skip("set MLINK_TEST_KEYCHAIN_INTEGRATION=1 to exercise macOS security")
	}
	service := fmt.Sprintf("dev.mlink.integration.%d", time.Now().UnixNano())
	account := "connection/local/token"
	t.Cleanup(func() {
		_ = exec.Command("/usr/bin/security", "delete-generic-password", "-s", service, "-a", account).Run()
	})
	secret := []byte{0x00, 0x01, '\n', '\r', 0x7f, 0x80, 0xff}
	encoded := encodeKeychainSecret(secret)
	defer wipeBytes(encoded)
	args := []string{
		"/usr/bin/security", "add-generic-password", "-U",
		"-s", service, "-a", account, "-w",
	}
	if err := (promptedWriter{timeout: 5 * time.Second}).Write(context.Background(), args, encoded); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(
		"/usr/bin/security", "find-generic-password", "-s", service,
		"-a", account, "-w",
	).Output()
	stored := bytes.TrimSuffix(output, []byte{'\n'})
	decoded, decodeErr := decodeKeychainSecret(stored)
	if err != nil || decodeErr != nil || !bytes.Equal(decoded, secret) {
		t.Fatalf("read scratch secret: match=%t read_err=%v decode_err=%v", bytes.Equal(decoded, secret), err, decodeErr)
	}
}

func TestPromptedWriterHelperProcess(t *testing.T) {
	if os.Getenv("MLINK_KEYCHAIN_PROMPT_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	values := make([]string, 0, 2)
	for _, prompt := range []string{"password for new item: ", "retype password for new item: "} {
		_, _ = fmt.Fprint(os.Stderr, prompt)
		value, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(3)
		}
		values = append(values, strings.TrimRight(value, "\r\n"))
	}
	if values[0] != "secret-via-terminal" || values[1] != values[0] {
		os.Exit(4)
	}
	os.Exit(0)
}
