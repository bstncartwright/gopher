package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	telegramWebhookSecretHeader = "X-Telegram-Bot-Api-Secret-Token"
	telegramWebhookMaxBodyBytes = 1 << 20
)

type telegramWebhookRuntime interface {
	RunWithRetry(context.Context) error
	Stop() error
}

type telegramWebhookServerOptions struct {
	ListenAddr   string
	Path         string
	Secret       string
	HandleUpdate func(context.Context, []byte) error
}

type telegramWebhookServer struct {
	listenAddr   string
	path         string
	secret       string
	handleUpdate func(context.Context, []byte) error

	runMu   sync.Mutex
	current *http.Server
}

func newTelegramWebhookServer(opts telegramWebhookServerOptions) (*telegramWebhookServer, error) {
	listenAddr := strings.TrimSpace(opts.ListenAddr)
	if listenAddr == "" {
		return nil, fmt.Errorf("telegram webhook listen address is required")
	}
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		return nil, fmt.Errorf("telegram webhook path is required")
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("telegram webhook path must start with /")
	}
	secret := strings.TrimSpace(opts.Secret)
	if secret == "" {
		return nil, fmt.Errorf("telegram webhook secret is required")
	}
	if opts.HandleUpdate == nil {
		return nil, fmt.Errorf("telegram webhook handler is required")
	}
	return &telegramWebhookServer{
		listenAddr:   listenAddr,
		path:         path,
		secret:       secret,
		handleUpdate: opts.HandleUpdate,
	}, nil
}

func (s *telegramWebhookServer) RunWithRetry(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		attempt++
		listener, err := net.Listen("tcp", s.listenAddr)
		if err != nil {
			delay := channelBridgeRetryDelay(attempt)
			slog.Warn("telegram_webhook: listen failed, retrying", "addr", s.listenAddr, "error", err, "retry_in", delay)
			if !waitForTelegramWebhookRetry(ctx, delay) {
				return nil
			}
			continue
		}

		attempt = 0
		httpServer := &http.Server{
			Addr:    s.listenAddr,
			Handler: s.newMux(),
		}
		s.setCurrent(httpServer)
		errCh := make(chan error, 1)
		slog.Info("telegram_webhook: listening", "addr", s.listenAddr, "path", s.path)
		go func() {
			errCh <- httpServer.Serve(listener)
		}()

		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = httpServer.Shutdown(shutdownCtx)
			cancel()
			s.clearCurrent(httpServer)
			err := <-errCh
			if err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		case err := <-errCh:
			s.clearCurrent(httpServer)
			if err == nil || err == http.ErrServerClosed {
				delay := channelBridgeRetryDelay(1)
				if !waitForTelegramWebhookRetry(ctx, delay) {
					return nil
				}
				continue
			}
			delay := channelBridgeRetryDelay(1)
			slog.Warn("telegram_webhook: serve failed, retrying", "addr", s.listenAddr, "error", err, "retry_in", delay)
			if !waitForTelegramWebhookRetry(ctx, delay) {
				return nil
			}
		}
	}
}

func (s *telegramWebhookServer) Stop() error {
	server := s.snapshotCurrent()
	if server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func (s *telegramWebhookServer) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc(s.path, s.handleWebhook)
	return mux
}

func (s *telegramWebhookServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	receivedSecret := strings.TrimSpace(r.Header.Get(telegramWebhookSecretHeader))
	if subtle.ConstantTimeCompare([]byte(receivedSecret), []byte(s.secret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, telegramWebhookMaxBodyBytes+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body) > telegramWebhookMaxBodyBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !json.Valid(body) {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := s.handleUpdate(r.Context(), body); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "decode telegram webhook update") {
			http.Error(w, "invalid telegram update", http.StatusBadRequest)
			return
		}
		slog.Error("telegram_webhook: failed to process update", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *telegramWebhookServer) setCurrent(server *http.Server) {
	s.runMu.Lock()
	s.current = server
	s.runMu.Unlock()
}

func (s *telegramWebhookServer) clearCurrent(server *http.Server) {
	s.runMu.Lock()
	if s.current == server {
		s.current = nil
	}
	s.runMu.Unlock()
}

func (s *telegramWebhookServer) snapshotCurrent() *http.Server {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.current
}

func waitForTelegramWebhookRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
