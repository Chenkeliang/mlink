package journal

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
)

func TestControlPlaneMigrationHasNoRawExternalIDColumns(t *testing.T) {
	store := openTestStore(t)
	for _, table := range []string{"control_plane_installations", "principal_agents"} {
		rows, err := store.db.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, column)
		}
		_ = rows.Close()
		joined := strings.Join(columns, ",")
		for _, forbidden := range []string{"union_id", "open_id", "chat_id", "thread_id", "external_id"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("%s contains forbidden column %q: %s", table, forbidden, joined)
			}
		}
	}
}

func TestLoadControlPlaneTreatsPreV4JournalAsUnprovisioned(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec(`DROP TABLE control_plane_installations`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadControlPlane(context.Background()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v", err)
	}
}

func TestSaveLoadAndMarkControlPlane(t *testing.T) {
	store := openTestStore(t)
	want := ControlPlaneState{
		InstallationID: "installation-1", InstanceID: "default",
		OwnerUserID: "usr-owner", OwnerTeamID: "team-owner", OwnerAgentID: "agt-owner",
		OwnerAssetID:   "chat_memory-team-owner-agt-owner",
		PanelContainer: "tdai-memory-hub", PanelImage: "agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104", State: "provisioned",
	}
	if err := store.SaveControlPlane(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadControlPlane(context.Background())
	if err != nil || got != want {
		t.Fatalf("LoadControlPlane() = %#v, %v", got, err)
	}
	if err := store.MarkControlPlaneState(context.Background(), "active"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.LoadControlPlane(context.Background())
	if got.State != "active" {
		t.Fatalf("state = %q", got.State)
	}
	conflict := want
	conflict.OwnerAgentID = "agt-other"
	if err := store.SaveControlPlane(context.Background(), conflict); err == nil {
		t.Fatal("SaveControlPlane() conflict error = nil")
	}
}

func TestPrincipalAgentInsertReuseListAndConflict(t *testing.T) {
	store := openTestStore(t)
	want := PrincipalAgent{
		Fingerprint: "prn_abcdefghijklmnopqrstuvwxyz", RouteKind: "hermes-private",
		BackendUserID: "usr-owner", BackendTeamID: "team-owner", BackendAgentID: "agt-private",
		BackendAssetID: "chat_memory-team-owner-agt-private", DisplayLabel: "Feishu DM · Alice", State: "active",
	}
	if err := store.PutPrincipalAgent(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPrincipalAgent(context.Background(), want); err != nil {
		t.Fatalf("idempotent PutPrincipalAgent() = %v", err)
	}
	got, err := store.GetPrincipalAgent(context.Background(), want.Fingerprint)
	if err != nil || got != want {
		t.Fatalf("GetPrincipalAgent() = %#v, %v", got, err)
	}
	listed, err := store.ListPrincipalAgents(context.Background())
	if err != nil || len(listed) != 1 || listed[0] != want {
		t.Fatalf("ListPrincipalAgents() = %#v, %v", listed, err)
	}
	conflict := want
	conflict.BackendAgentID = "agt-conflict"
	if err := store.PutPrincipalAgent(context.Background(), conflict); err == nil {
		t.Fatal("PutPrincipalAgent() conflict error = nil")
	}
}

func TestPrincipalAgentConcurrentIdempotentInsert(t *testing.T) {
	store := openTestStore(t)
	want := PrincipalAgent{
		Fingerprint: "prn_zyxwvutsrqponmlkjihgfedcba", RouteKind: "hermes-group",
		BackendUserID: "usr-owner", BackendTeamID: "team-owner", BackendAgentID: "agt-group",
		BackendAssetID: "chat_memory-team-owner-agt-group", DisplayLabel: "Feishu Group · Test", State: "active",
	}
	var wait sync.WaitGroup
	errorsChannel := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsChannel <- store.PutPrincipalAgent(context.Background(), want)
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	listed, err := store.ListPrincipalAgents(context.Background())
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %#v, %v", listed, err)
	}
}

func TestPrincipalAgentRejectsRawExternalIdentifiers(t *testing.T) {
	store := openTestStore(t)
	base := PrincipalAgent{
		Fingerprint: "prn_abcdefghijklmnopqrstuvwxyz", RouteKind: "hermes-private",
		BackendUserID: "usr-owner", BackendTeamID: "team-owner", BackendAgentID: "agt-private",
		BackendAssetID: "chat_memory-team-owner-agt-private", DisplayLabel: "Safe label", State: "active",
	}
	for _, mutate := range []func(*PrincipalAgent){
		func(value *PrincipalAgent) { value.Fingerprint = "ou_raw_feishu_id" },
		func(value *PrincipalAgent) { value.DisplayLabel = "Alice ou_raw_feishu_id" },
		func(value *PrincipalAgent) { value.RouteKind = "owner" },
	} {
		value := base
		mutate(&value)
		if err := store.PutPrincipalAgent(context.Background(), value); err == nil {
			t.Fatalf("PutPrincipalAgent(%#v) error = nil", value)
		}
	}
	if _, err := store.GetPrincipalAgent(context.Background(), "prn_missingmissingmissingmissin"); !errors.Is(err, ErrPrincipalAgentNotFound) {
		t.Fatalf("missing error = %v", err)
	}
}
