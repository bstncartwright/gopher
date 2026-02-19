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

func TestHandleTransactionIncludesConversationNameFromRoomState(t *testing.T) {
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
				EventID: "$name",
				Type:    "m.room.name",
				RoomID:  "!dm:local",
				Sender:  "@user:local",
				Content: map[string]interface{}{"name": "Writer Room"},
			},
			{
				EventID: "$one",
				Type:    "m.room.message",
				RoomID:  "!dm:local",
				Sender:  "@user:local",
				Content: map[string]interface{}{"msgtype": "m.text", "body": "hello"},
			},
		},
	}
	blob, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/txn-name?access_token=hs-token", bytes.NewReader(blob))
	writer := httptest.NewRecorder()

	instance.handleTransaction(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", writer.Code)
	}
	if len(received) != 1 {
		t.Fatalf("received count = %d, want 1", len(received))
	}
	if received[0].ConversationName != "Writer Room" {
		t.Fatalf("conversation name = %q, want Writer Room", received[0].ConversationName)
	}
}

func TestHandleTransactionIncludesManagedSenderMessages(t *testing.T) {
	instance, err := New(Options{
		HomeserverURL:  "http://127.0.0.1:8008",
		AppserviceID:   "gopher",
		ASToken:        "as-token",
		HSToken:        "hs-token",
		BotUserID:      "@planner:local",
		ManagedUserIDs: []string{"@writer:local"},
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
				EventID: "$managed-msg",
				Type:    "m.room.message",
				RoomID:  "!room:local",
				Sender:  "@writer:local",
				Content: map[string]interface{}{"msgtype": "m.text", "body": "handoff"},
			},
		},
	}
	blob, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/txn-managed?access_token=hs-token", bytes.NewReader(blob))
	writer := httptest.NewRecorder()

	instance.handleTransaction(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", writer.Code)
	}
	if len(received) != 1 {
		t.Fatalf("received count = %d, want 1", len(received))
	}
	if !received[0].SenderManaged {
		t.Fatalf("expected sender managed flag")
	}
	if received[0].SenderID != "@writer:local" {
		t.Fatalf("sender id = %q, want @writer:local", received[0].SenderID)
	}
}

func TestHandleTransactionSetsRecipientFromManagedMembership(t *testing.T) {
	instance, err := New(Options{
		HomeserverURL:  "http://127.0.0.1:8008",
		AppserviceID:   "gopher",
		ASToken:        "as-token",
		HSToken:        "hs-token",
		BotUserID:      "@planner:local",
		ManagedUserIDs: []string{"@writer:local"},
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
				EventID:  "$invite",
				Type:     "m.room.member",
				RoomID:   "!dm:writer",
				Sender:   "@user:local",
				StateKey: "@writer:local",
				Content:  map[string]interface{}{"membership": "invite"},
			},
			{
				EventID: "$msg",
				Type:    "m.room.message",
				RoomID:  "!dm:writer",
				Sender:  "@user:local",
				Content: map[string]interface{}{"msgtype": "m.text", "body": "hello"},
			},
		},
	}
	blob, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPut, "/_matrix/app/v1/transactions/txn-recipient?access_token=hs-token", bytes.NewReader(blob))
	writer := httptest.NewRecorder()

	instance.handleTransaction(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", writer.Code)
	}
	if len(received) != 1 {
		t.Fatalf("received count = %d, want 1", len(received))
	}
	if received[0].RecipientID != "@writer:local" {
		t.Fatalf("recipient id = %q, want @writer:local", received[0].RecipientID)
	}
}

func TestManagedUsersForConversationReturnsSortedSnapshot(t *testing.T) {
	instance, err := New(Options{
		HomeserverURL:  "http://127.0.0.1:8008",
		AppserviceID:   "gopher",
		ASToken:        "as-token",
		HSToken:        "hs-token",
		BotUserID:      "@planner:local",
		ManagedUserIDs: []string{"@writer:local"},
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	instance.setRoomManagedUser("!room:one", "@writer:local")
	instance.setRoomManagedUser("!room:one", "@planner:local")

	users := instance.ManagedUsersForConversation("!room:one")
	if len(users) != 2 {
		t.Fatalf("managed users length = %d, want 2", len(users))
	}
	if users[0] != "@planner:local" || users[1] != "@writer:local" {
		t.Fatalf("managed users = %#v, want sorted planner/writer", users)
	}

	users[0] = "@mutated:local"
	latest := instance.ManagedUsersForConversation("!room:one")
	if latest[0] != "@planner:local" {
		t.Fatalf("expected snapshot copy; got %#v", latest)
	}
}

func TestManagedUsersForConversationUnknownRoomReturnsEmpty(t *testing.T) {
	instance, err := New(Options{
		HomeserverURL: "http://127.0.0.1:8008",
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		BotUserID:     "@planner:local",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if got := instance.ManagedUsersForConversation("!missing:local"); len(got) != 0 {
		t.Fatalf("managed users length = %d, want 0", len(got))
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

func TestSendMessageNowUsesProvidedSenderID(t *testing.T) {
	seenUserID := ""
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenUserID = request.URL.Query().Get("user_id")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"event_id":"$ok"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if err := instance.sendMessageNow(context.Background(), transport.OutboundMessage{
		ConversationID: "!dm:writer",
		SenderID:       "@writer:local",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("sendMessageNow() error: %v", err)
	}
	if seenUserID != "@writer:local" {
		t.Fatalf("user_id = %q, want @writer:local", seenUserID)
	}
}

func TestCreatePublicRoomCallsHomeserverAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/_matrix/client/v3/createRoom" {
			t.Fatalf("path = %q, want createRoom", request.URL.Path)
		}
		if request.URL.Query().Get("user_id") != "@planner:local" {
			t.Fatalf("user_id = %q, want @planner:local", request.URL.Query().Get("user_id"))
		}
		body, _ := io.ReadAll(request.Body)
		if !bytes.Contains(body, []byte(`"visibility":"public"`)) {
			t.Fatalf("expected public visibility payload: %s", string(body))
		}
		if !bytes.Contains(body, []byte(`"invite":["@target:local","@user:local"]`)) {
			t.Fatalf("expected invite list payload: %s", string(body))
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"room_id":"!new:local"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	roomID, err := instance.CreatePublicRoom(context.Background(), CreatePublicRoomOptions{
		Name:          "Delegation",
		Topic:         "session",
		CreatorUserID: "@planner:local",
		InviteUserIDs: []string{"@user:local", "@target:local", "@planner:local"},
	})
	if err != nil {
		t.Fatalf("CreatePublicRoom() error: %v", err)
	}
	if roomID != "!new:local" {
		t.Fatalf("room_id = %q, want !new:local", roomID)
	}
}

func TestInviteToRoomCallsHomeserverAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/_matrix/client/v3/rooms/!new:local/invite" {
			t.Fatalf("path = %q, want invite path", request.URL.Path)
		}
		if request.URL.Query().Get("user_id") != "@planner:local" {
			t.Fatalf("user_id = %q, want @planner:local", request.URL.Query().Get("user_id"))
		}
		body, _ := io.ReadAll(request.Body)
		if !bytes.Contains(body, []byte(`"user_id":"@writer:local"`)) {
			t.Fatalf("invite payload mismatch: %s", string(body))
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL:  server.URL,
		AppserviceID:   "gopher",
		ASToken:        "as-token",
		HSToken:        "hs-token",
		ManagedUserIDs: []string{"@writer:local"},
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if err := instance.InviteToRoom(context.Background(), "!new:local", "@writer:local", "@planner:local"); err != nil {
		t.Fatalf("InviteToRoom() error: %v", err)
	}
}

func TestSendMessageNowIncludesFormattedBodyWhenRichTextEnabled(t *testing.T) {
	var payload outboundMessagePayload
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", request.Method)
		}
		body, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"event_id":"$ok"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL:   server.URL,
		AppserviceID:    "gopher",
		ASToken:         "as-token",
		HSToken:         "hs-token",
		RichTextEnabled: true,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if err := instance.sendMessageNow(context.Background(), transport.OutboundMessage{
		ConversationID: "!dm:local",
		Text:           "# hello\n\n**world**",
	}); err != nil {
		t.Fatalf("sendMessageNow() error: %v", err)
	}

	if payload.MsgType != "m.text" {
		t.Fatalf("msgtype = %q, want m.text", payload.MsgType)
	}
	if payload.Body != "# hello\n\n**world**" {
		t.Fatalf("body = %q", payload.Body)
	}
	if payload.Format != matrixMessageHTMLFormat {
		t.Fatalf("format = %q, want %q", payload.Format, matrixMessageHTMLFormat)
	}
	if strings.TrimSpace(payload.FormattedBody) == "" {
		t.Fatalf("formatted_body is empty")
	}
}

func TestSendMessageNowOmitsFormattedBodyWhenRichTextDisabled(t *testing.T) {
	var payload outboundMessagePayload
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"event_id":"$ok"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL:   server.URL,
		AppserviceID:    "gopher",
		ASToken:         "as-token",
		HSToken:         "hs-token",
		RichTextEnabled: false,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if err := instance.sendMessageNow(context.Background(), transport.OutboundMessage{
		ConversationID: "!dm:local",
		Text:           "**plain**",
	}); err != nil {
		t.Fatalf("sendMessageNow() error: %v", err)
	}

	if payload.Format != "" {
		t.Fatalf("format = %q, want empty", payload.Format)
	}
	if payload.FormattedBody != "" {
		t.Fatalf("formatted_body = %q, want empty", payload.FormattedBody)
	}
}

func TestSendMessageNowFallsBackWhenFormatterUnavailable(t *testing.T) {
	var payload outboundMessagePayload
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"event_id":"$ok"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL:   server.URL,
		AppserviceID:    "gopher",
		ASToken:         "as-token",
		HSToken:         "hs-token",
		RichTextEnabled: true,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	instance.formatter = nil

	if err := instance.sendMessageNow(context.Background(), transport.OutboundMessage{
		ConversationID: "!dm:local",
		Text:           "**hello**",
	}); err != nil {
		t.Fatalf("sendMessageNow() error: %v", err)
	}

	if payload.Format != "" {
		t.Fatalf("format = %q, want empty", payload.Format)
	}
	if payload.FormattedBody != "" {
		t.Fatalf("formatted_body = %q, want empty", payload.FormattedBody)
	}
}

func TestSendTypingCallsHomeserverAPI(t *testing.T) {
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
		if request.URL.Query().Get("user_id") != "@gopher:local" {
			t.Fatalf("user_id mismatch: %q", request.URL.Query().Get("user_id"))
		}
		if !strings.Contains(request.URL.Path, "/typing/") {
			t.Fatalf("path = %q, expected typing endpoint", request.URL.Path)
		}
		body, _ := io.ReadAll(request.Body)
		if !bytes.Contains(body, []byte(`"typing":true`)) {
			t.Fatalf("unexpected body: %s", string(body))
		}
		if !bytes.Contains(body, []byte(`"timeout":8000`)) {
			t.Fatalf("missing timeout in body: %s", string(body))
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		ListenAddr:    "127.0.0.1:0",
		BotUserID:     "@gopher:local",
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
	if err := instance.SendTyping(context.Background(), "!dm:local", true); err != nil {
		t.Fatalf("SendTyping() error: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for requestCount < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if requestCount < 2 {
		t.Fatalf("homeserver request count = %d, want >= 2", requestCount)
	}
}

func TestPresenceStartKeepaliveAndStop(t *testing.T) {
	onlineCalls := 0
	offlineCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/register":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"user_id":"@gopher:local"}`))
			return
		case "/_matrix/client/v3/presence/@gopher:local/status":
			var payload map[string]string
			body, _ := io.ReadAll(request.Body)
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("json.Unmarshal() error: %v", err)
			}
			if request.URL.Query().Get("user_id") != "@gopher:local" {
				t.Fatalf("user_id mismatch: %q", request.URL.Query().Get("user_id"))
			}
			switch payload["presence"] {
			case presenceStateOnline:
				onlineCalls++
				if payload["status_msg"] != "gopher online" {
					t.Fatalf("status_msg = %q, want gopher online", payload["status_msg"])
				}
			case presenceStateOffline:
				offlineCalls++
			default:
				t.Fatalf("unexpected presence payload: %q", payload["presence"])
			}
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{}`))
			return
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL:     server.URL,
		AppserviceID:      "gopher",
		ASToken:           "as-token",
		HSToken:           "hs-token",
		ListenAddr:        "127.0.0.1:0",
		BotUserID:         "@gopher:local",
		PresenceEnabled:   true,
		PresenceInterval:  25 * time.Millisecond,
		PresenceStatusMsg: "gopher online",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = instance.Start(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for onlineCalls < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if onlineCalls < 2 {
		t.Fatalf("onlineCalls = %d, want >= 2", onlineCalls)
	}

	if err := instance.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for offlineCalls < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if offlineCalls < 1 {
		t.Fatalf("offlineCalls = %d, want >= 1", offlineCalls)
	}
}

func TestPresenceFailureDoesNotBlockMessaging(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/register":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"user_id":"@gopher:local"}`))
			return
		case "/_matrix/client/v3/presence/@gopher:local/status":
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"errcode":"M_UNKNOWN","error":"presence disabled"}`))
			return
		default:
			if strings.Contains(request.URL.Path, "/send/m.room.message/") {
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte(`{"event_id":"$ok"}`))
				return
			}
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL:    server.URL,
		AppserviceID:     "gopher",
		ASToken:          "as-token",
		HSToken:          "hs-token",
		ListenAddr:       "127.0.0.1:0",
		BotUserID:        "@gopher:local",
		PresenceEnabled:  true,
		PresenceInterval: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = instance.Start(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		stats := instance.snapshotMetrics()
		if stats.PresenceFailures > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("presence failures not observed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := instance.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "!dm:local",
		Text:           "still works",
	}); err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}

	if err := instance.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
}

func TestPresenceDisabledSkipsPresenceCalls(t *testing.T) {
	presenceCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/register":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"user_id":"@gopher:local"}`))
			return
		case "/_matrix/client/v3/presence/@gopher:local/status":
			presenceCalls++
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{}`))
			return
		default:
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{}`))
			return
		}
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL:   server.URL,
		AppserviceID:    "gopher",
		ASToken:         "as-token",
		HSToken:         "hs-token",
		ListenAddr:      "127.0.0.1:0",
		BotUserID:       "@gopher:local",
		PresenceEnabled: false,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = instance.Start(ctx)
	}()
	time.Sleep(60 * time.Millisecond)

	if err := instance.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	if presenceCalls != 0 {
		t.Fatalf("presenceCalls = %d, want 0", presenceCalls)
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

func TestEnsureBotUserRegistersManagedUsers(t *testing.T) {
	usernames := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if strings.Contains(string(body), `"username":"planner"`) {
			usernames = append(usernames, "planner")
		}
		if strings.Contains(string(body), `"username":"writer"`) {
			usernames = append(usernames, "writer")
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"user_id":"ok"}`))
	}))
	defer server.Close()

	instance, err := New(Options{
		HomeserverURL:  server.URL,
		AppserviceID:   "gopher",
		ASToken:        "as-token",
		HSToken:        "hs-token",
		BotUserID:      "@planner:local",
		ManagedUserIDs: []string{"@writer:local"},
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := instance.ensureBotUser(context.Background()); err != nil {
		t.Fatalf("ensureBotUser error: %v", err)
	}
	if len(usernames) != 2 {
		t.Fatalf("registered users = %v, want planner+writer", usernames)
	}
}

func TestEnsureBotUserSetsProfileAvatarWhenMissing(t *testing.T) {
	uploaded := false
	updated := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/register":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"user_id":"@gopher:local"}`))
		case "/_matrix/client/v3/profile/@gopher:local/avatar_url":
			if request.Method == http.MethodGet {
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte(`{"avatar_url":""}`))
				return
			}
			if request.Method == http.MethodPut {
				updated = true
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte(`{}`))
				return
			}
			t.Fatalf("unexpected profile method: %s", request.Method)
		case "/_matrix/media/v3/upload":
			uploaded = true
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"content_uri":"mxc://local/avatar"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	providerCalls := 0
	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		BotUserID:     "@gopher:local",
		AvatarProvider: func(_ context.Context, userID string) (ManagedAvatar, error) {
			providerCalls++
			if userID != "@gopher:local" {
				t.Fatalf("provider userID = %q", userID)
			}
			return ManagedAvatar{
				ContentType: "image/png",
				Data:        []byte{0x89, 'P', 'N', 'G'},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := instance.ensureBotUser(context.Background()); err != nil {
		t.Fatalf("ensureBotUser error: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("providerCalls = %d, want 1", providerCalls)
	}
	if !uploaded {
		t.Fatalf("expected avatar upload")
	}
	if !updated {
		t.Fatalf("expected profile avatar update")
	}
}

func TestEnsureBotUserSkipsProfileAvatarWhenAlreadySet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/register":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"user_id":"@gopher:local"}`))
		case "/_matrix/client/v3/profile/@gopher:local/avatar_url":
			if request.Method != http.MethodGet {
				t.Fatalf("unexpected profile method: %s", request.Method)
			}
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"avatar_url":"mxc://local/existing"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	providerCalls := 0
	instance, err := New(Options{
		HomeserverURL: server.URL,
		AppserviceID:  "gopher",
		ASToken:       "as-token",
		HSToken:       "hs-token",
		BotUserID:     "@gopher:local",
		AvatarProvider: func(_ context.Context, _ string) (ManagedAvatar, error) {
			providerCalls++
			return ManagedAvatar{MXCURL: "mxc://local/new"}, nil
		},
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := instance.ensureBotUser(context.Background()); err != nil {
		t.Fatalf("ensureBotUser error: %v", err)
	}
	if providerCalls != 0 {
		t.Fatalf("providerCalls = %d, want 0", providerCalls)
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
