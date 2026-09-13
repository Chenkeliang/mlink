package tencentdb

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"mlink/internal/model"
)

const (
	defaultSkillIdleArchiveAfter = 10 * time.Minute
	maximumSkillRecallItems      = 2
	skillArchiveTimeout          = 30 * time.Second
	skillIdleArchiveReason       = "MLink archived the remaining conversation after session inactivity"
	skillShutdownArchiveReason   = "MLink archived the remaining conversation before Provider shutdown"
	skillSessionEndArchiveReason = "MLink archived the conversation after the agent session ended"
)

type skillLifecycle struct {
	client    *Client
	idleAfter time.Duration

	mu       sync.Mutex
	closed   bool
	sessions map[string]model.IdentityScope
	timers   map[string]*time.Timer
	versions map[string]uint64
}

func newSkillLifecycle(client *Client, idleAfter time.Duration) *skillLifecycle {
	if idleAfter <= 0 {
		idleAfter = defaultSkillIdleArchiveAfter
	}
	return &skillLifecycle{
		client:    client,
		idleAfter: idleAfter,
		sessions:  make(map[string]model.IdentityScope),
		timers:    make(map[string]*time.Timer),
		versions:  make(map[string]uint64),
	}
}

func (p *Provider) enableSkills(idleAfter time.Duration) {
	if p == nil || p.client == nil {
		return
	}
	p.skills = newSkillLifecycle(p.client, idleAfter)
}

func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.skills == nil {
		return nil
	}
	return p.skills.shutdown(ctx)
}

func (p *Provider) ArchiveSession(ctx context.Context, identity model.IdentityScope) error {
	if p == nil || p.skills == nil {
		return errors.New("MemoryCore Skill lifecycle is unavailable")
	}
	if err := identity.ValidateForRecall(); err != nil {
		return err
	}
	if strings.TrimSpace(identity.SessionID) == "" {
		return errors.New("Skill archive requires session_id")
	}
	return p.skills.archiveSession(ctx, identity)
}

func (s *skillLifecycle) captureTurn(ctx context.Context, identity model.IdentityScope, messages []memoryCoreMessage) error {
	request := struct {
		TeamID    string              `json:"team_id"`
		AgentID   string              `json:"agent_id"`
		UserID    string              `json:"user_id"`
		SessionID string              `json:"session_id"`
		Messages  []memoryCoreMessage `json:"messages"`
	}{
		TeamID:    identity.TenantID,
		AgentID:   identity.AgentID,
		UserID:    identity.UserID,
		SessionID: identity.SessionID,
		Messages:  messages,
	}
	var response struct {
		Status string `json:"status"`
	}
	if err := s.client.post(ctx, "/v3/skill/conversation/add", request, &response); err != nil {
		return err
	}
	if response.Status == "archived" {
		s.forget(identity)
		return nil
	}
	s.schedule(identity)
	return nil
}

func (s *skillLifecycle) recall(ctx context.Context, request model.RecallRequest, limit int) ([]model.ContextItem, error) {
	if limit <= 0 {
		return nil, nil
	}
	searchBody := map[string]any{
		"team_id":  request.Identity.TenantID,
		"agent_id": request.Identity.AgentID,
		"user_id":  request.Identity.UserID,
		"query":    compactUTF16(request.Query, officialSearchQueryUnits),
		"top_k":    limit,
	}
	var searched struct {
		Items []struct {
			SkillID     string  `json:"skill_id"`
			Name        string  `json:"name"`
			Version     int     `json:"version"`
			CreatedAtMS int64   `json:"created_at_ms"`
			UpdatedAtMS int64   `json:"updated_at_ms"`
			Score       float64 `json:"score"`
		} `json:"items"`
	}
	if err := s.client.post(ctx, "/v3/skill/search", searchBody, &searched); err != nil {
		return nil, err
	}
	items := make([]model.ContextItem, 0, min(limit, len(searched.Items)))
	for _, hit := range searched.Items {
		if len(items) >= limit || strings.TrimSpace(hit.SkillID) == "" {
			break
		}
		getBody := map[string]any{
			"team_id":          request.Identity.TenantID,
			"agent_id":         request.Identity.AgentID,
			"user_id":          request.Identity.UserID,
			"skill_id":         hit.SkillID,
			"version":          hit.Version,
			"include_content":  true,
			"include_manifest": false,
		}
		var detail struct {
			Content string `json:"content"`
		}
		if err := s.client.post(ctx, "/v3/skill/get", getBody, &detail); err != nil {
			return nil, err
		}
		if strings.TrimSpace(detail.Content) == "" {
			continue
		}
		score := hit.Score
		items = append(items, model.ContextItem{
			ID:        "skill:" + hit.SkillID + "@v" + strconv.Itoa(hit.Version),
			Kind:      "skill",
			Scope:     model.ScopeAgent,
			Text:      detail.Content,
			CreatedAt: timeFromMilliseconds(hit.CreatedAtMS),
			UpdatedAt: timeFromMilliseconds(hit.UpdatedAtMS),
			Score:     &score,
			Source:    "tencentdb:skill/" + hit.SkillID,
		})
	}
	return items, nil
}

func (s *skillLifecycle) schedule(identity model.IdentityScope) {
	key := skillSessionKey(identity)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if timer := s.timers[key]; timer != nil {
		timer.Stop()
	}
	s.sessions[key] = identity
	s.scheduleLocked(key, identity)
}

func (s *skillLifecycle) scheduleLocked(key string, identity model.IdentityScope) {
	s.versions[key]++
	version := s.versions[key]
	s.timers[key] = time.AfterFunc(s.idleAfter, func() { s.archiveOnTimer(key, identity, version) })
}

func (s *skillLifecycle) archiveOnTimer(key string, identity model.IdentityScope, version uint64) {
	s.mu.Lock()
	if s.closed || s.versions[key] != version {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), skillArchiveTimeout)
	err := s.forceArchive(ctx, identity, skillIdleArchiveReason)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.versions[key] != version {
		return
	}
	if err == nil {
		delete(s.sessions, key)
		delete(s.timers, key)
		delete(s.versions, key)
		return
	}
	if !s.closed {
		s.scheduleLocked(key, identity)
	}
}

func (s *skillLifecycle) forget(identity model.IdentityScope) {
	key := skillSessionKey(identity)
	s.mu.Lock()
	defer s.mu.Unlock()
	if timer := s.timers[key]; timer != nil {
		timer.Stop()
	}
	delete(s.timers, key)
	delete(s.sessions, key)
	delete(s.versions, key)
}

func (s *skillLifecycle) archiveSession(ctx context.Context, identity model.IdentityScope) error {
	key := skillSessionKey(identity)
	s.mu.Lock()
	version := s.versions[key]
	s.mu.Unlock()
	if err := s.forceArchive(ctx, identity, skillSessionEndArchiveReason); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.versions[key] != version {
		return nil
	}
	if timer := s.timers[key]; timer != nil {
		timer.Stop()
	}
	delete(s.timers, key)
	delete(s.sessions, key)
	delete(s.versions, key)
	return nil
}

func (s *skillLifecycle) forceArchive(ctx context.Context, identity model.IdentityScope, reason string) error {
	request := map[string]any{
		"team_id":    identity.TenantID,
		"agent_id":   identity.AgentID,
		"user_id":    identity.UserID,
		"session_id": identity.SessionID,
		"reason":     reason,
	}
	var response struct {
		Status string `json:"status"`
	}
	return s.client.post(ctx, "/v3/skill/conversation/force-archive", request, &response)
}

func (s *skillLifecycle) shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sessions := make([]model.IdentityScope, 0, len(s.sessions))
	for key, identity := range s.sessions {
		sessions = append(sessions, identity)
		if timer := s.timers[key]; timer != nil {
			timer.Stop()
		}
	}
	s.timers = make(map[string]*time.Timer)
	s.sessions = make(map[string]model.IdentityScope)
	s.versions = make(map[string]uint64)
	s.mu.Unlock()

	var archiveErrors []error
	for _, identity := range sessions {
		if err := s.forceArchive(ctx, identity, skillShutdownArchiveReason); err != nil {
			archiveErrors = append(archiveErrors, err)
		}
	}
	return errors.Join(archiveErrors...)
}

func skillSessionKey(identity model.IdentityScope) string {
	return strings.Join([]string{identity.TenantID, identity.UserID, identity.AgentID, identity.SessionID}, "\x00")
}

func timeFromMilliseconds(value int64) *time.Time {
	if value <= 0 {
		return nil
	}
	parsed := time.UnixMilli(value).UTC()
	return &parsed
}
