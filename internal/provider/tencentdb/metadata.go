package tencentdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type MetadataClient interface {
	InitAdmin(context.Context, InitAdminRequest) (UserCredential, error)
	CreateUser(context.Context, []byte, CreateUserRequest) (UserCredential, error)
	VerifyUser(context.Context, []byte) (User, error)
	CreateTeam(context.Context, []byte, CreateTeamRequest) (Team, error)
	ListTeams(context.Context, []byte, ListTeamsRequest) ([]Team, error)
	CreateAgent(context.Context, []byte, CreateAgentRequest) (Agent, error)
	ListAgents(context.Context, []byte, ListAgentsRequest) ([]Agent, error)
	GetAsset(context.Context, []byte, string) (Asset, error)
	InstanceQuota(context.Context, []byte) (InstanceQuota, error)
}

type metadataClient struct {
	client *Client
}

func NewMetadataClient(client *Client) MetadataClient {
	return &metadataClient{client: client}
}

type InitAdminRequest struct {
	Username string `json:"username"`
}

type CreateUserRequest struct {
	Username string `json:"username"`
}

type UserCredential struct {
	UserID  string
	UserKey []byte
}

type User struct {
	UserID    string `json:"user_id"`
	UserType  string `json:"user_type"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}

func (credential UserCredential) String() string {
	return fmt.Sprintf("UserCredential{UserID:%q UserKey:<redacted>}", credential.UserID)
}

func (credential UserCredential) GoString() string { return credential.String() }

func (credential *UserCredential) Wipe() {
	if credential == nil {
		return
	}
	wipeMetadataBytes(credential.UserKey)
	credential.UserKey = nil
}

type CreateTeamRequest struct {
	Name         string `json:"name"`
	OwnerUserID  string `json:"owner_user_id"`
	Description  string `json:"description,omitempty"`
	MetadataJSON string `json:"metadata_json,omitempty"`
}

type Team struct {
	TeamID       string  `json:"team_id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	OwnerUserID  string  `json:"owner_user_id"`
	Status       string  `json:"status"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	MetadataJSON string  `json:"metadata_json"`
}

type ListTeamsRequest struct {
	UserID string `json:"user_id"`
	Name   string `json:"name,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type CreateAgentRequest struct {
	TeamID       string `json:"team_id"`
	OwnerUserID  string `json:"owner_user_id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	Visibility   string `json:"visibility,omitempty"`
	MetadataJSON string `json:"metadata_json,omitempty"`
}

type Agent struct {
	AgentID      string  `json:"agent_id"`
	TeamID       string  `json:"team_id"`
	OwnerUserID  string  `json:"owner_user_id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	Prompt       *string `json:"prompt"`
	Visibility   string  `json:"visibility"`
	Status       string  `json:"status"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	MetadataJSON string  `json:"metadata_json"`
}

type ListAgentsRequest struct {
	TeamID      string `json:"team_id,omitempty"`
	OwnerUserID string `json:"owner_user_id,omitempty"`
	Status      string `json:"status,omitempty"`
	Name        string `json:"name,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Offset      int    `json:"offset,omitempty"`
}

type Asset struct {
	AssetID      string   `json:"asset_id"`
	TeamID       string   `json:"team_id"`
	AssetType    string   `json:"asset_type"`
	Name         string   `json:"name"`
	OwnerUserID  string   `json:"owner_user_id"`
	SourceType   string   `json:"source_type"`
	Description  *string  `json:"description"`
	SourceRef    *string  `json:"source_ref"`
	Visibility   string   `json:"visibility"`
	Status       string   `json:"status"`
	Confidence   *float64 `json:"confidence"`
	ExpiresAt    *string  `json:"expires_at"`
	ContentRef   *string  `json:"content_ref"`
	MetadataJSON string   `json:"metadata_json"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	Version      int      `json:"version"`
}

type InstanceQuota struct {
	MaxUsers int `json:"max_users_per_instance"`
	MaxTeams int `json:"max_teams_per_instance"`
}

func (metadata *metadataClient) InitAdmin(ctx context.Context, request InitAdminRequest) (UserCredential, error) {
	if metadata == nil || metadata.client == nil || strings.TrimSpace(request.Username) == "" {
		return UserCredential{}, errors.New("complete TencentDB admin request is required")
	}
	var response struct {
		UserID  string `json:"user_id"`
		UserKey string `json:"user_key"`
	}
	if err := metadata.post(ctx, "/v3/internal/meta/user/init-admin", nil, request, &response); err != nil {
		return UserCredential{}, err
	}
	return credential(response.UserID, response.UserKey)
}

func (metadata *metadataClient) CreateUser(ctx context.Context, adminKey []byte, request CreateUserRequest) (UserCredential, error) {
	if metadata == nil || metadata.client == nil || strings.TrimSpace(request.Username) == "" {
		return UserCredential{}, errors.New("complete TencentDB user request is required")
	}
	var response struct {
		UserID         string `json:"user_id"`
		UserType       string `json:"user_type"`
		CreatedAt      string `json:"created_at"`
		DefaultUserKey string `json:"default_user_key"`
	}
	if err := metadata.post(ctx, "/v3/meta/user/create", adminKey, request, &response); err != nil {
		return UserCredential{}, err
	}
	return credential(response.UserID, response.DefaultUserKey)
}

func (metadata *metadataClient) VerifyUser(ctx context.Context, userKey []byte) (User, error) {
	var response struct {
		Valid bool  `json:"valid"`
		User  *User `json:"user"`
	}
	if err := metadata.post(ctx, "/v3/meta/auth/verify", userKey, map[string]string{"user_key": string(userKey)}, &response); err != nil {
		return User{}, err
	}
	if !response.Valid || response.User == nil || strings.TrimSpace(response.User.UserID) == "" {
		return User{}, errors.New("TencentDB metadata user key is invalid")
	}
	return *response.User, nil
}

func (metadata *metadataClient) CreateTeam(ctx context.Context, ownerKey []byte, request CreateTeamRequest) (Team, error) {
	if strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.OwnerUserID) == "" {
		return Team{}, errors.New("complete TencentDB team request is required")
	}
	var response Team
	if err := metadata.post(ctx, "/v3/meta/team/create", ownerKey, request, &response); err != nil {
		return Team{}, err
	}
	return response, nil
}

func (metadata *metadataClient) ListTeams(ctx context.Context, ownerKey []byte, request ListTeamsRequest) ([]Team, error) {
	if strings.TrimSpace(request.UserID) == "" || request.Limit < 0 || request.Limit > 100 || request.Offset < 0 {
		return nil, errors.New("valid TencentDB Team list request is required")
	}
	var response struct {
		Items  []Team `json:"items"`
		Total  int    `json:"total"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if err := metadata.post(ctx, "/v3/meta/team/list", ownerKey, request, &response); err != nil {
		return nil, err
	}
	return response.Items, nil
}

func (metadata *metadataClient) CreateAgent(ctx context.Context, ownerKey []byte, request CreateAgentRequest) (Agent, error) {
	if strings.TrimSpace(request.TeamID) == "" || strings.TrimSpace(request.OwnerUserID) == "" || strings.TrimSpace(request.Name) == "" {
		return Agent{}, errors.New("complete TencentDB Agent request is required")
	}
	var response Agent
	if err := metadata.post(ctx, "/v3/meta/agent/create", ownerKey, request, &response); err != nil {
		return Agent{}, err
	}
	return response, nil
}

func (metadata *metadataClient) ListAgents(ctx context.Context, ownerKey []byte, request ListAgentsRequest) ([]Agent, error) {
	if request.TeamID == "" && request.OwnerUserID == "" || request.Limit < 0 || request.Limit > 100 || request.Offset < 0 {
		return nil, errors.New("valid TencentDB Agent list request is required")
	}
	var response struct {
		Items  []Agent `json:"items"`
		Total  int     `json:"total"`
		Limit  int     `json:"limit"`
		Offset int     `json:"offset"`
	}
	if err := metadata.post(ctx, "/v3/meta/agent/list", ownerKey, request, &response); err != nil {
		return nil, err
	}
	return response.Items, nil
}

func (metadata *metadataClient) GetAsset(ctx context.Context, ownerKey []byte, assetID string) (Asset, error) {
	if strings.TrimSpace(assetID) == "" {
		return Asset{}, errors.New("TencentDB Asset ID is required")
	}
	var response Asset
	if err := metadata.post(ctx, "/v3/meta/asset/get", ownerKey, map[string]string{"asset_id": assetID}, &response); err != nil {
		return Asset{}, err
	}
	return response, nil
}

func (metadata *metadataClient) InstanceQuota(ctx context.Context, ownerKey []byte) (InstanceQuota, error) {
	var response InstanceQuota
	if err := metadata.post(ctx, "/v3/meta/instance-quota/get", ownerKey, map[string]any{}, &response); err != nil {
		return InstanceQuota{}, err
	}
	return response, nil
}

func (metadata *metadataClient) post(ctx context.Context, path string, userKey []byte, request, response any) error {
	if metadata == nil || metadata.client == nil {
		return errors.New("TencentDB metadata client is unavailable")
	}
	var raw json.RawMessage
	var err error
	if len(userKey) == 0 {
		err = metadata.client.post(ctx, path, request, &raw)
	} else {
		err = metadata.client.postWithUserKey(ctx, path, userKey, request, &raw)
	}
	if err != nil {
		return err
	}
	return decodeMetadata(raw, response)
}

func credential(userID, userKey string) (UserCredential, error) {
	if strings.TrimSpace(userID) == "" || userKey == "" {
		return UserCredential{}, errors.New("TencentDB returned an incomplete user credential")
	}
	return UserCredential{UserID: userID, UserKey: []byte(userKey)}, nil
}

func decodeMetadata(raw []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return errors.New("TencentDB metadata returned invalid response data")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("TencentDB metadata returned multiple response values")
	}
	return nil
}

func wipeMetadataBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
