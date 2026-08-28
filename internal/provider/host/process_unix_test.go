//go:build unix

package host

import (
	"os/exec"
	"testing"
)

func TestConfigureProcessCreatesIndependentProcessGroup(t *testing.T) {
	command := exec.Command("/usr/bin/true")
	configureProcess(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %#v, want Setpgid", command.SysProcAttr)
	}
}
