package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"mlink/internal/config"
	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/host"
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
	"mlink/internal/secret"
)

var ErrConnectionUnavailable = errors.New("connection runtime is unavailable")

type StartProvider func(context.Context, host.Config) (host.Session, error)

type RuntimeConfig struct {
	Secrets           secret.Store
	Manifest          manifest.Manifest
	PackageDirectory  string
	RuntimeDirectory  string
	ParentEnvironment []string
	StartProvider     StartProvider
}

type Runtime struct {
	mu        sync.RWMutex
	config    RuntimeConfig
	sessions  map[string]host.Session
	snapshots map[string]connection.ConnectionSnapshot
	closed    bool
}

func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.StartProvider == nil {
		cfg.StartProvider = host.Start
	}
	return &Runtime{config: cfg, sessions: make(map[string]host.Session), snapshots: make(map[string]connection.ConnectionSnapshot)}
}

func (r *Runtime) Start(ctx context.Context, connectionConfig config.Connection) (host.Session, error) {
	if r == nil {
		return nil, ErrConnectionUnavailable
	}
	if connectionConfig.ProviderID != r.config.Manifest.ProviderID || connectionConfig.ProviderVersion != r.config.Manifest.Version {
		return nil, errors.New("connection and bundled Provider identities differ")
	}
	providerConfig, err := json.Marshal(connectionConfig.ProviderConfig)
	if err != nil {
		return nil, fmt.Errorf("encode Provider configuration: %w", err)
	}
	snapshot, err := connection.NewSnapshot(
		connectionConfig.ID,
		connection.ProviderRef{ID: connectionConfig.ProviderID, Version: connectionConfig.ProviderVersion},
		connectionConfig.ConfigRevision,
		providerConfig,
		connectionConfig.SecretRefs,
	)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startSnapshotLocked(ctx, snapshot)
}

// Recovery uses the original immutable route/config, resolving secret references afresh.
func (r *Runtime) startSnapshotLocked(ctx context.Context, snapshot connection.ConnectionSnapshot) (host.Session, error) {
	if r.closed {
		return nil, ErrConnectionUnavailable
	}
	key := routeKey(snapshot.RouteKey())
	if session := r.sessions[key]; session != nil && session.State() == host.StateReady {
		return session, nil
	}
	secrets, err := r.resolveSecrets(ctx, snapshot.SecretRefs())
	if err != nil {
		return nil, err
	}
	session, err := r.config.StartProvider(ctx, host.Config{
		Snapshot:          snapshot,
		Manifest:          r.config.Manifest,
		PackageDirectory:  r.config.PackageDirectory,
		RuntimeDirectory:  r.config.RuntimeDirectory,
		Secrets:           secrets,
		ParentEnvironment: append([]string(nil), r.config.ParentEnvironment...),
	})
	if err != nil {
		return nil, err
	}
	if session.RouteKey() != snapshot.RouteKey() {
		_ = session.Shutdown(context.Background())
		return nil, errors.New("Provider Session returned a different route")
	}
	r.sessions[key] = session
	r.snapshots[key] = snapshot
	return session, nil
}

func (r *Runtime) sessionForCall(ctx context.Context, route connection.RouteKey) (host.Session, error) {
	if r == nil {
		return nil, ErrConnectionUnavailable
	}
	if session, ok := r.Session(route); ok {
		return session, nil
	}
	// Concurrent callers share one replacement; no business request is replayed here.
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, ok := r.snapshots[routeKey(route)]
	if !ok || r.closed {
		return nil, ErrConnectionUnavailable
	}
	session, err := r.startSnapshotLocked(ctx, snapshot)
	if err != nil {
		// No business request was sent. The journal's existing backoff is safe.
		return nil, &host.CallError{Code: protocol.ErrorTemporarilyUnavailable, Delivery: host.DeliveryNotSent, Message: "provider restart failed"}
	}
	return session, nil
}

func (r *Runtime) Session(route connection.RouteKey) (host.Session, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.sessions[routeKey(route)]
	return session, ok && session.State() == host.StateReady
}

func (r *Runtime) CaptureTurn(ctx context.Context, route connection.RouteKey, meta host.CallMeta, turn model.Turn) (model.WriteReceipt, error) {
	session, err := r.sessionForCall(ctx, route)
	if err != nil {
		return model.WriteReceipt{}, err
	}
	return session.CaptureTurn(ctx, meta, turn)
}

func (r *Runtime) Recall(ctx context.Context, route connection.RouteKey, meta host.CallMeta, request model.RecallRequest) (model.ContextBundle, error) {
	session, err := r.sessionForCall(ctx, route)
	if err != nil {
		return model.ContextBundle{}, err
	}
	return session.Recall(ctx, meta, request)
}

func (r *Runtime) ArchiveSession(ctx context.Context, route connection.RouteKey, meta host.CallMeta, identity model.IdentityScope) error {
	session, err := r.sessionForCall(ctx, route)
	if errors.Is(err, ErrConnectionUnavailable) {
		return &host.CallError{
			Code: protocol.ErrorTemporarilyUnavailable, IdempotencyKey: meta.IdempotencyKey,
			Delivery: host.DeliveryNotSent, ReplaySafe: true, Message: "provider session is unavailable",
		}
	}
	if err != nil {
		return err
	}
	if _, supported := session.Capabilities()["archive_session"]; !supported {
		return host.ErrCapabilityUnavailable
	}
	archiver, ok := session.(host.SessionArchiver)
	if !ok {
		return host.ErrCapabilityUnavailable
	}
	return archiver.ArchiveSession(ctx, meta, identity)
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	sessions := make([]host.Session, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.sessions = make(map[string]host.Session)
	r.snapshots = make(map[string]connection.ConnectionSnapshot)
	r.closed = true
	r.mu.Unlock()
	var shutdownErrors []error
	for _, session := range sessions {
		if err := session.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	return errors.Join(shutdownErrors...)
}

func (r *Runtime) resolveSecrets(ctx context.Context, refs map[string]string) (map[string]string, error) {
	if len(refs) == 0 {
		return map[string]string{}, nil
	}
	if r.config.Secrets == nil {
		return nil, errors.New("secret store is unavailable")
	}
	resolved := make(map[string]string, len(refs))
	for name, ref := range refs {
		const prefix = "keychain://dev.mlink/"
		if !strings.HasPrefix(ref, prefix) || len(ref) == len(prefix) {
			return nil, fmt.Errorf("unsupported secret reference for %q", name)
		}
		value, err := r.config.Secrets.Get(ctx, strings.TrimPrefix(ref, prefix))
		if err != nil {
			return nil, fmt.Errorf("resolve secret %q: %w", name, err)
		}
		resolved[name] = string(value)
		for i := range value {
			value[i] = 0
		}
	}
	return resolved, nil
}

func routeKey(route connection.RouteKey) string {
	return strings.Join([]string{route.ConnectionID, route.ProviderID, route.ProviderVersion, route.ConfigRevision}, "\x00")
}
