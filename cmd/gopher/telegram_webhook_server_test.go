package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramWebhookServerAcceptsValidRequest(t *testing.T) {
	called := false
	server, err := newTelegramWebhookServer(telegramWebhookServerOptions{
		ListenAddr: "127.0.0.1:29330",
		Path:       "/_gopher/telegram/webhook",
		Secret:     "secret",
		HandleUpdate: func(_ context.Context, payload []byte) error {
			called = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newTelegramWebhookServer() error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/_gopher/telegram/webhook", http.NoBody)
	req.Body = io.NopCloser(strings.NewReader(`{"update_id":1}`))
	req.Header.Set(telegramWebhookSecretHeader, "secret")
	rec := httptest.NewRecorder()
	server.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !called {
		t.Fatalf("expected webhook handler to be called")
	}
}

func TestTelegramWebhookServerRejectsBadSecret(t *testing.T) {
	server, err := newTelegramWebhookServer(telegramWebhookServerOptions{
		ListenAddr: "127.0.0.1:29330",
		Path:       "/_gopher/telegram/webhook",
		Secret:     "secret",
		HandleUpdate: func(_ context.Context, payload []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newTelegramWebhookServer() error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/_gopher/telegram/webhook", strings.NewReader(`{"update_id":1}`))
	req.Header.Set(telegramWebhookSecretHeader, "wrong")
	rec := httptest.NewRecorder()
	server.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestTelegramWebhookServerRejectsWrongMethod(t *testing.T) {
	server, err := newTelegramWebhookServer(telegramWebhookServerOptions{
		ListenAddr: "127.0.0.1:29330",
		Path:       "/_gopher/telegram/webhook",
		Secret:     "secret",
		HandleUpdate: func(_ context.Context, payload []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newTelegramWebhookServer() error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/_gopher/telegram/webhook", nil)
	rec := httptest.NewRecorder()
	server.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestTelegramWebhookServerRejectsMalformedJSON(t *testing.T) {
	server, err := newTelegramWebhookServer(telegramWebhookServerOptions{
		ListenAddr: "127.0.0.1:29330",
		Path:       "/_gopher/telegram/webhook",
		Secret:     "secret",
		HandleUpdate: func(_ context.Context, payload []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newTelegramWebhookServer() error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/_gopher/telegram/webhook", strings.NewReader(`{`))
	req.Header.Set(telegramWebhookSecretHeader, "secret")
	rec := httptest.NewRecorder()
	server.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
