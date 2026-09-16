package broker

import (
	"context"
	"mlink/internal/model"
	"mlink/internal/provider/host"
	"net/http"
)

func (s Server) handleEndTurn(local bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input TurnInput
		if err := s.decodeJSON(w, r, &input); err != nil {
			return
		}
		a, err := s.authorizeIdentity(r, input.AdapterID, input.externalContext(), input.UserID, input.SessionID, local)
		if err != nil {
			s.writeAuthorizationError(w, err)
			return
		}
		i := model.IdentityScope{ConnectionID: a.Route.ConnectionID, TenantID: a.Identity.TenantID, AgentID: a.Identity.AgentID, UserID: a.Identity.UserID, SessionID: a.Identity.SessionID, TurnID: s.Authorizer.CanonicalTurnID(a.Identity.ActorDigest, input.TurnID)}
		if err := i.ValidateForCapture(); err != nil {
			writeAPIError(w, 400, "invalid_turn")
			return
		}
		if j, ok := s.Service.Journal.(interface {
			EndObservation(context.Context, string, model.IdentityScope) error
		}); ok {
			if err := j.EndObservation(r.Context(), input.AdapterID, i); err != nil {
				writeAPIError(w, 503, "journal_unavailable")
				return
			}
		}
		if p, ok := s.Service.Provider.(userTurnObserver); ok {
			if _, err := p.ObserveUserTurn(r.Context(), a.Route, host.CallMeta{IdempotencyKey: i.TurnID}, model.UserTurn{Identity: i, Ended: true}); err != nil {
				writeAPIError(w, 503, "provider_unavailable")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ended": true})
	}
}
