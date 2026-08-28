package launchagent

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"mlink/internal/install"
	"mlink/internal/layout"
)

func TestPlanUsesInstalledBinaryAndDirectLaunchctlCommands(t *testing.T) {
	paths, err := layout.FromHome("/Users/test", "/Users/test/projects/mlink/build/mlink")
	if err != nil {
		t.Fatal(err)
	}
	resources, err := Plan(paths, 501)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 3 {
		t.Fatalf("resource count = %d", len(resources))
	}
	plist := resources[0]
	if plist.Target != "/Users/test/Library/LaunchAgents/dev.mlink.broker.plist" || plist.Mode.Perm() != 0o600 {
		t.Fatalf("plist resource = %#v", plist)
	}
	text := string(plist.Content)
	for _, want := range []string{paths.Binary, "broker", "serve", paths.Config, paths.Journal, paths.Socket} {
		if !strings.Contains(text, want) {
			t.Fatalf("plist missing %q", want)
		}
	}
	if strings.Contains(text, paths.SourceExecutable) {
		t.Fatal("LaunchAgent depends on project build path")
	}
	wantCommands := [][]string{
		{"launchctl", "bootstrap", "gui/501", plist.Target},
		{"launchctl", "kickstart", "-k", "gui/501/dev.mlink.broker"},
	}
	gotCommands := [][]string{resources[1].Command, resources[2].Command}
	if !reflect.DeepEqual(gotCommands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", gotCommands, wantCommands)
	}
	for _, resource := range resources[1:] {
		if resource.Action != install.ActionService {
			t.Fatalf("service action = %q", resource.Action)
		}
		for _, arg := range resource.Command {
			if arg == "sh" || arg == "-c" {
				t.Fatalf("shell command found: %#v", resource.Command)
			}
		}
	}
}

func TestPlanIsDeterministicAndEscapesXML(t *testing.T) {
	paths, err := layout.FromHome("/Users/test&operator", "/tmp/build/mlink")
	if err != nil {
		t.Fatal(err)
	}
	first, err := Plan(paths, 501)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Plan(paths, 501)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first[0].Content, second[0].Content) {
		t.Fatal("plist is not deterministic")
	}
	if strings.Contains(string(first[0].Content), "/Users/test&operator") || !strings.Contains(string(first[0].Content), "/Users/test&amp;operator") {
		t.Fatal("plist path was not XML escaped")
	}
}

func TestPlanRejectsInvalidUID(t *testing.T) {
	paths, err := layout.FromHome("/Users/test", "/tmp/mlink")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Plan(paths, 0); err == nil {
		t.Fatal("Plan() error = nil")
	}
}

func TestPlanUnloadRemovesOnlyOwnedServiceAndPlist(t *testing.T) {
	paths, err := layout.FromHome("/Users/test", "/tmp/mlink")
	if err != nil {
		t.Fatal(err)
	}
	resources, err := PlanUnload(paths, 501)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 || resources[0].Action != install.ActionService || resources[1].Action != install.ActionRemoveOwned {
		t.Fatalf("resources = %#v", resources)
	}
	if got := resources[0].Command; !reflect.DeepEqual(got, []string{"launchctl", "bootout", "gui/501/dev.mlink.broker"}) {
		t.Fatalf("bootout = %#v", got)
	}
}
