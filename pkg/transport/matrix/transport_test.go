package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
		ListenAddr:    "127.0.0.1:29328",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := instance.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "!dm:local",
		Text:           "hello matrix",
	}); err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("homeserver request count = %d, want 1", requestCount)
	}
}
