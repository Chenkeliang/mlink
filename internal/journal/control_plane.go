package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"time"
)

var (
	ErrPrincipalAgentNotFound   = errors.New("principal Agent mapping not found")
	principalFingerprintPattern = regexp.MustCompile(`^prn_[a-z2-7]{26}$`)
	journalIDPattern            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	rawExternalIDPattern        = regexp.MustCompile(`(?i)(?:ou_|oc_|on_|union_id|open_id|chat_id|thread_id)`)
)

type ControlPlaneState struct {
	InstallationID string
	InstanceID     string
	OwnerUserID    string
	OwnerTeamID    string
	OwnerAgentID   string
	OwnerAssetID   string
	PanelContainer string
	PanelImage     string
	State          string
}

type PrincipalAgent struct {
	Fingerprint    string
	RouteKind      string
	BackendUserID  string
	BackendTeamID  string
	BackendAgentID string
	BackendAssetID string
	DisplayLabel   string
	State          string
}

func (s *Store) SaveControlPlane(ctx context.Context, value ControlPlaneState) error {
	if s == nil || s.db == nil || !validControlPlane(value) {
		return errors.New("valid control-plane state is required")
	}
	existing, err := s.controlPlaneByID(ctx, value.InstallationID)
	if err == nil {
		if !sameControlPlaneIdentity(existing, value) {
			return errors.New("control-plane installation identity conflict")
		}
		_, err = s.db.ExecContext(ctx, `
			UPDATE control_plane_installations
			SET panel_container = ?, panel_image = ?, state = ?, updated_at = ?
			WHERE installation_id = ?`,
			value.PanelContainer, value.PanelImage, value.State, formatTime(time.Now().UTC()), value.InstallationID)
		if err != nil {
			return fmt.Errorf("update control-plane state: %w", err)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	now := formatTime(time.Now().UTC())
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO control_plane_installations(
			installation_id, instance_id, owner_user_id, owner_team_id, owner_agent_id, owner_asset_id,
			panel_container, panel_image, state, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.InstallationID, value.InstanceID, value.OwnerUserID, value.OwnerTeamID, value.OwnerAgentID,
		value.OwnerAssetID, value.PanelContainer, value.PanelImage, value.State, now, now)
	if err != nil {
		return fmt.Errorf("save control-plane state: %w", err)
	}
	return nil
}

func (s *Store) LoadControlPlane(ctx context.Context) (ControlPlaneState, error) {
	if s == nil || s.db == nil {
		return ControlPlaneState{}, errors.New("journal store is unavailable")
	}
	var value ControlPlaneState
	err := s.db.QueryRowContext(ctx, `
		SELECT installation_id, instance_id, owner_user_id, owner_team_id, owner_agent_id, owner_asset_id,
		       panel_container, panel_image, state
		FROM control_plane_installations
		ORDER BY updated_at DESC, installation_id DESC LIMIT 1`).Scan(
		&value.InstallationID, &value.InstanceID, &value.OwnerUserID, &value.OwnerTeamID,
		&value.OwnerAgentID, &value.OwnerAssetID, &value.PanelContainer, &value.PanelImage, &value.State)
	if errors.Is(err, sql.ErrNoRows) {
		return ControlPlaneState{}, fs.ErrNotExist
	}
	if err != nil {
		return ControlPlaneState{}, fmt.Errorf("load control-plane state: %w", err)
	}
	return value, nil
}

func (s *Store) MarkControlPlaneState(ctx context.Context, state string) error {
	if !validControlPlaneState(state) {
		return errors.New("invalid control-plane state")
	}
	current, err := s.LoadControlPlane(ctx)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE control_plane_installations SET state = ?, updated_at = ? WHERE installation_id = ?`,
		state, formatTime(time.Now().UTC()), current.InstallationID)
	if err != nil {
		return fmt.Errorf("mark control-plane state: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fs.ErrNotExist
	}
	return nil
}

func (s *Store) PutPrincipalAgent(ctx context.Context, value PrincipalAgent) error {
	if s == nil || s.db == nil || !validPrincipalAgent(value) {
		return errors.New("valid principal Agent mapping is required")
	}
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO principal_agents(
			principal_fingerprint, route_kind, backend_user_id, backend_team_id, backend_agent_id,
			backend_asset_id, display_label, state, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(principal_fingerprint) DO NOTHING`,
		value.Fingerprint, value.RouteKind, value.BackendUserID, value.BackendTeamID,
		value.BackendAgentID, value.BackendAssetID, value.DisplayLabel, value.State, now, now)
	if err != nil {
		return fmt.Errorf("insert principal Agent mapping: %w", err)
	}
	existing, err := s.GetPrincipalAgent(ctx, value.Fingerprint)
	if err != nil {
		return err
	}
	if !samePrincipalAgentIdentity(existing, value) {
		return errors.New("principal Agent mapping conflict")
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE principal_agents SET display_label = ?, state = ?, updated_at = ?
		WHERE principal_fingerprint = ?`, value.DisplayLabel, value.State, now, value.Fingerprint)
	if err != nil {
		return fmt.Errorf("update principal Agent mapping: %w", err)
	}
	return nil
}

func (s *Store) GetPrincipalAgent(ctx context.Context, fingerprint string) (PrincipalAgent, error) {
	if s == nil || s.db == nil {
		return PrincipalAgent{}, errors.New("journal store is unavailable")
	}
	var value PrincipalAgent
	err := s.db.QueryRowContext(ctx, `
		SELECT principal_fingerprint, route_kind, backend_user_id, backend_team_id, backend_agent_id,
		       backend_asset_id, display_label, state
		FROM principal_agents WHERE principal_fingerprint = ?`, fingerprint).Scan(
		&value.Fingerprint, &value.RouteKind, &value.BackendUserID, &value.BackendTeamID,
		&value.BackendAgentID, &value.BackendAssetID, &value.DisplayLabel, &value.State)
	if errors.Is(err, sql.ErrNoRows) {
		return PrincipalAgent{}, ErrPrincipalAgentNotFound
	}
	if err != nil {
		return PrincipalAgent{}, fmt.Errorf("load principal Agent mapping: %w", err)
	}
	return value, nil
}

func (s *Store) ListPrincipalAgents(ctx context.Context) ([]PrincipalAgent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("journal store is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT principal_fingerprint, route_kind, backend_user_id, backend_team_id, backend_agent_id,
		       backend_asset_id, display_label, state
		FROM principal_agents ORDER BY principal_fingerprint`)
	if err != nil {
		return nil, fmt.Errorf("list principal Agent mappings: %w", err)
	}
	defer rows.Close()
	var values []PrincipalAgent
	for rows.Next() {
		var value PrincipalAgent
		if err := rows.Scan(&value.Fingerprint, &value.RouteKind, &value.BackendUserID, &value.BackendTeamID,
			&value.BackendAgentID, &value.BackendAssetID, &value.DisplayLabel, &value.State); err != nil {
			return nil, fmt.Errorf("scan principal Agent mapping: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) controlPlaneByID(ctx context.Context, installationID string) (ControlPlaneState, error) {
	var value ControlPlaneState
	err := s.db.QueryRowContext(ctx, `
		SELECT installation_id, instance_id, owner_user_id, owner_team_id, owner_agent_id, owner_asset_id,
		       panel_container, panel_image, state
		FROM control_plane_installations WHERE installation_id = ?`, installationID).Scan(
		&value.InstallationID, &value.InstanceID, &value.OwnerUserID, &value.OwnerTeamID,
		&value.OwnerAgentID, &value.OwnerAssetID, &value.PanelContainer, &value.PanelImage, &value.State)
	if errors.Is(err, sql.ErrNoRows) {
		return ControlPlaneState{}, fs.ErrNotExist
	}
	if err != nil {
		return ControlPlaneState{}, fmt.Errorf("load control-plane installation: %w", err)
	}
	return value, nil
}

func validControlPlane(value ControlPlaneState) bool {
	for _, item := range []string{value.InstallationID, value.InstanceID, value.OwnerUserID, value.OwnerTeamID,
		value.OwnerAgentID, value.OwnerAssetID, value.PanelContainer, value.PanelImage} {
		if !journalIDPattern.MatchString(item) {
			return false
		}
	}
	return validControlPlaneState(value.State)
}

func validControlPlaneState(state string) bool {
	switch state {
	case "provisioning", "provisioned", "active", "inactive", "failed":
		return true
	default:
		return false
	}
}

func validPrincipalAgent(value PrincipalAgent) bool {
	if !principalFingerprintPattern.MatchString(value.Fingerprint) ||
		value.RouteKind != "hermes-private" && value.RouteKind != "hermes-group" ||
		len(value.DisplayLabel) > 200 || rawExternalIDPattern.MatchString(value.DisplayLabel) {
		return false
	}
	for _, item := range []string{value.BackendUserID, value.BackendTeamID, value.BackendAgentID, value.BackendAssetID} {
		if !journalIDPattern.MatchString(item) {
			return false
		}
	}
	switch value.State {
	case "provisioning", "active", "inactive", "failed":
		return true
	default:
		return false
	}
}

func sameControlPlaneIdentity(left, right ControlPlaneState) bool {
	return left.InstallationID == right.InstallationID && left.InstanceID == right.InstanceID &&
		left.OwnerUserID == right.OwnerUserID && left.OwnerTeamID == right.OwnerTeamID &&
		left.OwnerAgentID == right.OwnerAgentID && left.OwnerAssetID == right.OwnerAssetID
}

func samePrincipalAgentIdentity(left, right PrincipalAgent) bool {
	return left.Fingerprint == right.Fingerprint && left.RouteKind == right.RouteKind &&
		left.BackendUserID == right.BackendUserID && left.BackendTeamID == right.BackendTeamID &&
		left.BackendAgentID == right.BackendAgentID && left.BackendAssetID == right.BackendAssetID
}

func (value PrincipalAgent) String() string {
	suffix := value.Fingerprint
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return fmt.Sprintf("PrincipalAgent{Fingerprint:…%s RouteKind:%q BackendAgentID:%q State:%q}", suffix, value.RouteKind, value.BackendAgentID, value.State)
}

func (value PrincipalAgent) GoString() string { return value.String() }
