package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/bstncartwright/gopher/pkg/transport"
)

type Options struct {
	HomeserverURL string
	AppserviceID  string
	ASToken       string
	HSToken       string
	ListenAddr    string
	BotUserID     string
	HTTPClient    *http.Client
}

type Transport struct {
	homeserverURL string
	appserviceID  string
	asToken       string
	hsToken       string
	listenAddr    string
	botUserID     string
	httpClient    *http.Client

	mu      sync.RWMutex
	handler transport.InboundHandler
	seenTxn map[string]struct{}
	server  *http.Server
}

type transactionBody struct {
	Events []matrixEvent `json:"events"`
}

type matrixEvent struct {
	EventID string                 `json:"event_id"`
	Type    string                 `json:"type"`
	RoomID  string                 `json:"room_id"`
	Sender  string                 `json:"sender"`
	Content map[string]interface{} `json:"content"`
}

var _ transport.Transport = (*Transport)(nil)

func New(opts Options) (*Transport, error) {
	hs := strings.TrimSpace(opts.HomeserverURL)
	if hs == "" {
		return nil, fmt.Errorf("matrix homeserver url is required")
	}
	if _, err := url.Parse(hs); err != nil {
		return nil, fmt.Errorf("invalid matrix homeserver url: %w", err)
	}
	if strings.TrimSpace(opts.ASToken) == "" {
		return nil, fmt.Errorf("matrix as token is required")
	}
	if strings.TrimSpace(opts.HSToken) == "" {
		return nil, fmt.Errorf("matrix hs token is required")
	}
	addr := strings.TrimSpace(opts.ListenAddr)
	if addr == "" {
		addr = "127.0.0.1:29328"
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Transport{
		homeserverURL: strings.TrimRight(hs, "/"),
		appserviceID:  strings.TrimSpace(opts.AppserviceID),
		asToken:       strings.TrimSpace(opts.ASToken),
		hsToken:       strings.TrimSpace(opts.HSToken),
		listenAddr:    addr,
		botUserID:     strings.TrimSpace(opts.BotUserID),
		httpClient:    client,
		seenTxn:       map[string]struct{}{},
	}, nil
}

func (t *Transport) SetInboundHandler(handler transport.InboundHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.handler = handler
}

func (t *Transport) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/app/v1/transactions/", t.handleTransaction)
	mux.HandleFunc("/_matrix/app/v1/users/", t.handleAppservicePing)
	mux.HandleFunc("/_matrix/app/v1/rooms/", t.handleAppservicePing)

	server := &http.Server{
		Addr:    t.listenAddr,
		Handler: mux,
	}

	t.mu.Lock()
	t.server = server
	t.mu.Unlock()

	go func() {
		<-ctx.Done()
		_ = t.Stop()
	}()

	err := server.ListenAndServe()
	if err == nil || err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (t *Transport) Stop() error {
	t.mu.RLock()
	server := t.server
	t.mu.RUnlock()
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

func (t *Transport) SendMessage(ctx context.Context, message transport.OutboundMessage) error {
	roomID := strings.TrimSpace(message.ConversationID)
	if roomID == "" {
		return fmt.Errorf("matrix outbound conversation id is required")
	}
	bodyText := strings.TrimSpace(message.Text)
	if bodyText == "" {
		return nil
	}

	txnID := fmt.Sprintf("gopher-%d", time.Now().UTC().UnixNano())
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s?access_token=%s",
		t.homeserverURL,
		url.PathEscape(roomID),
		url.PathEscape(txnID),
		url.QueryEscape(t.asToken),
	)
	payload := map[string]string{
		"msgtype": "m.text",
		"body":    bodyText,
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal matrix outbound payload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(string(blob)))
	if err != nil {
		return fmt.Errorf("build matrix outbound request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := t.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send matrix outbound message: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("matrix outbound status: %s", response.Status)
	}
	return nil
}

func (t *Transport) handleAppservicePing(writer http.ResponseWriter, request *http.Request) {
	if !t.isAuthorized(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	writer.Header().Set("content-type", "application/json")
	_, _ = writer.Write([]byte(`{}`))
}

func (t *Transport) handleTransaction(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !t.isAuthorized(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}

	txnID := path.Base(request.URL.Path)
	if strings.TrimSpace(txnID) == "" || txnID == "transactions" {
		http.Error(writer, "transaction id is required", http.StatusBadRequest)
		return
	}
	if t.isDuplicateTransaction(txnID) {
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
		return
	}

	body := transactionBody{}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(writer, "invalid payload", http.StatusBadRequest)
		return
	}
	for _, event := range body.Events {
		inbound, ok := t.toInboundMessage(event)
		if !ok {
			continue
		}
		handler := t.getHandler()
		if handler == nil {
			continue
		}
		_ = handler(request.Context(), inbound)
	}
	writer.Header().Set("content-type", "application/json")
	_, _ = writer.Write([]byte(`{}`))
}

func (t *Transport) toInboundMessage(event matrixEvent) (transport.InboundMessage, bool) {
	if event.Type != "m.room.message" {
		return transport.InboundMessage{}, false
	}
	if strings.TrimSpace(event.RoomID) == "" || strings.TrimSpace(event.Sender) == "" {
		return transport.InboundMessage{}, false
	}
	if t.botUserID != "" && event.Sender == t.botUserID {
		return transport.InboundMessage{}, false
	}
	msgType, _ := event.Content["msgtype"].(string)
	if msgType != "m.text" {
		return transport.InboundMessage{}, false
	}
	body, _ := event.Content["body"].(string)
	body = strings.TrimSpace(body)
	if body == "" {
		return transport.InboundMessage{}, false
	}
	return transport.InboundMessage{
		ConversationID: event.RoomID,
		SenderID:       event.Sender,
		EventID:        strings.TrimSpace(event.EventID),
		Text:           body,
	}, true
}

func (t *Transport) isAuthorized(request *http.Request) bool {
	token := strings.TrimSpace(request.URL.Query().Get("access_token"))
	return token != "" && token == t.hsToken
}

func (t *Transport) isDuplicateTransaction(txnID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.seenTxn[txnID]; exists {
		return true
	}
	t.seenTxn[txnID] = struct{}{}
	return false
}

func (t *Transport) getHandler() transport.InboundHandler {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.handler
}
