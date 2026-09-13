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
	mu       sync.RWMutex
	config   RuntimeConfig
	sessions map[string]host.Session
}

func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.StartProvider == nil {
		cfg.StartProvider = host.Start
	}
	return &Runtime{config: cfg, sessions: make(map[string]host.Session)}
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
	key := routeKey(snapshot.RouteKey())
	r.mu.Lock()
	defer r.mu.Unlock()
	if session := r.sessions[key]; session != nil && session.State() == host.StateReady {
		return session, nil
	}
	secrets, err := r.resolveSecrets(ctx, connectionConfig.SecretRefs)
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
	session, ok := r.Session(route)
	if !ok {
		return model.WriteReceipt{}, ErrConnectionUnavailable
	}
	return session.CaptureTurn(ctx, meta, turn)
}

func (r *Runtime) Recall(ctx context.Context, route connection.RouteKey, meta host.CallMeta, request model.RecallRequest) (model.ContextBundle, error) {
	session, ok := r.Session(route)
	if !ok {
		return model.ContextBundle{}, ErrConnectionUnavailable
	}
	return session.Recall(ctx, meta, request)
}

func (r *Runtime) ArchiveSession(ctx context.Context, route connection.RouteKey, meta host.CallMeta, identity model.IdentityScope) error {
	session, ok := r.Session(route)
	if !ok {
		return &host.CallError{
			Code: protocol.ErrorTemporarilyUnavailable, IdempotencyKey: meta.IdempotencyKey,
			Delivery: host.DeliveryNotSent, ReplaySafe: true, Message: "provider session is unavailable",
		}
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
