# MLink Full Backup and Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an encrypted full-workspace backup and restore workflow that moves MemoryCore, Knowledge, MLink identity/state, and Agent integrations to a new macOS computer without changing any Core-generated ID.

**Architecture:** A Provider snapshot registry separates backend-specific volume operations from a Provider-neutral encrypted bundle and restore orchestrator. TencentDB snapshots Core and Knowledge through pinned helper containers into encrypted section files, then wraps those ciphertext sections in one authenticated outer stream. Restore creates only empty formal resources, restores Core before MLink, verifies every fixed/dynamic ID and memory sample hash, then installs Agent adapters and runs Doctor.

**Tech Stack:** Go 1.27, `filippo.io/age` streaming scrypt encryption, standard `archive/tar`, Docker/OrbStack, SQLite `quick_check`, macOS Keychain, Bubble Tea.

**Spec:** `docs/superpowers/specs/2026-09-01-mlink-full-backup-restore-design.md`

## Global Constraints

- Restore never calls `init-admin`, `create-user`, `create-team`, or `create-agent` before restored identity verification.
- Fixed and dynamic Core IDs must match the encrypted manifest byte-for-byte.
- No secret, raw Feishu ID, memory content, document content, source path, or host identity enters argv, Plan JSON, normal logs, or an unencrypted temporary file.
- Temporary section files contain ciphertext only and live in a mode-`0700` directory.
- Existing non-empty target volumes are never overwritten.
- Formal TencentDB defaults remain `tdai-memory-core`, `tdai-memory-core-data`, `tdai-memory-stack`, and `~/.mlink/memorycore/tdai-gateway.yaml`.
- Ordinary uninstall retains restored Core and Knowledge volumes.
- First-release restore target is macOS arm64; other platforms fail preflight without mutation.

---

### Task 1: Streaming command boundary

**Files:**
- Create: `internal/install/stream.go`
- Create: `internal/install/stream_test.go`

**Interfaces:**
- Produces: `install.StreamRunner`, `install.LocalStreamRunner`, `install.LimitedWriter`.
- Consumed by: TencentDB snapshot driver in Task 5.

- [ ] **Step 1: Write failing streaming and cancellation tests**

```go
func TestLocalStreamRunnerDoesNotBufferStdout(t *testing.T) {
    destination := &countingWriter{}
    err := (LocalStreamRunner{}).RunStream(context.Background(),
        []string{"sh", "-c", "dd if=/dev/zero bs=1024 count=2048 2>/dev/null"}, nil, destination)
    if err != nil || destination.Bytes != 2<<20 { t.Fatalf("bytes/error = %d/%v", destination.Bytes, err) }
}

func TestLimitedWriterStopsOversizedStream(t *testing.T) {
    writer := &LimitedWriter{Writer: io.Discard, Limit: 10}
    _, err := writer.Write([]byte("01234567890"))
    if !errors.Is(err, ErrStreamLimit) { t.Fatalf("error = %v", err) }
}
```

- [ ] **Step 2: Run the focused tests and observe RED**

Run: `rtk go test ./internal/install -run 'TestLocalStreamRunner|TestLimitedWriter' -count=1`

Expected: compile failure because the streaming interfaces do not exist.

- [ ] **Step 3: Implement direct-argv streaming**

```go
type StreamRunner interface {
    RunStream(context.Context, []string, io.Reader, io.Writer) error
}

type LocalStreamRunner struct{}

func (LocalStreamRunner) RunStream(ctx context.Context, argv []string, stdin io.Reader, stdout io.Writer) error {
    if len(argv) == 0 || stdout == nil { return errors.New("stream command and stdout are required") }
    command := exec.CommandContext(ctx, argv[0], argv[1:]...)
    command.Stdin, command.Stdout = stdin, stdout
    command.Stderr = &boundedErrorBuffer{Limit: 32 << 10}
    return command.Run()
}
```

Error formatting must include only exit code and redacted bounded stderr. It must not include stdin or environment values.

- [ ] **Step 4: Verify focused and install transaction tests**

Run: `rtk go test ./internal/install -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/install/stream.go internal/install/stream_test.go
rtk git commit -m "feat: add bounded streaming command runner"
```

### Task 2: Encrypted workspace bundle contract

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/workspacebackup/types.go`
- Create: `internal/workspacebackup/crypto.go`
- Create: `internal/workspacebackup/crypto_test.go`

**Interfaces:**
- Produces: `workspacebackup.Manifest`, `Section`, `Encrypt`, `Decrypt`, `InspectHeader`, `Wipe`.
- Consumes: `filippo.io/age` and `age.NewScryptRecipient/Identity`.

- [ ] **Step 1: Add failing authentication, truncation, and redaction tests**

```go
func TestEncryptedEnvelopeRoundTripAndAuthentication(t *testing.T) {
    var encrypted bytes.Buffer
    err := Encrypt(&encrypted, []byte("correct horse battery"), strings.NewReader("private"))
    if err != nil { t.Fatal(err) }
    var clear bytes.Buffer
    if err := Decrypt(&clear, []byte("correct horse battery"), bytes.NewReader(encrypted.Bytes())); err != nil { t.Fatal(err) }
    if clear.String() != "private" { t.Fatalf("clear = %q", clear.String()) }
    encrypted.Bytes()[len(encrypted.Bytes())-1] ^= 1
    if err := Decrypt(io.Discard, []byte("correct horse battery"), bytes.NewReader(encrypted.Bytes())); !errors.Is(err, ErrAuthentication) {
        t.Fatalf("tamper error = %v", err)
    }
}

func TestManifestFormattingNeverContainsIdentityOrSecrets(t *testing.T) {
    manifest := Manifest{ControlPlane: ControlPlane{OwnerAgentID: "agt-secret"}}
    rendered := fmt.Sprintf("%#v", manifest)
    if strings.Contains(rendered, "agt-secret") || !strings.Contains(rendered, "<redacted>") { t.Fatalf("unsafe = %s", rendered) }
}
```

- [ ] **Step 2: Run tests and observe RED**

Run: `rtk go test ./internal/workspacebackup -run 'TestEncryptedEnvelope|TestManifestFormatting' -count=1`

Expected: package missing.

- [ ] **Step 3: Add the age dependency and implement the envelope**

Run: `rtk go get filippo.io/age@v1.2.1`

Use magic `MLINK-BACKUP\n`, format `mlink-full-backup/v1`, scrypt work factor `18`, and a maximum passphrase length of 4096 bytes. Convert the passphrase to the age-required string only at the call boundary, clear byte slices immediately, and never retain the string on a struct.

```go
func Encrypt(dst io.Writer, passphrase []byte, clear io.Reader) error
func Decrypt(dst io.Writer, passphrase []byte, encrypted io.Reader) error
func InspectHeader(src io.Reader) (Header, error)
```

Define the shared manifest types in the same task:

```go
type Manifest struct {
    Format string `json:"format"`
    CreatedAt time.Time `json:"created_at"`
    MLink version.Info `json:"mlink"`
    Provider ProviderManifest `json:"provider"`
    ControlPlane ControlPlaneManifest `json:"control_plane"`
    PrincipalAgents []PrincipalAgentManifest `json:"principal_agents"`
    Agents []string `json:"agents"`
    Sections []SectionManifest `json:"sections"`
}

type ProviderManifest struct {
    ProviderID string `json:"provider_id"`
    DriverVersion string `json:"driver_version"`
    InstanceID string `json:"instance_id"`
    CoreImageDigest string `json:"core_image_digest"`
    HubImageDigest string `json:"hub_image_digest"`
    Volumes []VolumeManifest `json:"volumes"`
}
```

`ControlPlaneManifest`, `PrincipalAgentManifest`, and `Manifest.String/GoString` must redact IDs to suffix fingerprints. JSON is used only inside the encrypted envelope.

- [ ] **Step 4: Verify package tests, fuzz seeds, and secret scanning**

Run:

```bash
rtk go test ./internal/workspacebackup -count=1
rtk go test ./internal/workspacebackup -run Fuzz -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add go.mod go.sum internal/workspacebackup
rtk git commit -m "feat: define encrypted workspace bundle"
```

### Task 3: Ciphertext section packer and manifest checksums

**Files:**
- Create: `internal/workspacebackup/sections.go`
- Create: `internal/workspacebackup/sections_test.go`

**Interfaces:**
- Produces: `SectionWriter`, `SectionReader`, `Pack`, `Open`, `VerifyManifest`.
- Consumes: Task 2 encryption functions.

- [ ] **Step 1: Write failing tests for ciphertext-only staging**

```go
func TestPackStagesOnlyEncryptedSections(t *testing.T) {
    staging := t.TempDir()
    bundle := filepath.Join(t.TempDir(), "workspace.mlink-backup")
    source := SectionSource{Name: SectionCore, Open: func(context.Context) (io.ReadCloser, error) {
        return io.NopCloser(strings.NewReader("CANARY-CLEAR")), nil
    }}
    staged, err := EncryptSection(context.Background(), staging, []byte("twelve-byte-passphrase"), source)
    if err != nil { t.Fatal(err) }
    ciphertext, _ := os.ReadFile(staged.Path)
    if bytes.Contains(ciphertext, []byte("CANARY-CLEAR")) { t.Fatal("plaintext reached staging") }
    err = (Packer{StagingParent: staging}).Pack(context.Background(), bundle,
        []byte("twelve-byte-passphrase"), fixtureManifest(), source)
    if err != nil { t.Fatal(err) }
    entries, _ := os.ReadDir(staging)
    if len(entries) != 1 { t.Fatalf("staging cleanup left %d entries", len(entries)) }
}
```

Also cover duplicate section names, unknown mandatory sections, wrong inner checksum, archive path traversal, output symlink refusal, mode `0600`, staging mode `0700`, and cleanup on every injected failure.

- [ ] **Step 2: Run and observe RED**

Run: `rtk go test ./internal/workspacebackup -run 'TestPack|TestOpen|TestVerifyManifest' -count=1`

Expected: missing types.

- [ ] **Step 3: Implement double encrypted streaming sections**

Each logical source streams through Task 2 encryption into a private ciphertext `.agepart`. `Pack` writes `manifest.agepart`, `checksums.agepart`, and data section parts into an outer tar stream which is itself passed through `Encrypt` to the final atomic output. No plaintext section file is created.

```go
type SectionSource struct {
    Name Section
    Open func(context.Context) (io.ReadCloser, error)
}

type Packer struct { StagingParent string }

func EncryptSection(ctx context.Context, staging string, passphrase []byte, source SectionSource) (StagedSection, error)
func (Packer) Pack(ctx context.Context, output string, passphrase []byte, manifest Manifest, sections ...SectionSource) error
func Open(ctx context.Context, input string, passphrase []byte, visitor func(Section, io.Reader) error) (Manifest, error)
```

- [ ] **Step 4: Verify package tests**

Run: `rtk go test ./internal/workspacebackup -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/workspacebackup/sections.go internal/workspacebackup/sections_test.go
rtk git commit -m "feat: pack authenticated backup sections"
```

### Task 4: Provider snapshot lifecycle registry

**Files:**
- Create: `internal/provider/lifecycle/snapshot.go`
- Create: `internal/provider/lifecycle/snapshot_test.go`

**Interfaces:**
- Produces: `SnapshotDriver`, `SnapshotSource`, `BackupRequest`, `RestoreRequest`, `SnapshotRegistry`.
- Consumes: `workspacebackup.Manifest`, `install.ChangeSet`.

- [ ] **Step 1: Write failing registry and secret-redaction tests**

```go
type SnapshotDriver interface {
    Detect(context.Context) (SnapshotSource, error)
    PlanBackup(context.Context, BackupRequest) (install.ChangeSet, error)
    StreamBackup(context.Context, BackupRequest, io.Writer) (workspacebackup.ProviderManifest, error)
    PlanRestore(context.Context, RestoreRequest, workspacebackup.ProviderManifest) (install.ChangeSet, error)
    ApplyRestore(context.Context, RestoreRequest, workspacebackup.ProviderManifest, io.Reader) error
    VerifyRestore(context.Context, workspacebackup.ProviderManifest) error
}
```

Test duplicate registration, exact Provider lookup, unavailable Provider, immutable returned registrations, request formatting with `<redacted>`, and rejection of relative/symlink output paths.

- [ ] **Step 2: Run and observe RED**

Run: `rtk go test ./internal/provider/lifecycle -run Snapshot -count=1`

- [ ] **Step 3: Implement the explicit non-global registry**

Do not add snapshot methods to the existing `BackendLifecycle`; deployment and snapshot ownership remain separate interfaces registered under the same Provider ID.

- [ ] **Step 4: Verify lifecycle tests**

Run: `rtk go test ./internal/provider/lifecycle -count=1`

- [ ] **Step 5: Commit**

```bash
rtk git add internal/provider/lifecycle/snapshot.go internal/provider/lifecycle/snapshot_test.go
rtk git commit -m "feat: define provider snapshot lifecycle"
```

### Task 5: TencentDB Core and Knowledge snapshot driver

**Files:**
- Create: `internal/provider/tencentdb/snapshot.go`
- Create: `internal/provider/tencentdb/snapshot_test.go`

**Interfaces:**
- Produces: `tencentdb.SnapshotDriver` implementing Task 4.
- Consumes: `install.StreamRunner`, Docker inspect, official image constants, Core metadata client.

- [ ] **Step 1: Write failing source detection and Plan tests**

Fixtures cover:

- owned formal Core and Knowledge volumes;
- explicitly approved compatible external Core;
- unowned/incompatible container refusal;
- missing/empty/non-volume mount refusal;
- exact helper image `docker.io/library/alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc`;
- no `docker volume rm` in backup/ordinary restore Plans;
- no secret in argv or Plan;
- Core stop after Broker pause and restart before Broker resume;
- Knowledge stop/start symmetry.

```go
func TestSnapshotPlanUsesReadOnlyVolumeMountsAndPinnedHelper(t *testing.T) {
    plan, err := driver.PlanBackup(ctx, request)
    if err != nil { t.Fatal(err) }
    for _, operation := range plan.Operations {
        joined := strings.Join(operation.Command, " ")
        if strings.Contains(joined, "volume rm") || strings.Contains(joined, "gateway-secret") { t.Fatalf("unsafe: %s", joined) }
    }
}
```

- [ ] **Step 2: Run and observe RED**

Run: `rtk go test ./internal/provider/tencentdb -run Snapshot -count=1`

- [ ] **Step 3: Implement backup streaming**

The driver runs the pinned helper with direct argv:

```text
docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v <volume>:/source:ro <pinned-alpine> tar -C /source -cf - .
```

Logical size/file count and SQLite checks run in separate read-only helpers. `StreamBackup` writes tar bytes only to the supplied encrypted section writer.

- [ ] **Step 4: Implement restore and verification**

Restore creates a new labeled empty volume, then streams authenticated tar bytes to:

```text
docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges \
  -i -v <new-volume>:/target <pinned-alpine> tar -C /target -xf -
```

`VerifyRestore` starts Core on a temporary loopback port and uses restored Owner credentials to verify User/Team/Agent/Asset and all dynamic mappings. It reads bounded memory samples named in the encrypted manifest and compares hashes without logging content.

- [ ] **Step 5: Run Provider and live-disabled tests**

Run: `rtk go test ./internal/provider/tencentdb -count=1`

Expected: PASS; live tests skip without their explicit environment gate.

- [ ] **Step 6: Commit**

```bash
rtk git add internal/provider/tencentdb/snapshot.go internal/provider/tencentdb/snapshot_test.go
rtk git commit -m "feat: snapshot tencentdb memory volumes"
```

### Task 6: Full backup orchestration service

**Files:**
- Create: `internal/app/workspace_backup.go`
- Create: `internal/app/workspace_backup_test.go`
- Modify: `internal/app/service.go`

**Interfaces:**
- Produces: `Service.PlanWorkspaceBackup`, `ApplyWorkspaceBackup`, `InspectWorkspaceBackup`.
- Consumes: snapshot driver, Keychain, Journal, config, Task 3 packer.

- [ ] **Step 1: Write failing zero-write preview and service-restart tests**

```go
type WorkspaceBackupRequest struct {
    OutputPath string
    Passphrase []byte
    ApproveExternalSource bool
}

func (request WorkspaceBackupRequest) String() string {
    return fmt.Sprintf("WorkspaceBackupRequest{OutputPath:%q Passphrase:<redacted>}", filepath.Base(request.OutputPath))
}
```

Test that Plan does not create output/staging/Keychain writes, Apply stops and restarts services in order, every injected snapshot/encryption/write failure restarts the original stack, and output is atomic.

- [ ] **Step 2: Run and observe RED**

Run: `rtk go test ./internal/app -run WorkspaceBackup -count=1`

- [ ] **Step 3: Implement manifest and section composition**

The service reads config, control-plane state, dynamic mappings, active adapters, Journal schema/version, image digests, and Keychain values. It streams all secrets directly into encrypted sections and wipes them after closing the section writer.

The Gateway token, LLM key, Hermes grant, identity HMAC, Admin/Owner keys, and bindings must be present for a full backup. Missing optional Knowledge data is represented explicitly; missing required Core identity fails.

- [ ] **Step 4: Verify app and secret tests**

Run: `rtk go test ./internal/app ./internal/secret ./internal/workspacebackup -count=1`

- [ ] **Step 5: Commit**

```bash
rtk git add internal/app/workspace_backup.go internal/app/workspace_backup_test.go internal/app/service.go
rtk git commit -m "feat: orchestrate full workspace backup"
```

### Task 7: Restore planner, apply transaction, and ID gate

**Files:**
- Create: `internal/app/workspace_restore.go`
- Create: `internal/app/workspace_restore_test.go`
- Modify: `internal/app/service.go`

**Interfaces:**
- Produces: `Service.PlanWorkspaceRestore`, `ApplyWorkspaceRestore`, `WorkspaceRestoreStatus`.
- Consumes: Tasks 3–6.

- [ ] **Step 1: Write failing refusal and rollback tests**

```go
type WorkspaceRestoreRequest struct {
    BundlePath string
    Passphrase []byte
    SelectedAgents []Agent
}
```

Test wrong passphrase, corrupt bundle, incompatible platform/schema, insufficient disk, existing container, non-empty target volume, existing Keychain collision, SQLite failure, fixed-ID mismatch, dynamic-ID mismatch, memory-hash mismatch, and protected Agent-config mutation. Assert zero writes for every Plan failure.

- [ ] **Step 2: Run and observe RED**

Run: `rtk go test ./internal/app -run WorkspaceRestore -count=1`

- [ ] **Step 3: Implement exact restore planning**

Plan lists formal containers, volumes, network, config, MLink state files, Keychain accounts, Agent semantic merges, service start order, rollback operations, image digests, byte counts, and ID fingerprints. Secret values and full source paths are absent.

- [ ] **Step 4: Implement restore apply order**

Apply uses a private restore transaction state file containing only operation IDs/status, never secrets. It restores volumes, starts verification Core, verifies all IDs/hashes, restores MLink/Keychain, installs Agent adapters, switches to final Core, starts Hub/Broker, and invokes Doctor. Any failure removes only resources recorded as created by this Plan.

- [ ] **Step 5: Assert restore never provisions metadata**

Add a metadata client double whose `InitAdmin`, `CreateUser`, `CreateTeam`, and `CreateAgent` methods fail the test if called. Successful restore must use only verify/list/get/read APIs.

- [ ] **Step 6: Verify app/e2e offline tests**

Run: `rtk go test ./internal/app ./internal/e2e -run 'WorkspaceRestore|Identity' -count=1`

- [ ] **Step 7: Commit**

```bash
rtk git add internal/app/workspace_restore.go internal/app/workspace_restore_test.go internal/app/service.go
rtk git commit -m "feat: restore workspace without identity rewrite"
```

### Task 8: Credential inventory and controlled Panel key copy

**Files:**
- Create: `internal/app/credentials.go`
- Create: `internal/app/credentials_test.go`
- Create: `internal/cli/credentials.go`
- Create: `internal/cli/credentials_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/help.go`
- Modify: `cmd/mlink/runtime.go`

**Interfaces:**
- Produces: `CredentialStatus`, `CredentialRole`, `CopyCredential`.

- [ ] **Step 1: Write failing status/redaction/authorization tests**

```go
const (
    CredentialPanelOwner CredentialRole = "panel-owner"
    CredentialPanelAdmin CredentialRole = "panel-admin"
)

type CredentialStatus struct {
    Role CredentialRole `json:"role"`
    Present bool `json:"present"`
    Fingerprint string `json:"fingerprint,omitempty"`
    CopyAllowed bool `json:"copy_allowed"`
}
```

Test that Gateway/LLM/HMAC/Hermes/binding roles report presence/fingerprint but reject copy, while Admin/Owner require `--yes`, write only to `pbcopy` stdin, and never enter stdout/stderr.

- [ ] **Step 2: Run and observe RED**

Run: `rtk go test ./internal/app ./internal/cli -run Credential -count=1`

- [ ] **Step 3: Implement Keychain inventory and CLI**

Commands:

```text
mlink credentials status --json
mlink credentials copy panel-owner --yes
mlink credentials copy panel-admin --yes
```

The CLI prints a clipboard-retention warning before copy and a completion line afterward. It does not print secret length or prefix.

- [ ] **Step 4: Verify tests**

Run: `rtk go test ./internal/app ./internal/cli ./cmd/mlink -run Credential -count=1`

- [ ] **Step 5: Commit**

```bash
rtk git add internal/app/credentials.go internal/app/credentials_test.go internal/cli/credentials.go internal/cli/credentials_test.go internal/cli/run.go internal/cli/help.go cmd/mlink/runtime.go
rtk git commit -m "feat: manage panel login credentials"
```

### Task 9: Backup and restore CLI

**Files:**
- Modify: `internal/cli/backup.go`
- Modify: `internal/cli/backup_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/help.go`
- Create: `cmd/mlink/runtime_backup.go`
- Create: `cmd/mlink/runtime_backup_test.go`

**Interfaces:**
- Exposes Tasks 6–7 through exact Plan/Apply CLI commands.

- [ ] **Step 1: Write failing protected-input and exact-Plan tests**

Cover create/inspect/restore, bounded passphrase stdin, absolute paths, symlink rejection, no passphrase echo, wrong/stale Plan ID, `--yes`, external-source approval, JSON output, and stable exit codes.

- [ ] **Step 2: Run and observe RED**

Run: `rtk go test ./internal/cli ./cmd/mlink -run 'WorkspaceBackup|WorkspaceRestore' -count=1`

- [ ] **Step 3: Implement CLI parsing and runtime dependency wiring**

Use `readBoundedLine(input, 4096)` for passphrases and wipe immediately after application calls. `backup inspect` is read-only and never extracts data to disk.

- [ ] **Step 4: Verify CLI and packaged binary help**

Run:

```bash
rtk go test ./internal/cli ./cmd/mlink -count=1
candidate_dir="$(mktemp -d)"
rtk env -u GOROOT CGO_ENABLED=0 go build -trimpath -o "$candidate_dir/mlink" ./cmd/mlink
rtk "$candidate_dir/mlink" help backup
```

- [ ] **Step 5: Commit**

```bash
rtk git add internal/cli/backup.go internal/cli/backup_test.go internal/cli/run.go internal/cli/help.go cmd/mlink/runtime_backup.go cmd/mlink/runtime_backup_test.go
rtk git commit -m "feat: expose full backup and restore cli"
```

### Task 10: Guided TUI backup, restore, and credentials flows

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/view_test.go`

**Interfaces:**
- Consumes: Tasks 6–9 application methods.
- Produces: restore-first welcome mode and credentials management screens.

- [ ] **Step 1: Write failing transition and secret-wipe tests**

Cover:

- Welcome selection among new install / restore / existing Core;
- bundle path input and passphrase masking;
- manifest summary without IDs/content;
- dependency failure before confirmation;
- restore Plan and dedicated confirmation;
- busy-state key suppression;
- retry after failure without duplicated mutation;
- passphrase wipe on success, failure, quit, and panic-free window resize;
- credentials copy confirmation;
- keyboard navigation bounds derived from option counts.

- [ ] **Step 2: Run and observe RED**

Run: `rtk go test ./internal/tui -run 'Backup|Restore|Credential' -count=1`

- [ ] **Step 3: Implement restore-first state machine**

Add focused submodels/files when `model.go` would exceed one responsibility: `restore_model.go`, `restore_view.go`, and `credentials_model.go`. Do not put archive, Docker, or Keychain logic in the TUI package.

- [ ] **Step 4: Render every state at supported widths**

Run: `rtk go test ./internal/tui -run TestEvery -count=1`

Snapshots must fit 80×24, 100×30, and 140×40 without overflow. Restore screens show result first, details second, and never display raw IDs or paths wider than the viewport.

- [ ] **Step 5: Verify all TUI tests**

Run: `rtk go test ./internal/tui -count=1`

- [ ] **Step 6: Commit**

```bash
rtk git add internal/tui
rtk git commit -m "feat: guide full workspace restore in tui"
```

### Task 11: Doctor, resume evidence, and semantic uninstall

**Files:**
- Modify: `cmd/mlink/diagnostics.go`
- Modify: `cmd/mlink/main_test.go`
- Modify: `internal/app/uninstall.go`
- Modify: `internal/app/uninstall_test.go`
- Modify: `internal/journal/store.go`
- Create: `internal/journal/migrations/007_restore_operations.sql`
- Create: `internal/journal/restore.go`
- Create: `internal/journal/restore_test.go`

**Interfaces:**
- Adds Doctor checks: `backup.last_verified`, `restore.state`, `restore.identity_gate`.
- Persists non-secret restore operation state for diagnostics and safe retry.

- [ ] **Step 1: Write failing migration/status/uninstall tests**

Test interrupted restore states, stale operation refusal, no secret/path in Journal, successful completion, failed restore diagnostics, and ordinary uninstall retaining restored volumes.

- [ ] **Step 2: Run and observe RED**

Run: `rtk go test ./internal/journal ./internal/app ./cmd/mlink -run 'Restore|Backup' -count=1`

- [ ] **Step 3: Implement restore-operation persistence and Doctor checks**

Store only operation ID, bundle fingerprint, Plan ID, phase enum, created resource owner IDs, timestamps, and error code. Never store passphrase, source path, raw target path, IDs beyond suffix/fingerprint, or memory hashes.

- [ ] **Step 4: Verify diagnostics and uninstall**

Run: `rtk go test ./internal/journal ./internal/app ./cmd/mlink -count=1`

- [ ] **Step 5: Commit**

```bash
rtk git add cmd/mlink/diagnostics.go cmd/mlink/main_test.go internal/app/uninstall.go internal/app/uninstall_test.go internal/journal
rtk git commit -m "feat: diagnose and recover workspace restore"
```

### Task 12: Isolated live backup/restore acceptance and documentation

**Files:**
- Create: `internal/e2e/live_workspace_restore_test.go`
- Create: `docs/testing/mlink-full-backup-restore-live.md`
- Modify: `README.md`
- Modify: `README.zh-CN.md`

**Interfaces:**
- Verifies the complete official-image workflow on isolated resources.

- [ ] **Step 1: Add an explicit-gated live fixture**

`TestLiveFullWorkspaceBackupRestore` runs only when `MLINK_TEST_FULL_RESTORE=1`. It generates names prefixed `mlink-restore-e2e-`, refuses ports `8420/8125/8424`, refuses production container/volume/network names, and cleans only exact prefixed resources.

- [ ] **Step 2: Exercise the full lifecycle**

The test must:

1. start official Core/Hub on isolated ports;
2. provision fixed and at least two dynamic Agents;
3. capture distinct Owner/DM/group canaries and build one Knowledge asset fixture;
4. create an encrypted full backup;
5. destroy only the isolated source stack;
6. restore into different isolated names/ports;
7. prove every fixed/dynamic ID equal;
8. prove each canary remains isolated and recallable;
9. prove Panel Owner login and Knowledge health;
10. prove ordinary uninstall retains restored volumes;
11. prove no test resources remain after final cleanup.

- [ ] **Step 3: Run full quality gates**

Run:

```bash
rtk go test ./... -count=1
rtk go test ./... -shuffle=on -count=1
rtk go test -race ./internal/workspacebackup ./internal/provider/tencentdb ./internal/controlplane ./internal/app ./internal/e2e -count=1
rtk go vet ./...
rtk git diff --check
```

- [ ] **Step 4: Run the isolated live acceptance**

Provide the memory LLM inputs only through the process environment and run:

```bash
MLINK_TEST_FULL_RESTORE=1 \
MLINK_TEST_LLM_BASE_URL=<https-or-loopback-url> \
MLINK_TEST_LLM_MODEL=<model> \
MLINK_TEST_LLM_API_KEY=<protected-key> \
go test ./internal/e2e -run '^TestLiveFullWorkspaceBackupRestore$' -count=1 -v -timeout=15m
```

Record image digests, redacted ID suffixes, bundle size/hash, Plan IDs, restore duration, and cleanup evidence without secrets or content.

- [ ] **Step 5: Update bilingual documentation**

Document full backup versus automatic install backup versus identity export, source/destination requirements, TUI screenshots, failure recovery, and the explicit fact that restore preserves IDs rather than migrating them.

- [ ] **Step 6: Commit**

```bash
rtk git add internal/e2e/live_workspace_restore_test.go docs/testing/mlink-full-backup-restore-live.md README.md README.zh-CN.md
rtk git commit -m "test: accept full workspace backup and restore"
```
