package install

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"reflect"
	"testing"
)

type fakeFile struct {
	content []byte
	mode    fs.FileMode
}

type fakeTarget struct {
	files       map[string]fakeFile
	initial     map[string][]byte
	actions     []string
	writes      int
	failAtWrite int
}

func newFakeTarget(files map[string][]byte) *fakeTarget {
	target := &fakeTarget{files: make(map[string]fakeFile, len(files)), initial: make(map[string][]byte, len(files))}
	for name, content := range files {
		target.files[name] = fakeFile{content: append([]byte(nil), content...), mode: 0o600}
		target.initial[name] = append([]byte(nil), content...)
	}
	return target
}

func (t *fakeTarget) Read(_ context.Context, target string) ([]byte, fs.FileMode, error) {
	file, ok := t.files[target]
	if !ok {
		return nil, 0, fs.ErrNotExist
	}
	return append([]byte(nil), file.content...), file.mode, nil
}

func (t *fakeTarget) WriteAtomic(_ context.Context, target string, content []byte, mode fs.FileMode) error {
	t.writes++
	action := "apply:" + target
	if bytes.Equal(content, t.initial[target]) {
		action = "rollback:" + target
	}
	t.actions = append(t.actions, action)
	if t.failAtWrite > 0 && t.writes == t.failAtWrite {
		return errors.New("injected write failure")
	}
	t.files[target] = fakeFile{content: append([]byte(nil), content...), mode: mode}
	return nil
}

func (t *fakeTarget) Remove(_ context.Context, target string) error {
	t.actions = append(t.actions, "remove:"+target)
	delete(t.files, target)
	return nil
}

func (t *fakeTarget) Run(context.Context, []string, io.Reader) ([]byte, error) {
	return nil, nil
}

type memoryLedger struct {
	backups map[string]Backup
	owned   []OwnedResource
	corrupt bool
}

func newMemoryLedger() *memoryLedger {
	return &memoryLedger{backups: make(map[string]Backup)}
}

func (l *memoryLedger) SaveBackup(_ context.Context, backup Backup) error {
	l.backups[backupKey(backup.PlanID, backup.Target)] = backup.clone()
	return nil
}

func (l *memoryLedger) LoadBackup(_ context.Context, planID, target string) (Backup, error) {
	backup, ok := l.backups[backupKey(planID, target)]
	if !ok {
		return Backup{}, fs.ErrNotExist
	}
	backup = backup.clone()
	if l.corrupt {
		backup.Content = []byte("corrupt")
	}
	return backup, nil
}

func (l *memoryLedger) RecordOwned(_ context.Context, resource OwnedResource) error {
	l.owned = append(l.owned, resource)
	return nil
}

func TestTransactionDoesNotWriteDuringPlan(t *testing.T) {
	target := newFakeTarget(map[string][]byte{"/config": []byte("before")})
	plan, err := BuildChangeSet(target, []DesiredResource{{OwnerID: "codex", Target: "/config", Content: []byte("after"), Mode: 0o600}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(target.files["/config"].content); got != "before" {
		t.Fatalf("wrote during plan: %q", got)
	}
	if len(target.actions) != 0 {
		t.Fatalf("actions during plan = %#v", target.actions)
	}
	if plan.Operations[0].BeforeHash == plan.Operations[0].ProposedHash {
		t.Fatal("hashes should differ")
	}
}

func TestBuildChangeSetPreservesDependencyOrder(t *testing.T) {
	target := newFakeTarget(nil)
	plan, err := BuildChangeSet(target, []DesiredResource{
		{OwnerID: "broker", Target: "z-broker", Content: []byte("binary"), Mode: 0o700},
		{OwnerID: "launchagent", Target: "a-launchagent", Content: []byte("plist"), Mode: 0o600},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{plan.Operations[0].Target, plan.Operations[1].Target}; !reflect.DeepEqual(got, []string{"z-broker", "a-launchagent"}) {
		t.Fatalf("operation order = %#v", got)
	}
}

func TestBuildChangeSetRejectsDuplicateTarget(t *testing.T) {
	target := newFakeTarget(nil)
	_, err := BuildChangeSet(target, []DesiredResource{
		{OwnerID: "one", Target: "/config", Content: []byte("one")},
		{OwnerID: "two", Target: "/config", Content: []byte("two")},
	})
	if err == nil {
		t.Fatal("BuildChangeSet() error = nil")
	}
}

func TestApplyFailureRollsBackInReverseOrder(t *testing.T) {
	target := newFakeTarget(map[string][]byte{"a": []byte("before-a"), "b": []byte("before-b")})
	target.failAtWrite = 2
	plan, err := BuildChangeSet(target, []DesiredResource{
		{OwnerID: "test", Target: "a", Content: []byte("after-a"), Mode: 0o600},
		{OwnerID: "test", Target: "b", Content: []byte("after-b"), Mode: 0o600},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = NewTransaction(target, newMemoryLedger()).Apply(context.Background(), plan)
	if err == nil {
		t.Fatal("expected failure")
	}
	want := []string{"apply:a", "apply:b", "rollback:a"}
	if !reflect.DeepEqual(want, target.actions) {
		t.Fatalf("actions = %#v, want %#v", target.actions, want)
	}
	if got := string(target.files["a"].content); got != "before-a" {
		t.Fatalf("a after rollback = %q", got)
	}
}

func TestApplyRejectsStalePlanBeforeWrite(t *testing.T) {
	target := newFakeTarget(map[string][]byte{"/config": []byte("before")})
	plan, err := BuildChangeSet(target, []DesiredResource{{OwnerID: "codex", Target: "/config", Content: []byte("after"), Mode: 0o600}})
	if err != nil {
		t.Fatal(err)
	}
	target.files["/config"] = fakeFile{content: []byte("user changed it"), mode: 0o600}
	if err := NewTransaction(target, newMemoryLedger()).Apply(context.Background(), plan); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(target.actions) != 0 {
		t.Fatalf("actions = %#v", target.actions)
	}
}

func TestApplyVerifiesBackupsBeforeFirstWrite(t *testing.T) {
	target := newFakeTarget(map[string][]byte{"/config": []byte("before")})
	plan, err := BuildChangeSet(target, []DesiredResource{{OwnerID: "codex", Target: "/config", Content: []byte("after"), Mode: 0o600}})
	if err != nil {
		t.Fatal(err)
	}
	ledger := newMemoryLedger()
	ledger.corrupt = true
	if err := NewTransaction(target, ledger).Apply(context.Background(), plan); !errors.Is(err, ErrBackupCorrupt) {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(target.actions) != 0 {
		t.Fatalf("actions = %#v", target.actions)
	}
}

func TestApplyRejectsBrokenProtectedInvariant(t *testing.T) {
	target := newFakeTarget(map[string][]byte{"/config": []byte("before")})
	plan, err := BuildChangeSet(target, []DesiredResource{{
		OwnerID:             "codex",
		Target:              "/config",
		Content:             []byte("after"),
		Mode:                0o600,
		ProtectedInvariants: []Invariant{{Name: "model_provider", Preserved: false}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewTransaction(target, newMemoryLedger()).Apply(context.Background(), plan); !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(target.actions) != 0 {
		t.Fatalf("actions = %#v", target.actions)
	}
}
