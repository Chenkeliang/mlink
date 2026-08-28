package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"mlink/internal/journal"
	"mlink/internal/model"
)

const defaultMaxBodyBytes = 1 << 20

type RecallInput struct {
	AdapterID     string `json:"adapter_id"`
	Source        string `json:"source,omitempty"`
	SourceSubject string `json:"source_subject,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	Query         string `json:"query"`
	MaxItems      int    `json:"max_items,omitempty"`
}

type TurnInput struct {
	AdapterID     string          `json:"adapter_id"`
	Source        string          `json:"source,omitempty"`
	SourceSubject string          `json:"source_subject,omitempty"`
	UserID        string          `json:"user_id,omitempty"`
	SessionID     string          `json:"session_id"`
	TurnID        string          `json:"turn_id"`
	Messages      []model.Message `json:"messages"`
}

type FragmentInput struct {
	AdapterID     string    `json:"adapter_id"`
	Source        string    `json:"source,omitempty"`
	SourceSubject string    `json:"source_subject,omitempty"`
	UserID        string    `json:"user_id,omitempty"`
	SessionID     string    `json:"session_id"`
	TurnID        string    `json:"turn_id"`
	Role          string    `json:"role"`
	Content       string    `json:"content"`
	OccurredAt    time.Time `json:"occurred_at"`
}

type FlushInput struct {
	AdapterID     string `json:"adapter_id"`
	Source        string `json:"source,omitempty"`
	SourceSubject string `json:"source_subject,omitempty"`
	UserID        string `json:"user_id,omitempty"`
}

func (s Server) Handler(local bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth(local))
	mux.HandleFunc("POST /v1/recall", s.handleRecall(local))
	mux.HandleFunc("POST /v1/turn-fragments", s.handleFragment(local))
	mux.HandleFunc("POST /v1/turns", s.handleTurn(local))
	mux.HandleFunc("POST /v1/sessions/{session_id}/flush", s.handleFlush(local))
	return mux
}

func (s Server) handleHealth(local bool) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		adapterID := request.URL.Query().Get("adapter_id")
		if _, err := s.authorizeGrant(request, adapterID, local); err != nil {
			s.writeAuthorizationError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"status": "ready"})
	}
}

func (s Server) handleRecall(local bool) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input RecallInput
		if err := s.decodeJSON(response, request, &input); err != nil {
			return
		}
		grant, userID, err := s.authorizeIdentity(request, input.AdapterID, input.Source, input.SourceSubject, input.UserID, local)
		if err != nil {
			s.writeAuthorizationError(response, err)
			return
		}
		identityScope := model.IdentityScope{
			ConnectionID: grant.Route.ConnectionID,
			TenantID:     grant.TenantID,
			AgentID:      grant.AgentID,
			UserID:       userID,
		}
		bundle, err := s.Service.Recall(request.Context(), grant.Route, recallKey(input, userID), model.RecallRequest{
			Identity:           identityScope,
			Query:              input.Query,
			MaxItems:           input.MaxItems,
			IncludeAgentShared: grant.IncludeAgentShared,
		})
		if err != nil {
			writeAPIError(response, http.StatusServiceUnavailable, "provider_unavailable")
			return
		}
		writeJSON(response, http.StatusOK, bundle)
	}
}

func (s Server) handleTurn(local bool) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input TurnInput
		if err := s.decodeJSON(response, request, &input); err != nil {
			return
		}
		grant, userID, err := s.authorizeIdentity(request, input.AdapterID, input.Source, input.SourceSubject, input.UserID, local)
		if err != nil {
			s.writeAuthorizationError(response, err)
			return
		}
		receipt, err := s.Service.SubmitTurn(request.Context(), journal.Envelope{
			AdapterID: input.AdapterID,
			Route:     grant.Route,
			Turn: model.Turn{
				Identity: model.IdentityScope{
					ConnectionID: grant.Route.ConnectionID,
					TenantID:     grant.TenantID,
					AgentID:      grant.AgentID,
					UserID:       userID,
					SessionID:    input.SessionID,
					TurnID:       input.TurnID,
				},
				Messages: input.Messages,
			},
		})
		if err != nil {
			if errors.Is(err, journal.ErrTurnConflict) {
				writeAPIError(response, http.StatusConflict, "turn_conflict")
				return
			}
			writeAPIError(response, http.StatusServiceUnavailable, "journal_unavailable")
			return
		}
		writeJSON(response, http.StatusAccepted, receipt)
	}
}

func (s Server) handleFragment(local bool) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input FragmentInput
		if err := s.decodeJSON(response, request, &input); err != nil {
			return
		}
		grant, userID, err := s.authorizeIdentity(request, input.AdapterID, input.Source, input.SourceSubject, input.UserID, local)
		if err != nil {
			s.writeAuthorizationError(response, err)
			return
		}
		err = s.Service.SubmitFragment(request.Context(), journal.Fragment{
			AdapterID: input.AdapterID,
			Route:     grant.Route,
			Identity: model.IdentityScope{
				ConnectionID: grant.Route.ConnectionID,
				TenantID:     grant.TenantID,
				AgentID:      grant.AgentID,
				UserID:       userID,
				SessionID:    input.SessionID,
				TurnID:       input.TurnID,
			},
			Role:       input.Role,
			Content:    input.Content,
			OccurredAt: input.OccurredAt,
		})
		if err != nil {
			if errors.Is(err, journal.ErrTurnConflict) {
				writeAPIError(response, http.StatusConflict, "turn_conflict")
				return
			}
			writeAPIError(response, http.StatusServiceUnavailable, "journal_unavailable")
			return
		}
		writeJSON(response, http.StatusAccepted, map[string]any{"queued": true})
	}
}

func (s Server) handleFlush(local bool) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input FlushInput
		if err := s.decodeJSON(response, request, &input); err != nil {
			return
		}
		if _, _, err := s.authorizeIdentity(request, input.AdapterID, input.Source, input.SourceSubject, input.UserID, local); err != nil {
			s.writeAuthorizationError(response, err)
			return
		}
		pending, err := s.Service.Flush(request.Context(), input.AdapterID, request.PathValue("session_id"))
		if err != nil {
			writeAPIError(response, http.StatusServiceUnavailable, "journal_unavailable")
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"pending": pending})
	}
}

func (s Server) authorizeIdentity(request *http.Request, adapterID, source, subject, directUserID string, local bool) (Grant, string, error) {
	grant, err := s.authorizeGrant(request, adapterID, local)
	if err != nil {
		return Grant{}, "", err
	}
	userID, err := s.Authorizer.CanonicalUser(grant, source, subject, directUserID)
	return grant, userID, err
}

func (s Server) authorizeGrant(request *http.Request, adapterID string, local bool) (Grant, error) {
	token := ""
	if !local {
		const prefix = "Bearer "
		header := request.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) || len(header) == len(prefix) {
			return Grant{}, ErrUnauthorized
		}
		token = strings.TrimPrefix(header, prefix)
	}
	return s.Authorizer.GrantFor(token, adapterID, local)
}

func (s Server) decodeJSON(response http.ResponseWriter, request *http.Request, target any) error {
	limit := s.MaxBodyBytes
	if limit <= 0 {
		limit = defaultMaxBodyBytes
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAPIError(response, http.StatusRequestEntityTooLarge, "body_too_large")
		} else {
			writeAPIError(response, http.StatusBadRequest, "invalid_request")
		}
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAPIError(response, http.StatusBadRequest, "invalid_request")
		return errors.New("multiple JSON values")
	}
	return nil
}

func (s Server) writeAuthorizationError(response http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnauthorized) {
		writeAPIError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeAPIError(response, http.StatusForbidden, "identity_forbidden")
}

func recallKey(input RecallInput, userID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{input.AdapterID, userID, input.Query}, "\x00")))
	return "recall_" + hex.EncodeToString(sum[:13])
}

func writeAPIError(response http.ResponseWriter, status int, code string) {
	writeJSON(response, status, map[string]any{"error": map[string]string{"code": code}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
