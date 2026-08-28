package broker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Server struct {
	Service      Service
	Authorizer   Authorizer
	MaxBodyBytes int64
}

func (s Server) ServeHTTP(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("HTTP listener is required")
	}
	return serve(ctx, listener, s.Handler(false))
}

func (s Server) ServeUnix(ctx context.Context, socketPath string) error {
	if socketPath == "" {
		return errors.New("Unix socket path is required")
	}
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&fs.ModeSocket == 0 {
			return errors.New("refusing to replace a non-socket path")
		}
		if err := os.Remove(socketPath); err != nil {
			return fmt.Errorf("remove stale Unix socket: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect Unix socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on Unix socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("protect Unix socket: %w", err)
	}
	return serve(ctx, listener, s.Handler(true))
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err := server.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
