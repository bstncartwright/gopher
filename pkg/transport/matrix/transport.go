package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bstncartwright/gopher/pkg/transport"
)

type Options struct {
	HomeserverURL     string
	AppserviceID      string
	ASToken           string
	HSToken           string
	ListenAddr        string
	BotUserID         string
	ManagedUserIDs    []string
	RichTextEnabled   bool
	PresenceEnabled   bool
	PresenceInterval  time.Duration
	PresenceStatusMsg string
	HTTPClient        *http.Client
	QueueCapacity     int
	MaxAttempts       int
	DedupeTTL         time.Duration
	AvatarProvider    ManagedAvatarProvider
}

type Transport struct {
	homeserverURL     string
	appserviceID      string
	asToken           string
	hsToken           string
	listenAddr        string
	botUserID         string
	managedUserIDs    []string
	managedUsers      map[string]struct{}
	roomManagedUsers  map[string]map[string]struct{}
	roomNames         map[string]string
	richTextEnabled   bool
	presenceEnabled   bool
	presenceInterval  time.Duration
	presenceStatusMsg string
	httpClient        *http.Client
	formatter         richTextFormatter

	mu      sync.RWMutex
	handler transport.InboundHandler
	seenTxn map[string]time.Time
	events  map[string]eventDeliveryState
	server  *http.Server

	started         bool
	workerCtx       context.Context
	workerCancel    context.CancelFunc
	outboundQueue   chan queuedOutbound
	outboundDone    chan struct{}
	typingQueue     chan queuedOutbound
	typingDone      chan struct{}
	presenceDone    chan struct{}
	queueCapacity   int
	maxAttempts     int
	dedupeTTL       time.Duration
	droppedMu       sync.Mutex
	droppedReplay   []queuedOutbound
	droppedCapacity int
	avatarProvider  ManagedAvatarProvider
	stats           matrixStats
	inboundFailures atomic.Uint64
}

type queuedOutbound struct {
	kind                 outboundKind
	message              transport.OutboundMessage
	typingConversationID string
	typingUserID         string
	typing               bool
	eventID              string
	attempt              int
}

type outboundKind int

const (
	outboundKindMessage outboundKind = iota
	outboundKindTyping
)

type deliveryStatus string

const (
	deliveryStatusDelivered deliveryStatus = "delivered"
	deliveryStatusRetryable deliveryStatus = "retryable_failure"
)

type eventDeliveryState struct {
	Status    deliveryStatus
	UpdatedAt time.Time
}

type matrixStats struct {
	mu                     sync.RWMutex
	LastInboundTxnAt       time.Time `json:"last_inbound_txn_at,omitempty"`
	LastOutboundSuccessAt  time.Time `json:"last_outbound_success_at,omitempty"`
	PresenceLastSuccessAt  time.Time `json:"presence_last_success_at,omitempty"`
	QueueDepth             int       `json:"queue_depth"`
	OutboundRetries        uint64    `json:"outbound_retries"`
	OutboundDropped        uint64    `json:"outbound_dropped"`
	OutboundReplayPending  int       `json:"outbound_replay_pending"`
	OutboundTransientErrs  uint64    `json:"outbound_transient_errors"`
	OutboundPermanentErrs  uint64    `json:"outbound_permanent_errors"`
	DuplicateTxnSeen       uint64    `json:"duplicate_txn_seen"`
	DuplicateEventsSkipped uint64    `json:"duplicate_events_skipped"`
	ReplayEventsProcessed  uint64    `json:"replay_events_processed"`
	TraceRoomsCreated      uint64    `json:"trace_rooms_created_total"`
	TracePublishSuccess    uint64    `json:"trace_publish_success_total"`
	TracePublishFailure    uint64    `json:"trace_publish_failure_total"`
	TraceInboundIgnored    uint64    `json:"trace_events_ignored_inbound_total"`
	PresenceEnabled        bool      `json:"presence_enabled"`
	PresenceState          string    `json:"presence_state"`
	PresenceFailures       uint64    `json:"presence_failures"`
	PresenceLastError      string    `json:"presence_last_error,omitempty"`
}

type matrixStatsSnapshot struct {
	LastInboundTxnAt       time.Time
	LastOutboundSuccessAt  time.Time
	PresenceLastSuccessAt  time.Time
	QueueDepth             int
	OutboundRetries        uint64
	OutboundDropped        uint64
	OutboundReplayPending  int
	OutboundTransientErrs  uint64
	OutboundPermanentErrs  uint64
	DuplicateTxnSeen       uint64
	DuplicateEventsSkipped uint64
	ReplayEventsProcessed  uint64
	TraceRoomsCreated      uint64
	TracePublishSuccess    uint64
	TracePublishFailure    uint64
	TraceInboundIgnored    uint64
	PresenceEnabled        bool
	PresenceState          string
	PresenceFailures       uint64
	PresenceLastError      string
}

type transactionBody struct {
	Events []matrixEvent `json:"events"`
}

type matrixEvent struct {
	EventID  string                 `json:"event_id"`
	Type     string                 `json:"type"`
	RoomID   string                 `json:"room_id"`
	Sender   string                 `json:"sender"`
	StateKey string                 `json:"state_key"`
	Content  map[string]interface{} `json:"content"`
}

const inboundEventTimeout = 15 * time.Second
const matrixMessageHTMLFormat = "org.matrix.custom.html"
const defaultPresenceInterval = 60 * time.Second
const presenceRequestTimeout = 5 * time.Second
const matrixTypingTimeoutMillis = 8000
const (
	presenceStateOnline  = "online"
	presenceStateOffline = "offline"
	presenceStateUnknown = "unknown"
)

type outboundMessagePayload struct {
	MsgType       string           `json:"msgtype"`
	Body          string           `json:"body"`
	Format        string           `json:"format,omitempty"`
	FormattedBody string           `json:"formatted_body,omitempty"`
	RelatesTo     *matrixRelatesTo `json:"m.relates_to,omitempty"`
}

type matrixRelatesTo struct {
	RelType       string           `json:"rel_type,omitempty"`
	EventID       string           `json:"event_id,omitempty"`
	IsFallingBack bool             `json:"is_falling_back,omitempty"`
	InReplyTo     *matrixInReplyTo `json:"m.in_reply_to,omitempty"`
}

type matrixInReplyTo struct {
	EventID string `json:"event_id"`
}

type matrixSendResponse struct {
	EventID string `json:"event_id"`
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
	queueCapacity := opts.QueueCapacity
	if queueCapacity <= 0 {
		queueCapacity = 256
	}
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 4
	}
	dedupeTTL := opts.DedupeTTL
	if dedupeTTL <= 0 {
		dedupeTTL = 15 * time.Minute
	}
	presenceInterval := opts.PresenceInterval
	if presenceInterval <= 0 {
		presenceInterval = defaultPresenceInterval
	}
	botUserID, managedUserIDs, managedUsers := normalizeManagedUserIDs(strings.TrimSpace(opts.BotUserID), opts.ManagedUserIDs)
	presenceEnabled := opts.PresenceEnabled && botUserID != ""
	return &Transport{
		homeserverURL:     strings.TrimRight(hs, "/"),
		appserviceID:      strings.TrimSpace(opts.AppserviceID),
		asToken:           strings.TrimSpace(opts.ASToken),
		hsToken:           strings.TrimSpace(opts.HSToken),
		listenAddr:        addr,
		botUserID:         botUserID,
		managedUserIDs:    managedUserIDs,
		managedUsers:      managedUsers,
		roomManagedUsers:  map[string]map[string]struct{}{},
		roomNames:         map[string]string{},
		richTextEnabled:   opts.RichTextEnabled,
		presenceEnabled:   presenceEnabled,
		presenceInterval:  presenceInterval,
		presenceStatusMsg: strings.TrimSpace(opts.PresenceStatusMsg),
		httpClient:        client,
		formatter:         newMarkdownHTMLFormatter(),
		seenTxn:           map[string]time.Time{},
		events:            map[string]eventDeliveryState{},
		queueCapacity:     queueCapacity,
		maxAttempts:       maxAttempts,
		dedupeTTL:         dedupeTTL,
		droppedCapacity:   droppedReplayCapacity(queueCapacity),
		avatarProvider:    opts.AvatarProvider,
		stats: matrixStats{
			PresenceEnabled: presenceEnabled,
			PresenceState:   presenceStateUnknown,
		},
	}, nil
}

func (t *Transport) SetInboundHandler(handler transport.InboundHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.handler = handler
}

func (t *Transport) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return fmt.Errorf("matrix transport already started")
	}
	t.started = true
	t.mu.Unlock()

	if err := t.ensureBotUser(ctx); err != nil {
		t.mu.Lock()
		t.started = false
		t.mu.Unlock()
		return fmt.Errorf("ensure matrix bot user: %w", err)
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	outboundQueue := make(chan queuedOutbound, t.queueCapacity)
	outboundDone := make(chan struct{})
	typingQueue := make(chan queuedOutbound, typingQueueCapacity(t.queueCapacity))
	typingDone := make(chan struct{})
	var presenceDone chan struct{}
	if t.presenceEnabled {
		presenceDone = make(chan struct{})
	}

	t.mu.Lock()
	if !t.started {
		t.mu.Unlock()
		workerCancel()
		return fmt.Errorf("matrix transport stopped during startup")
	}
	t.outboundQueue = outboundQueue
	t.outboundDone = outboundDone
	t.typingQueue = typingQueue
	t.typingDone = typingDone
	t.presenceDone = presenceDone
	t.workerCtx = workerCtx
	t.workerCancel = workerCancel
	t.mu.Unlock()

	go t.runOutboundWorker(workerCtx, outboundQueue, outboundDone)
	go t.runTypingWorker(workerCtx, typingQueue, typingDone)
	if presenceDone != nil {
		t.applyPresenceUpdate(ctx, presenceStateOnline, false)
		go t.runPresenceWorker(workerCtx, presenceDone)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/app/v1/transactions/", t.handleTransaction)
	mux.HandleFunc("/_matrix/app/v1/users/", t.handleAppservicePing)
	mux.HandleFunc("/_matrix/app/v1/rooms/", t.handleAppservicePing)
	mux.HandleFunc("/_gopher/matrix/health", t.handleHealth)
	mux.HandleFunc("/_gopher/matrix/metrics", t.handleMetrics)

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
	t.shutdownWorker()
	if err == nil || err == http.ErrServerClosed || strings.Contains(strings.ToLower(err.Error()), "closed network connection") {
		return nil
	}
	return err
}

func (t *Transport) Stop() error {
	t.mu.RLock()
	started := t.started
	t.mu.RUnlock()
	if started && t.presenceEnabled {
		t.applyPresenceUpdate(context.Background(), presenceStateOffline, true)
	}

	t.shutdownWorker()

	t.mu.Lock()
	server := t.server
	t.server = nil
	t.started = false
	t.mu.Unlock()
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
	t.mu.RLock()
	started := t.started
	queue := t.outboundQueue
	t.mu.RUnlock()
	if !started || queue == nil {
		return fmt.Errorf("matrix transport is not running")
	}

	item := queuedOutbound{
		kind: outboundKindMessage,
		message: transport.OutboundMessage{
			ConversationID: roomID,
			SenderID:       strings.TrimSpace(message.SenderID),
			Text:           bodyText,
		},
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case queue <- item:
		t.recordQueueDepth(len(queue))
		return nil
	default:
		t.incrementDropped()
		t.enqueueDroppedForReplay(item)
		return fmt.Errorf("matrix outbound queue is full")
	}
}

func (t *Transport) SendMessageWithResult(ctx context.Context, message transport.OutboundMessage) (transport.OutboundSendResult, error) {
	roomID := strings.TrimSpace(message.ConversationID)
	if roomID == "" {
		return transport.OutboundSendResult{}, fmt.Errorf("matrix outbound conversation id is required")
	}
	bodyText := strings.TrimSpace(message.Text)
	if bodyText == "" {
		return transport.OutboundSendResult{}, nil
	}
	t.mu.RLock()
	started := t.started
	t.mu.RUnlock()
	if !started {
		return transport.OutboundSendResult{}, fmt.Errorf("matrix transport is not running")
	}
	outbound := transport.OutboundMessage{
		ConversationID:    roomID,
		SenderID:          strings.TrimSpace(message.SenderID),
		Text:              bodyText,
		ThreadRootEventID: strings.TrimSpace(message.ThreadRootEventID),
	}
	return t.sendMessageNowWithResult(ctx, outbound)
}

func (t *Transport) SendTyping(ctx context.Context, conversationID string, typing bool) error {
	return t.SendTypingAs(ctx, conversationID, "", typing)
}

func (t *Transport) SendTypingAs(ctx context.Context, conversationID, senderID string, typing bool) error {
	roomID := strings.TrimSpace(conversationID)
	if roomID == "" {
		return fmt.Errorf("matrix typing conversation id is required")
	}
	userID := strings.TrimSpace(senderID)
	if userID != "" && !t.isManagedUser(userID) {
		userID = ""
	}
	if userID == "" {
		userID = strings.TrimSpace(t.resolveRoomManagedUser(roomID))
	}
	if userID == "" {
		return nil
	}
	t.setRoomManagedUser(roomID, userID)
	t.mu.RLock()
	started := t.started
	queue := t.typingQueue
	t.mu.RUnlock()
	if !started || queue == nil {
		return fmt.Errorf("matrix transport is not running")
	}
	item := queuedOutbound{
		kind:                 outboundKindTyping,
		typingConversationID: roomID,
		typingUserID:         userID,
		typing:               typing,
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case queue <- item:
		return nil
	default:
		if !typing {
			// Best effort to clear sticky indicators if the typing queue is saturated.
			return t.sendTypingNow(ctx, roomID, userID, false)
		}
		t.incrementDropped()
		return fmt.Errorf("matrix typing queue is full")
	}
}

func (t *Transport) sendMessageNow(ctx context.Context, message transport.OutboundMessage) error {
	_, err := t.sendMessageNowWithResult(ctx, message)
	return err
}

func (t *Transport) sendMessageNowWithResult(ctx context.Context, message transport.OutboundMessage) (transport.OutboundSendResult, error) {
	txnID := fmt.Sprintf("gopher-%d", time.Now().UTC().UnixNano())
	senderID := strings.TrimSpace(message.SenderID)
	if senderID == "" {
		senderID = strings.TrimSpace(t.resolveRoomManagedUser(message.ConversationID))
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s?access_token=%s",
		t.homeserverURL,
		url.PathEscape(message.ConversationID),
		url.PathEscape(txnID),
		url.QueryEscape(t.asToken),
	)
	if senderID != "" {
		endpoint += "&user_id=" + url.QueryEscape(senderID)
	}
	payload := outboundMessagePayload{
		MsgType: "m.text",
		Body:    message.Text,
	}
	if threadRoot := strings.TrimSpace(message.ThreadRootEventID); threadRoot != "" {
		payload.RelatesTo = &matrixRelatesTo{
			RelType:       "m.thread",
			EventID:       threadRoot,
			IsFallingBack: true,
			InReplyTo: &matrixInReplyTo{
				EventID: threadRoot,
			},
		}
	}
	if t.richTextEnabled {
		if formattedBody, ok := t.formatOutboundHTML(message.Text); ok {
			payload.Format = matrixMessageHTMLFormat
			payload.FormattedBody = formattedBody
		}
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return transport.OutboundSendResult{}, fmt.Errorf("marshal matrix outbound payload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(blob))
	if err != nil {
		return transport.OutboundSendResult{}, fmt.Errorf("build matrix outbound request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := t.httpClient.Do(request)
	if err != nil {
		return transport.OutboundSendResult{}, fmt.Errorf("send matrix outbound message: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return transport.OutboundSendResult{}, fmt.Errorf("matrix outbound status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	result := transport.OutboundSendResult{}
	if len(body) > 0 {
		var parsed matrixSendResponse
		if err := json.Unmarshal(body, &parsed); err == nil {
			result.EventID = strings.TrimSpace(parsed.EventID)
		}
	}
	return result, nil
}

func (t *Transport) formatOutboundHTML(body string) (string, bool) {
	if t == nil || t.formatter == nil {
		return "", false
	}
	return t.formatter.Format(body)
}

func (t *Transport) sendTypingNow(ctx context.Context, roomID, userID string, typing bool) error {
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/typing/%s?access_token=%s",
		t.homeserverURL,
		url.PathEscape(roomID),
		url.PathEscape(userID),
		url.QueryEscape(t.asToken),
	)
	endpoint += "&user_id=" + url.QueryEscape(userID)
	payload := map[string]any{
		"typing": typing,
	}
	if typing {
		payload["timeout"] = matrixTypingTimeoutMillis
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal matrix typing payload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(string(blob)))
	if err != nil {
		return fmt.Errorf("build matrix typing request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := t.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send matrix typing request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("matrix typing status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (t *Transport) setPresenceNow(ctx context.Context, state string) error {
	if !t.presenceEnabled {
		return nil
	}
	botUserID := strings.TrimSpace(t.botUserID)
	if botUserID == "" {
		return nil
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/presence/%s/status?access_token=%s",
		t.homeserverURL,
		url.PathEscape(botUserID),
		url.QueryEscape(t.asToken),
	)
	endpoint += "&user_id=" + url.QueryEscape(botUserID)

	payload := map[string]string{
		"presence": strings.TrimSpace(state),
	}
	if state == presenceStateOnline && strings.TrimSpace(t.presenceStatusMsg) != "" {
		payload["status_msg"] = t.presenceStatusMsg
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal matrix presence payload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(blob))
	if err != nil {
		return fmt.Errorf("build matrix presence request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := t.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send matrix presence request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("matrix presence status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (t *Transport) applyPresenceUpdate(parent context.Context, state string, alwaysLogFailure bool) {
	if !t.presenceEnabled {
		return
	}
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, presenceRequestTimeout)
	defer cancel()

	err := t.setPresenceNow(requestCtx, state)
	failureTransition, recovered := t.recordPresenceResult(state, err)
	if err != nil {
		if alwaysLogFailure || failureTransition {
			slog.Warn("matrix_transport: presence update failed", "state", state, "error", err)
		}
		return
	}
	t.maybeReplayDroppedOutbound()
	if recovered {
		slog.Info("matrix_transport: presence recovered", "state", state)
	}
}

func (t *Transport) runPresenceWorker(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(t.presenceInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.applyPresenceUpdate(ctx, presenceStateOnline, false)
		}
	}
}

func (t *Transport) runOutboundWorker(workerCtx context.Context, outboundQueue <-chan queuedOutbound, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-workerCtx.Done():
			return
		case item := <-outboundQueue:
			t.recordQueueDepth(len(outboundQueue))
			t.processOutbound(item)
		}
	}
}

func (t *Transport) runTypingWorker(workerCtx context.Context, typingQueue <-chan queuedOutbound, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-workerCtx.Done():
			return
		case item := <-typingQueue:
			t.processTyping(item)
		}
	}
}

func (t *Transport) processOutbound(item queuedOutbound) {
	for {
		var err error
		switch item.kind {
		case outboundKindMessage:
			err = t.sendMessageNow(t.workerCtx, item.message)
		case outboundKindTyping:
			err = t.sendTypingNow(t.workerCtx, item.typingConversationID, item.typingUserID, item.typing)
		default:
			return
		}
		if err == nil {
			t.recordOutboundSuccess()
			t.maybeReplayDroppedOutbound()
			if strings.TrimSpace(item.eventID) != "" {
				t.markEventState(item.eventID, deliveryStatusDelivered)
			}
			return
		}
		transient := isTransientSendError(err)
		if transient {
			t.incrementTransientError()
		} else {
			t.incrementPermanentError()
		}
		if transient && item.attempt < t.maxAttempts {
			item.attempt++
			t.incrementRetries()
			delay := retryDelay(item.attempt)
			timer := time.NewTimer(delay)
			select {
			case <-t.workerCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		t.incrementDropped()
		if transient && strings.TrimSpace(item.eventID) != "" {
			t.markEventState(item.eventID, deliveryStatusRetryable)
		}
		if transient {
			t.enqueueDroppedForReplay(item)
		}
		return
	}
}

func (t *Transport) processTyping(item queuedOutbound) {
	for {
		err := t.sendTypingNow(t.workerCtx, item.typingConversationID, item.typingUserID, item.typing)
		if err == nil {
			t.recordOutboundSuccess()
			t.maybeReplayDroppedOutbound()
			return
		}
		transient := isTransientSendError(err)
		if transient {
			t.incrementTransientError()
		} else {
			t.incrementPermanentError()
		}
		if transient && item.attempt < t.maxAttempts {
			item.attempt++
			t.incrementRetries()
			delay := retryDelay(item.attempt)
			timer := time.NewTimer(delay)
			select {
			case <-t.workerCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		t.incrementDropped()
		return
	}
}

func typingQueueCapacity(messageQueueCapacity int) int {
	if messageQueueCapacity < 8 {
		return 8
	}
	capacity := messageQueueCapacity / 4
	if capacity < 8 {
		return 8
	}
	return capacity
}

func droppedReplayCapacity(messageQueueCapacity int) int {
	if messageQueueCapacity <= 0 {
		return 64
	}
	capacity := messageQueueCapacity * 4
	if capacity < 64 {
		return 64
	}
	return capacity
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 300 * time.Millisecond
	maxDelay := 5 * time.Second
	exp := 1 << maxInt(0, attempt-1)
	delay := base * time.Duration(exp)
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func isTransientSendError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "timeout") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "connection reset") ||
		strings.Contains(text, "temporary") {
		return true
	}
	if strings.Contains(text, "status: 5") || strings.Contains(text, "status: 429") {
		return true
	}
	return false
}

func (t *Transport) shutdownWorker() {
	t.mu.Lock()
	cancel := t.workerCancel
	done := t.outboundDone
	typingDone := t.typingDone
	presenceDone := t.presenceDone
	t.workerCancel = nil
	t.workerCtx = nil
	t.outboundQueue = nil
	t.outboundDone = nil
	t.typingQueue = nil
	t.typingDone = nil
	t.presenceDone = nil
	t.started = false
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	if typingDone != nil {
		select {
		case <-typingDone:
		case <-time.After(2 * time.Second):
		}
	}
	if presenceDone != nil {
		select {
		case <-presenceDone:
		case <-time.After(2 * time.Second):
		}
	}
}

func (t *Transport) handleAppservicePing(writer http.ResponseWriter, request *http.Request) {
	if !t.isAuthorized(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	writer.Header().Set("content-type", "application/json")
	_, _ = writer.Write([]byte(`{}`))
}

func (t *Transport) handleHealth(writer http.ResponseWriter, request *http.Request) {
	_ = request
	stats := t.snapshotMetrics()
	writer.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]interface{}{
		"ok":                       true,
		"listen_addr":              t.listenAddr,
		"queue_depth":              stats.QueueDepth,
		"last_inbound_txn_at":      formatTime(stats.LastInboundTxnAt),
		"last_outbound_success_at": formatTime(stats.LastOutboundSuccessAt),
	})
}

func (t *Transport) handleMetrics(writer http.ResponseWriter, request *http.Request) {
	_ = request
	stats := t.snapshotMetrics()
	writer.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]interface{}{
		"last_inbound_txn_at":                formatTime(stats.LastInboundTxnAt),
		"last_outbound_success_at":           formatTime(stats.LastOutboundSuccessAt),
		"presence_enabled":                   stats.PresenceEnabled,
		"presence_state":                     stats.PresenceState,
		"presence_last_success_at":           formatTime(stats.PresenceLastSuccessAt),
		"presence_failures":                  stats.PresenceFailures,
		"presence_last_error":                stats.PresenceLastError,
		"queue_depth":                        stats.QueueDepth,
		"outbound_retries":                   stats.OutboundRetries,
		"outbound_dropped":                   stats.OutboundDropped,
		"outbound_replay_pending":            stats.OutboundReplayPending,
		"outbound_transient_errors":          stats.OutboundTransientErrs,
		"outbound_permanent_errors":          stats.OutboundPermanentErrs,
		"duplicate_txn_seen":                 stats.DuplicateTxnSeen,
		"duplicate_events_skipped":           stats.DuplicateEventsSkipped,
		"replay_events_processed":            stats.ReplayEventsProcessed,
		"trace_rooms_created_total":          stats.TraceRoomsCreated,
		"trace_publish_success_total":        stats.TracePublishSuccess,
		"trace_publish_failure_total":        stats.TracePublishFailure,
		"trace_events_ignored_inbound_total": stats.TraceInboundIgnored,
		"inbound_failures":                   t.inboundFailures.Load(),
	})
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
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
	t.recordTransactionSeen(txnID)

	body := transactionBody{}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(writer, "invalid payload", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	t.cleanupState(now)
	t.recordInboundTxn(now)
	hadFailure := false

	for _, event := range body.Events {
		t.trackManagedRoomMembership(event)
		t.trackRoomName(event)
	}

	for _, event := range body.Events {
		if t.shouldSkipEvent(event) {
			continue
		}
		eventCtx, cancelEvent := context.WithTimeout(context.Background(), inboundEventTimeout)
		if inviteeUserID, ok := t.invitedManagedUser(event); ok {
			if err := t.joinRoomAs(eventCtx, event.RoomID, inviteeUserID); err != nil {
				slog.Warn("matrix_transport: inbound invite handling failed", "room_id", event.RoomID, "event_id", strings.TrimSpace(event.EventID), "error", err)
				t.markEventState(event.EventID, deliveryStatusDelivered)
				cancelEvent()
				continue
			}
			t.markEventState(event.EventID, deliveryStatusDelivered)
			cancelEvent()
			continue
		}
		inbound, ok := t.toInboundMessage(event)
		if !ok {
			cancelEvent()
			continue
		}
		handler := t.getHandler()
		if handler == nil {
			t.markEventState(event.EventID, deliveryStatusRetryable)
			hadFailure = true
			cancelEvent()
			continue
		}
		if err := handler(eventCtx, inbound); err != nil {
			t.inboundFailures.Add(1)
			slog.Error("matrix_transport: inbound handler failed", "event_id", strings.TrimSpace(event.EventID), "room_id", inbound.ConversationID, "sender", inbound.SenderID, "error", err)
			t.markEventState(event.EventID, deliveryStatusRetryable)
			hadFailure = true
			cancelEvent()
			continue
		}
		t.markEventState(event.EventID, deliveryStatusDelivered)
		cancelEvent()
	}
	if hadFailure {
		http.Error(writer, "temporary failure", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("content-type", "application/json")
	_, _ = writer.Write([]byte(`{}`))
}

func (t *Transport) shouldSkipEvent(event matrixEvent) bool {
	eventID := strings.TrimSpace(event.EventID)
	if eventID == "" {
		return false
	}
	state, ok := t.getEventState(eventID)
	if !ok {
		return false
	}
	if state.Status == deliveryStatusDelivered {
		t.incrementDuplicateEvents()
		return true
	}
	if state.Status == deliveryStatusRetryable {
		t.incrementReplayEvents()
		return false
	}
	return false
}

func (t *Transport) invitedManagedUser(event matrixEvent) (string, bool) {
	if event.Type != "m.room.member" {
		return "", false
	}
	if strings.TrimSpace(event.RoomID) == "" {
		return "", false
	}
	membership, _ := event.Content["membership"].(string)
	if strings.TrimSpace(membership) != "invite" {
		return "", false
	}
	stateKey := strings.TrimSpace(event.StateKey)
	if stateKey != "" {
		if t.isManagedUser(stateKey) {
			return stateKey, true
		}
		return "", false
	}
	if strings.TrimSpace(t.botUserID) != "" {
		return strings.TrimSpace(t.botUserID), true
	}
	if len(t.managedUserIDs) == 1 {
		return t.managedUserIDs[0], true
	}
	return "", false
}

func (t *Transport) joinRoomAs(ctx context.Context, roomID string, userID string) error {
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/join?access_token=%s",
		t.homeserverURL,
		url.PathEscape(strings.TrimSpace(roomID)),
		url.QueryEscape(t.asToken),
	)
	if strings.TrimSpace(userID) != "" {
		endpoint += "&user_id=" + url.QueryEscape(strings.TrimSpace(userID))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{}`))
	if err != nil {
		return fmt.Errorf("build matrix join request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := t.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send matrix join request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("matrix join status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (t *Transport) ensureBotUser(ctx context.Context) error {
	if len(t.managedUserIDs) == 0 {
		if strings.TrimSpace(t.botUserID) == "" {
			return nil
		}
		return t.ensureManagedUser(ctx, strings.TrimSpace(t.botUserID))
	}
	for _, userID := range t.managedUserIDs {
		if err := t.ensureManagedUser(ctx, userID); err != nil {
			return err
		}
	}
	return nil
}

func (t *Transport) ensureManagedUser(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	localPart := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(userID, ":", 2)[0], "@"))
	if localPart == "" {
		return fmt.Errorf("invalid bot user id: %s", userID)
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/register?access_token=%s",
		t.homeserverURL,
		url.QueryEscape(t.asToken),
	)
	payload := map[string]string{
		"type":     "m.login.application_service",
		"username": localPart,
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal bot user register payload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(blob)))
	if err != nil {
		return fmt.Errorf("build bot user register request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := t.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send bot user register request: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if (response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusConflict) &&
		strings.Contains(string(body), "M_USER_IN_USE") {
		if err := t.ensureManagedUserProfile(ctx, userID); err != nil {
			slog.Warn("matrix_transport: managed user profile sync failed", "user_id", userID, "error", err)
		}
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("matrix bot register status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := t.ensureManagedUserProfile(ctx, userID); err != nil {
		slog.Warn("matrix_transport: managed user profile sync failed", "user_id", userID, "error", err)
	}
	return nil
}

func (t *Transport) toInboundMessage(event matrixEvent) (transport.InboundMessage, bool) {
	if event.Type != "m.room.message" {
		return transport.InboundMessage{}, false
	}
	if strings.TrimSpace(event.RoomID) == "" || strings.TrimSpace(event.Sender) == "" {
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
		ConversationID:   event.RoomID,
		ConversationName: t.resolveRoomName(event.RoomID),
		SenderID:         event.Sender,
		SenderManaged:    t.isManagedUser(event.Sender),
		RecipientID:      t.resolveRoomManagedUser(event.RoomID),
		EventID:          strings.TrimSpace(event.EventID),
		Text:             body,
	}, true
}

func normalizeManagedUserIDs(primary string, managed []string) (string, []string, map[string]struct{}) {
	set := map[string]struct{}{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		set[value] = struct{}{}
	}
	add(primary)
	for _, userID := range managed {
		add(userID)
	}
	out := make([]string, 0, len(set))
	for userID := range set {
		out = append(out, userID)
	}
	sort.Strings(out)
	if strings.TrimSpace(primary) == "" && len(out) == 1 {
		primary = out[0]
	}
	return strings.TrimSpace(primary), out, set
}

func (t *Transport) isManagedUser(userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.managedUsers[userID]
	return ok
}

func (t *Transport) trackManagedRoomMembership(event matrixEvent) {
	if event.Type != "m.room.member" {
		return
	}
	roomID := strings.TrimSpace(event.RoomID)
	userID := strings.TrimSpace(event.StateKey)
	if roomID == "" || userID == "" {
		return
	}
	if !t.isManagedUser(userID) {
		return
	}
	membership, _ := event.Content["membership"].(string)
	membership = strings.TrimSpace(membership)
	switch membership {
	case "invite", "join":
		t.setRoomManagedUser(roomID, userID)
	case "leave", "ban":
		t.clearRoomManagedUser(roomID, userID)
	}
}

func (t *Transport) trackRoomName(event matrixEvent) {
	roomID := strings.TrimSpace(event.RoomID)
	if roomID == "" {
		return
	}
	switch strings.TrimSpace(event.Type) {
	case "m.room.name":
		name, _ := event.Content["name"].(string)
		t.setRoomName(roomID, name)
	case "m.room.canonical_alias":
		if t.resolveRoomName(roomID) != "" {
			return
		}
		alias, _ := event.Content["alias"].(string)
		t.setRoomName(roomID, alias)
	}
}

func (t *Transport) setRoomName(roomID, name string) {
	roomID = strings.TrimSpace(roomID)
	name = strings.TrimSpace(name)
	if roomID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.roomNames == nil {
		t.roomNames = map[string]string{}
	}
	if name == "" {
		delete(t.roomNames, roomID)
		return
	}
	t.roomNames[roomID] = name
}

func (t *Transport) resolveRoomName(roomID string) string {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return strings.TrimSpace(t.roomNames[roomID])
}

func (t *Transport) setRoomManagedUser(roomID, userID string) {
	roomID = strings.TrimSpace(roomID)
	userID = strings.TrimSpace(userID)
	if roomID == "" || userID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.roomManagedUsers == nil {
		t.roomManagedUsers = map[string]map[string]struct{}{}
	}
	users, ok := t.roomManagedUsers[roomID]
	if !ok {
		users = map[string]struct{}{}
		t.roomManagedUsers[roomID] = users
	}
	users[userID] = struct{}{}
}

func (t *Transport) clearRoomManagedUser(roomID, userID string) {
	roomID = strings.TrimSpace(roomID)
	userID = strings.TrimSpace(userID)
	if roomID == "" || userID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	users, ok := t.roomManagedUsers[roomID]
	if !ok {
		return
	}
	delete(users, userID)
	if len(users) == 0 {
		delete(t.roomManagedUsers, roomID)
	}
}

func (t *Transport) resolveRoomManagedUser(roomID string) string {
	roomID = strings.TrimSpace(roomID)
	t.mu.RLock()
	defer t.mu.RUnlock()

	if roomID != "" {
		users, ok := t.roomManagedUsers[roomID]
		if ok && len(users) > 0 {
			if strings.TrimSpace(t.botUserID) != "" {
				if _, exists := users[t.botUserID]; exists {
					return t.botUserID
				}
			}
			if len(users) == 1 {
				for userID := range users {
					return userID
				}
			}
			choices := make([]string, 0, len(users))
			for userID := range users {
				choices = append(choices, userID)
			}
			sort.Strings(choices)
			return choices[0]
		}
	}
	if strings.TrimSpace(t.botUserID) != "" {
		return strings.TrimSpace(t.botUserID)
	}
	if len(t.managedUserIDs) == 1 {
		return t.managedUserIDs[0]
	}
	return ""
}

func (t *Transport) ManagedUsersForConversation(conversationID string) []string {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil
	}
	t.mu.RLock()
	users, ok := t.roomManagedUsers[conversationID]
	if !ok || len(users) == 0 {
		t.mu.RUnlock()
		return nil
	}
	out := make([]string, 0, len(users))
	for userID := range users {
		out = append(out, userID)
	}
	t.mu.RUnlock()
	sort.Strings(out)
	return out
}

func (t *Transport) isAuthorized(request *http.Request) bool {
	token := strings.TrimSpace(request.URL.Query().Get("access_token"))
	if token != "" {
		return token == t.hsToken
	}
	auth := strings.TrimSpace(request.Header.Get("authorization"))
	if auth == "" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return false
	}
	token = strings.TrimSpace(auth[len("bearer "):])
	return token != "" && token == t.hsToken
}

func (t *Transport) recordTransactionSeen(txnID string) {
	if strings.TrimSpace(txnID) == "" {
		return
	}
	now := time.Now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.seenTxn[txnID]; exists {
		t.stats.mu.Lock()
		t.stats.DuplicateTxnSeen++
		t.stats.mu.Unlock()
	}
	t.seenTxn[txnID] = now
}

func (t *Transport) cleanupState(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for txnID, seenAt := range t.seenTxn {
		if now.Sub(seenAt) > t.dedupeTTL {
			delete(t.seenTxn, txnID)
		}
	}
	for eventID, state := range t.events {
		if now.Sub(state.UpdatedAt) > t.dedupeTTL {
			delete(t.events, eventID)
		}
	}
}

func (t *Transport) markEventState(eventID string, status deliveryStatus) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events[eventID] = eventDeliveryState{
		Status:    status,
		UpdatedAt: time.Now().UTC(),
	}
}

func (t *Transport) getEventState(eventID string) (eventDeliveryState, bool) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return eventDeliveryState{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	state, ok := t.events[eventID]
	return state, ok
}

func (t *Transport) recordInboundTxn(value time.Time) {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.LastInboundTxnAt = value
}

func (t *Transport) recordOutboundSuccess() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.LastOutboundSuccessAt = time.Now().UTC()
}

func (t *Transport) recordPresenceResult(state string, err error) (failureTransition bool, recovered bool) {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()

	t.stats.PresenceEnabled = t.presenceEnabled
	hadError := strings.TrimSpace(t.stats.PresenceLastError) != ""
	if err != nil {
		t.stats.PresenceFailures++
		t.stats.PresenceLastError = err.Error()
		t.stats.PresenceState = presenceStateUnknown
		return !hadError, false
	}
	t.stats.PresenceLastSuccessAt = time.Now().UTC()
	t.stats.PresenceState = state
	t.stats.PresenceLastError = ""
	return false, hadError
}

func (t *Transport) incrementRetries() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.OutboundRetries++
}

func (t *Transport) incrementDropped() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.OutboundDropped++
}

func (t *Transport) recordReplayPending(pending int) {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.OutboundReplayPending = pending
}

func (t *Transport) incrementTransientError() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.OutboundTransientErrs++
}

func (t *Transport) incrementPermanentError() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.OutboundPermanentErrs++
}

func (t *Transport) recordQueueDepth(depth int) {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.QueueDepth = depth
}

func (t *Transport) incrementDuplicateEvents() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.DuplicateEventsSkipped++
}

func (t *Transport) incrementReplayEvents() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.ReplayEventsProcessed++
}

func (t *Transport) RecordTraceRoomCreated() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.TraceRoomsCreated++
}

func (t *Transport) RecordTracePublishSuccess() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.TracePublishSuccess++
}

func (t *Transport) RecordTracePublishFailure() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.TracePublishFailure++
}

func (t *Transport) RecordTraceInboundIgnored() {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.TraceInboundIgnored++
}

func (t *Transport) snapshotMetrics() matrixStatsSnapshot {
	t.stats.mu.RLock()
	defer t.stats.mu.RUnlock()
	return matrixStatsSnapshot{
		LastInboundTxnAt:       t.stats.LastInboundTxnAt,
		LastOutboundSuccessAt:  t.stats.LastOutboundSuccessAt,
		PresenceLastSuccessAt:  t.stats.PresenceLastSuccessAt,
		QueueDepth:             t.stats.QueueDepth,
		OutboundRetries:        t.stats.OutboundRetries,
		OutboundDropped:        t.stats.OutboundDropped,
		OutboundReplayPending:  t.stats.OutboundReplayPending,
		OutboundTransientErrs:  t.stats.OutboundTransientErrs,
		OutboundPermanentErrs:  t.stats.OutboundPermanentErrs,
		DuplicateTxnSeen:       t.stats.DuplicateTxnSeen,
		DuplicateEventsSkipped: t.stats.DuplicateEventsSkipped,
		ReplayEventsProcessed:  t.stats.ReplayEventsProcessed,
		TraceRoomsCreated:      t.stats.TraceRoomsCreated,
		TracePublishSuccess:    t.stats.TracePublishSuccess,
		TracePublishFailure:    t.stats.TracePublishFailure,
		TraceInboundIgnored:    t.stats.TraceInboundIgnored,
		PresenceEnabled:        t.stats.PresenceEnabled,
		PresenceState:          t.stats.PresenceState,
		PresenceFailures:       t.stats.PresenceFailures,
		PresenceLastError:      t.stats.PresenceLastError,
	}
}

func (t *Transport) getHandler() transport.InboundHandler {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.handler
}

func (t *Transport) enqueueDroppedForReplay(item queuedOutbound) {
	if item.kind != outboundKindMessage {
		return
	}
	item.attempt = 0
	t.droppedMu.Lock()
	if t.droppedCapacity <= 0 || len(t.droppedReplay) >= t.droppedCapacity {
		pending := len(t.droppedReplay)
		t.droppedMu.Unlock()
		t.recordReplayPending(pending)
		return
	}
	t.droppedReplay = append(t.droppedReplay, item)
	pending := len(t.droppedReplay)
	t.droppedMu.Unlock()
	t.recordReplayPending(pending)
}

func (t *Transport) dequeueDroppedForReplay() (queuedOutbound, bool) {
	t.droppedMu.Lock()
	if len(t.droppedReplay) == 0 {
		t.droppedMu.Unlock()
		return queuedOutbound{}, false
	}
	item := t.droppedReplay[0]
	t.droppedReplay = t.droppedReplay[1:]
	pending := len(t.droppedReplay)
	t.droppedMu.Unlock()
	t.recordReplayPending(pending)
	item.attempt = 0
	return item, true
}

func (t *Transport) requeueDroppedFront(item queuedOutbound) {
	if item.kind != outboundKindMessage {
		return
	}
	item.attempt = 0
	t.droppedMu.Lock()
	if t.droppedCapacity <= 0 {
		pending := len(t.droppedReplay)
		t.droppedMu.Unlock()
		t.recordReplayPending(pending)
		return
	}
	if len(t.droppedReplay) >= t.droppedCapacity {
		t.droppedReplay = t.droppedReplay[:t.droppedCapacity-1]
	}
	t.droppedReplay = append(t.droppedReplay, queuedOutbound{})
	copy(t.droppedReplay[1:], t.droppedReplay[:len(t.droppedReplay)-1])
	t.droppedReplay[0] = item
	pending := len(t.droppedReplay)
	t.droppedMu.Unlock()
	t.recordReplayPending(pending)
}

func (t *Transport) maybeReplayDroppedOutbound() {
	item, ok := t.dequeueDroppedForReplay()
	if !ok {
		return
	}
	t.mu.RLock()
	started := t.started
	queue := t.outboundQueue
	t.mu.RUnlock()
	if !started || queue == nil {
		t.requeueDroppedFront(item)
		return
	}
	select {
	case queue <- item:
		t.recordQueueDepth(len(queue))
	default:
		t.requeueDroppedFront(item)
	}
}
