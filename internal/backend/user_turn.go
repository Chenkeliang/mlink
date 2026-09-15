package backend

import (
	"context"
	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/host"
	"strings"
)

func (r *Runtime) ObserveUserTurn(ctx context.Context, route connection.RouteKey, meta host.CallMeta, turn model.UserTurn) (model.ObservationReceipt, error) {
	session, err := r.sessionForCall(ctx, route)
	if err != nil {
		return model.ObservationReceipt{}, err
	}
	observer, ok := session.(host.UserTurnObserver)
	if _, supported := session.Capabilities()["observe_user_turn"]; !ok || !supported {
		return model.ObservationReceipt{}, host.ErrCapabilityUnavailable
	}
	if turn.Ended {
		receipt, err := observer.ObserveUserTurn(ctx, meta, turn)
		if err == nil {
			r.finishObserved(route, turn.Identity, false)
		}
		return receipt, err
	}
	r.mu.Lock()
	if r.observed == nil {
		r.observed = make(map[string]map[string]model.UserTurn)
	}
	key := routeKey(route)
	if r.observed[key] == nil {
		r.observed[key] = make(map[string]model.UserTurn)
	}
	replay := turn
	replay.Correction = false
	replay.Previous = nil
	r.observed[key][activityKey(turn.Identity)] = replay
	r.mu.Unlock()
	return observer.ObserveUserTurn(ctx, meta, turn)
}

func activityKey(i model.IdentityScope) string {
	return strings.Join([]string{i.TenantID, i.UserID, i.AgentID, i.SessionID, i.TurnID}, "\x00")
}

func (r *Runtime) finishObserved(route connection.RouteKey, i model.IdentityScope, wholeSession bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, turn := range r.observed[routeKey(route)] {
		if (!wholeSession && key == activityKey(i)) || (wholeSession && turn.Identity.TenantID == i.TenantID && turn.Identity.UserID == i.UserID && turn.Identity.AgentID == i.AgentID && turn.Identity.SessionID == i.SessionID) {
			delete(r.observed[routeKey(route)], key)
		}
	}
}
