package tencentdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetadataClientUsesOfficialRoutesAndHeaders(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		if got := request.Header.Get("Authorization"); got != "Bearer gateway-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("x-tdai-service-id"); got != "default" {
			t.Errorf("service id = %q", got)
		}
		wantUserKey := "owner-key"
		if request.URL.Path == "/v3/internal/meta/user/init-admin" {
			wantUserKey = ""
		} else if request.URL.Path == "/v3/meta/user/create" {
			wantUserKey = "admin-key"
		}
		if got := request.Header.Get("x-tdai-user-key"); got != wantUserKey {
			t.Errorf("user key for %s = %q, want %q", request.URL.Path, got, wantUserKey)
		}
		writer.Header().Set("Content-Type", "application/json")
		responses := map[string]string{
			"/v3/internal/meta/user/init-admin": `{"user_id":"usr-admin","user_key":"admin-key"}`,
			"/v3/meta/user/create":              `{"user_id":"usr-owner","user_type":"normal","created_at":"2026-08-31T00:00:00Z","default_user_key":"owner-key"}`,
			"/v3/meta/auth/verify":              `{"valid":true,"user":{"user_id":"usr-owner","user_type":"normal","username":"keliang","created_at":"2026-08-31T00:00:00Z"}}`,
			"/v3/meta/team/create":              `{"team_id":"team-owner","name":"MLink","description":"Owner team","owner_user_id":"usr-owner","status":"active","created_at":"2026-08-31T00:00:00Z","updated_at":"2026-08-31T00:00:00Z","metadata_json":"{}"}`,
			"/v3/meta/team/list":                `{"items":[{"team_id":"team-owner","name":"MLink","description":"Owner team","owner_user_id":"usr-owner","status":"active","created_at":"2026-08-31T00:00:00Z","updated_at":"2026-08-31T00:00:00Z","metadata_json":"{}"}],"total":1,"limit":100,"offset":0}`,
			"/v3/meta/agent/create":             `{"agent_id":"agt-owner","team_id":"team-owner","owner_user_id":"usr-owner","name":"MLink Owner","description":"Owner memory","prompt":"","visibility":"private","status":"active","created_at":"2026-08-31T00:00:00Z","updated_at":"2026-08-31T00:00:00Z","metadata_json":"{}"}`,
			"/v3/meta/agent/list":               `{"items":[{"agent_id":"agt-owner","team_id":"team-owner","owner_user_id":"usr-owner","name":"MLink Owner","description":"Owner memory","prompt":"","visibility":"private","status":"active","created_at":"2026-08-31T00:00:00Z","updated_at":"2026-08-31T00:00:00Z","metadata_json":"{}"}],"total":1,"limit":100,"offset":0}`,
			"/v3/meta/asset/get":                `{"asset_id":"chat_memory-team-owner-agt-owner","team_id":"team-owner","asset_type":"chat_memory","name":"MLink Owner","owner_user_id":"usr-owner","source_type":"agent","description":"","source_ref":"agt-owner","visibility":"private","status":"approved","confidence":1,"expires_at":null,"last_used_at":null,"usage_count":0,"content_ref":"","metadata_json":"{}","created_at":"2026-08-31T00:00:00Z","updated_at":"2026-08-31T00:00:00Z","version":1}`,
			"/v3/meta/instance-quota/get":       `{"max_users_per_instance":100,"max_teams_per_instance":20}`,
		}
		_, _ = writer.Write([]byte(`{"code":0,"message":"ok","request_id":"req-1","data":` + responses[request.URL.Path] + `}`))
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Token: "gateway-secret", ServiceID: "default"})
	if err != nil {
		t.Fatal(err)
	}
	metadata := NewMetadataClient(client)
	ctx := context.Background()
	admin, err := metadata.InitAdmin(ctx, InitAdminRequest{Username: "mlink-admin"})
	if err != nil || admin.UserID != "usr-admin" || string(admin.UserKey) != "admin-key" {
		t.Fatalf("admin = %#v, %v", admin, err)
	}
	owner, err := metadata.CreateUser(ctx, []byte("admin-key"), CreateUserRequest{Username: "keliang"})
	if err != nil || owner.UserID != "usr-owner" || string(owner.UserKey) != "owner-key" {
		t.Fatalf("owner = %#v, %v", owner, err)
	}
	verified, err := metadata.VerifyUser(ctx, []byte("owner-key"))
	if err != nil || verified.UserID != owner.UserID || verified.UserType != "normal" {
		t.Fatalf("verified = %#v, %v", verified, err)
	}
	team, err := metadata.CreateTeam(ctx, []byte("owner-key"), CreateTeamRequest{Name: "MLink", OwnerUserID: owner.UserID, Description: "Owner team"})
	if err != nil || team.TeamID != "team-owner" || team.OwnerUserID != owner.UserID {
		t.Fatalf("team = %#v, %v", team, err)
	}
	teams, err := metadata.ListTeams(ctx, []byte("owner-key"), ListTeamsRequest{UserID: owner.UserID, Name: "MLink", Limit: 100})
	if err != nil || len(teams) != 1 || teams[0].TeamID != team.TeamID {
		t.Fatalf("teams = %#v, %v", teams, err)
	}
	agent, err := metadata.CreateAgent(ctx, []byte("owner-key"), CreateAgentRequest{TeamID: team.TeamID, OwnerUserID: owner.UserID, Name: "MLink Owner", Description: "Owner memory", Visibility: "private", MetadataJSON: "{}"})
	if err != nil || agent.AgentID != "agt-owner" {
		t.Fatalf("agent = %#v, %v", agent, err)
	}
	agents, err := metadata.ListAgents(ctx, []byte("owner-key"), ListAgentsRequest{TeamID: team.TeamID, OwnerUserID: owner.UserID, Limit: 100})
	if err != nil || len(agents) != 1 || agents[0].AgentID != agent.AgentID {
		t.Fatalf("agents = %#v, %v", agents, err)
	}
	asset, err := metadata.GetAsset(ctx, []byte("owner-key"), "chat_memory-team-owner-agt-owner")
	if err != nil || asset.AssetID == "" || asset.OwnerUserID != owner.UserID {
		t.Fatalf("asset = %#v, %v", asset, err)
	}
	quota, err := metadata.InstanceQuota(ctx, []byte("owner-key"))
	if err != nil || quota.MaxUsers != 100 || quota.MaxTeams != 20 {
		t.Fatalf("quota = %#v, %v", quota, err)
	}
	wantPaths := "/v3/internal/meta/user/init-admin,/v3/meta/user/create,/v3/meta/auth/verify,/v3/meta/team/create,/v3/meta/team/list,/v3/meta/agent/create,/v3/meta/agent/list,/v3/meta/asset/get,/v3/meta/instance-quota/get"
	if strings.Join(paths, ",") != wantPaths {
		t.Fatalf("paths = %s", strings.Join(paths, ","))
	}
	admin.Wipe()
	owner.Wipe()
	if len(admin.UserKey) != 0 || len(owner.UserKey) != 0 {
		t.Fatal("credentials were not wiped")
	}
}

func TestMetadataCredentialFormattingRedactsUserKey(t *testing.T) {
	credential := UserCredential{UserID: "usr-owner", UserKey: []byte("owner-key-secret")}
	for _, rendered := range []string{fmt.Sprint(credential), fmt.Sprintf("%#v", credential)} {
		if strings.Contains(rendered, "owner-key-secret") || !strings.Contains(rendered, "<redacted>") {
			t.Fatalf("credential rendered unsafely: %s", rendered)
		}
	}
}

func TestMetadataClientRejectsUnknownResponseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"code":0,"data":{"user_id":"usr-admin","user_key":"admin-key","unexpected":true}}`))
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Token: "gateway-secret", ServiceID: "default"})
	_, err := NewMetadataClient(client).InitAdmin(context.Background(), InitAdminRequest{Username: "admin"})
	if err == nil {
		t.Fatal("InitAdmin() error = nil")
	}
}

func TestMetadataErrorsDoNotRevealCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(writer).Encode(map[string]any{"code": 401, "message": "gateway-secret admin-key owner-key"})
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Token: "gateway-secret", ServiceID: "default"})
	_, err := NewMetadataClient(client).CreateUser(context.Background(), []byte("admin-key"), CreateUserRequest{Username: "owner-key"})
	if err == nil {
		t.Fatal("CreateUser() error = nil")
	}
	for _, secret := range []string{"gateway-secret", "admin-key", "owner-key"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

func TestMetadataRejectsMultilineUserKeyBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Token: "gateway-secret", ServiceID: "default"})
	_, err := NewMetadataClient(client).CreateUser(context.Background(), []byte("admin\nkey"), CreateUserRequest{Username: "owner"})
	if err == nil || requests != 0 {
		t.Fatalf("error/requests = %v/%d", err, requests)
	}
}
