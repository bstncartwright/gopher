package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/transport"
)

func TestHandleTransactionParsesInboundDM(t *testing.T) {
	instance, err := New(Options{
		HomeserverURL: "http://127.0.0.1:8008",
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		ListenAddr:    "127.0.0.1:29328",
		BotUserID:     "@gopher:local",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	received := []transport.InboundMessage{}
	instance.SetInboundHandler(func(_ context.Context, message transport.InboundMessage) error {
		received = append(received, message)
		return nil
	})

	payload := transactionBody{
		Events: []matrixEvent{
			{
				EventID: "$one",
				Type:    "m.room.message",
				RoomID:  "!dm:local",
				Sender:  "@user:local",
				Content: map[string]interface{}{"msgtype": "m.text", "body": "hello"},
			},
			{
				EventID: "$ignored-type",
				Type:    "m.room.member",
				RoomID:  "!dm:local",
				Sender:  "@user:local",
				Content: map[string]interface{}{},
			},
			{
				EventID: "$ignored-bot",
				Type:    "m.room.message",
				RoomID:  "!dm:local",
				Sender:  "@gopher:local",
				Content: map[string]interface{}{"msgtype": "m.text", "body": "bot"},
			},
		},
	}
	blob, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/txn-1?access_token=hs-token", bytes.NewReader(blob))
	writer := httptest.NewRecorder()

	instance.handleTransaction(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", writer.Code)
	}
	if len(received) != 1 {
		t.Fatalf("received count = %d, want 1", len(received))
	}
	if received[0].ConversationID != "!dm:local" || received[0].SenderID != "@user:local" || received[0].Text != "hello" {
		t.Fatalf("received payload = %#v", received[0])
	}
}

func TestHandleTransactionIsIdempotentPerTransactionID(t *testing.T) {
	instance, err := New(Options{
		HomeserverURL: "http://127.0.0.1:8008",
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		ListenAddr:    "127.0.0.1:29328",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	count := 0
	instance.SetInboundHandler(func(_ context.Context, _ transport.InboundMessage) error {
		count++
		return nil
	})

	payload := `{"events":[{"event_id":"$one","type":"m.room.message","room_id":"!dm:local","sender":"@user:local","content":{"msgtype":"m.text","body":"hello"}}]}`
	requestA := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/same?access_token=hs-token", bytes.NewBufferString(payload))
	writerA := httptest.NewRecorder()
	instance.handleTransaction(writerA, requestA)

	requestB := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/same?access_token=hs-token", bytes.NewBufferString(payload))
	writerB := httptest.NewRecorder()
	instance.handleTransaction(writerB, requestB)

	if count != 1 {
		t.Fatalf("handler call count = %d, want 1", count)
	}
}

func TestSendMessageCallsHomeserverAPI(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		if request.URL.Path == "/_matrix/client/v3/register" {
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"user_id":"@gopher:local"}`))
			return
		}
		if request.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", request.Method)
		}
		if request.URL.Query().Get("access_token") != "as-token" {
			t.Fatalf("access token mismatch: %q", request.URL.Query().Get("access_token"))
		}
		body, _ := io.ReadAll(request.Body)
		if !bytes.Contains(body, []byte(`"body":"hello matrix"`)) {
			t.Fatalf("unexpected body: %s", string(body))
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"event_id":"$ok"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		ListenAddr:    "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = instance.Start(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	if err := instance.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "!dm:local",
		Text:           "hello matrix",
	}); err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for requestCount < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if requestCount < 1 {
		t.Fatalf("homeserver request count = %d, want >= 1", requestCount)
	}
}

func TestHandleTransactionJoinsRoomOnBotInvite(t *testing.T) {
	joined := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.URL.Query().Get("access_token") != "as-token" {
			t.Fatalf("access token mismatch: %q", request.URL.Query().Get("access_token"))
		}
		if request.URL.Query().Get("user_id") != "@gopher:local" {
			t.Fatalf("user_id mismatch: %q", request.URL.Query().Get("user_id"))
		}
		if request.URL.Path != "/_matrix/client/v3/rooms/!dm:local/join" {
			t.Fatalf("join path mismatch: %q", request.URL.Path)
		}
		body, _ := io.ReadAll(request.Body)
		if strings.TrimSpace(string(body)) != "{}" {
			t.Fatalf("join body = %q, want {}", string(body))
		}
		joined = true
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"room_id":"!dm:local"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		ListenAddr:    "127.0.0.1:29328",
		BotUserID:     "@gopher:local",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	payload := transactionBody{
		Events: []matrixEvent{
			{
				EventID:  "$invite",
				Type:     "m.room.member",
				RoomID:   "!dm:local",
				Sender:   "@user:local",
				StateKey: "@gopher:local",
				Content:  map[string]interface{}{"membership": "invite"},
			},
		},
	}
	blob, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/txn-join?access_token=hs-token", bytes.NewReader(blob))
	writer := httptest.NewRecorder()

	instance.handleTransaction(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", writer.Code)
	}
	if !joined {
		t.Fatalf("expected join request to be sent")
	}
}

func TestHandleTransactionInviteJoinFailureDoesNotBlockDMEvents(t *testing.T) {
	joinAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		joinAttempts++
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"errcode":"M_UNKNOWN","error":"No server available to assist in joining."}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		ListenAddr:    "127.0.0.1:29328",
		BotUserID:     "@gopher:local",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	received := 0
	instance.SetInboundHandler(func(_ context.Context, inbound transport.InboundMessage) error {
		if inbound.EventID == "$msg" {
			received++
		}
		return nil
	})

	payload := transactionBody{
		Events: []matrixEvent{
			{
				EventID:  "$invite",
				Type:     "m.room.member",
				RoomID:   "!invite:local",
				Sender:   "@user:local",
				StateKey: "@gopher:local",
				Content:  map[string]interface{}{"membership": "invite"},
			},
			{
				EventID: "$msg",
				Type:    "m.room.message",
				RoomID:  "!dm:local",
				Sender:  "@user:local",
				Content: map[string]interface{}{"msgtype": "m.text", "body": "hello"},
			},
		},
	}

	blob, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/txn-mixed?access_token=hs-token", bytes.NewReader(blob))
	writer := httptest.NewRecorder()

	instance.handleTransaction(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", writer.Code)
	}
	if joinAttempts != 1 {
		t.Fatalf("join attempts = %d, want 1", joinAttempts)
	}
	if received != 1 {
		t.Fatalf("received = %d, want 1", received)
	}
}

func TestEnsureBotUserRegistersViaAppservice(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/_matrix/client/v3/register" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.URL.Query().Get("access_token") != "as-token" {
			t.Fatalf("access token mismatch: %q", request.URL.Query().Get("access_token"))
		}
		body, _ := io.ReadAll(request.Body)
		if !bytes.Contains(body, []byte(`"type":"m.login.application_service"`)) {
			t.Fatalf("unexpected payload: %s", string(body))
		}
		if !bytes.Contains(body, []byte(`"username":"gopher"`)) {
			t.Fatalf("missing username payload: %s", string(body))
		}
		called = true
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"user_id":"@gopher:local"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		BotUserID:     "@gopher:local",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := instance.ensureBotUser(context.Background()); err != nil {
		t.Fatalf("ensureBotUser error: %v", err)
	}
	if !called {
		t.Fatalf("expected ensureBotUser to call register endpoint")
	}
}

func TestEnsureBotUserAllowsUserInUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"errcode":"M_USER_IN_USE","error":"user already exists"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		BotUserID:     "@gopher:local",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := instance.ensureBotUser(context.Background()); err != nil {
		t.Fatalf("ensureBotUser error: %v", err)
	}
}

func TestEnsureBotUserAllowsUserInUseConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusConflict)
		_, _ = writer.Write([]byte(`{"errcode":"M_USER_IN_USE","error":"user already exists"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		BotUserID:     "@gopher:local",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := instance.ensureBotUser(context.Background()); err != nil {
		t.Fatalf("ensureBotUser error: %v", err)
	}
}

func TestSendMessageQueueFullWhenNotStarted(t *testing.T) {
	instance, err := New(Options{
		HomeserverURL: "http://127.0.0.1:8008",
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		QueueCapacity: 1,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	err = instance.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "!dm:local",
		Text:           "one",
	})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("expected not running error, got: %v", err)
	}
}

func TestHandleTransactionSoftIdempotentReplayOnHandlerFailure(t *testing.T) {
	instance, err := New(Options{
		HomeserverURL: "http://127.0.0.1:8008",
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	calls := 0
	instance.SetInboundHandler(func(_ context.Context, _ transport.InboundMessage) error {
		calls++
		if calls == 1 {
			return context.DeadlineExceeded
		}
		return nil
	})
	payload := `{"events":[{"event_id":"$event-replay","type":"m.room.message","room_id":"!dm:local","sender":"@user:local","content":{"msgtype":"m.text","body":"hello"}}]}`
	reqA := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/txn-a?access_token=hs-token", bytes.NewBufferString(payload))
	wA := httptest.NewRecorder()
	instance.handleTransaction(wA, reqA)
	if wA.Code != http.StatusInternalServerError {
		t.Fatalf("first response status = %d, want 500", wA.Code)
	}
	reqB := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/txn-b?access_token=hs-token", bytes.NewBufferString(payload))
	wB := httptest.NewRecorder()
	instance.handleTransaction(wB, reqB)
	if wB.Code != http.StatusOK {
		t.Fatalf("second response status = %d, want 200", wB.Code)
	}
	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2 for replay", calls)
	}
}

func TestHandleTransactionAcceptsBearerAuth(t *testing.T) {
	instance, err := New(Options{
		HomeserverURL: "http://127.0.0.1:8008",
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	count := 0
	instance.SetInboundHandler(func(_ context.Context, _ transport.InboundMessage) error {
		count++
		return nil
	})

	payload := `{"events":[{"event_id":"$bearer","type":"m.room.message","room_id":"!dm:local","sender":"@user:local","content":{"msgtype":"m.text","body":"hello"}}]}`
	req := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/txn-bearer", bytes.NewBufferString(payload))
	req.Header.Set("Authorization", "Bearer hs-token")
	w := httptest.NewRecorder()
	instance.handleTransaction(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if count != 1 {
		t.Fatalf("handler count = %d, want 1", count)
	}
}
