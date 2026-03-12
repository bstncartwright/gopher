package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ctxbundle "github.com/bstncartwright/gopher/pkg/context"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

type DMPipelineOptions struct {
	Manager             sessionrt.SessionManager
	Transport           transport.Transport
	EventStore          sessionrt.EventStore
	AgentID             sessionrt.ActorID
	AgentByRecipient    map[string]sessionrt.ActorID
	RecipientByAgent    map[sessionrt.ActorID]string
	Conversations       *ConversationSessionMap
	Bindings            ConversationBindingStore
	SessionLifecycle    *sessionrt.DailyResetPolicy
	Now                 func() time.Time
	TracePublisher      TracePublisher
	TraceProvisioner    TraceConversationProvisioner
	AttachmentResolver  func(conversationID string, agentID sessionrt.ActorID, event sessionrt.Event) []transport.OutboundAttachment
	AttachmentWorkspace func(agentID sessionrt.ActorID) string
	ModelPolicyCommand  ModelPolicyCommandHandler
}

type ModelPolicyCommandRequest struct {
	AgentID              sessionrt.ActorID
	RequestedModelPolicy string
}

type ModelPolicyCommandResult struct {
	CurrentModelPolicy  string
	PreviousModelPolicy string
	Updated             bool
	RestartScheduled    bool
}

type ModelPolicyCommandHandler func(context.Context, ModelPolicyCommandRequest) (ModelPolicyCommandResult, error)

type conversationRoute struct {
	AgentID     sessionrt.ActorID
	RecipientID string
	Mode        ConversationMode
}

type HeartbeatTarget struct {
	ConversationID string
	SessionID      sessionrt.SessionID
}

type TraceConversationRequest struct {
	ConversationID   string
	ConversationName string
	SessionID        sessionrt.SessionID
	AgentID          sessionrt.ActorID
	SenderID         string
	RecipientID      string
	TriggerEventID   string
}

type TraceConversationBinding struct {
	ConversationID   string
	ConversationName string
	Mode             string
	Render           string
}

type TraceConversationProvisioner interface {
	CreateTraceConversation(ctx context.Context, req TraceConversationRequest) (TraceConversationBinding, error)
}

type DMPipeline struct {
	manager       sessionrt.SessionManager
	transport     transport.Transport
	eventStore    sessionrt.EventStore
	agentID       sessionrt.ActorID
	agentByRecip  map[string]sessionrt.ActorID
	recipByAgent  map[sessionrt.ActorID]string
	conversations *ConversationSessionMap
	bindings      ConversationBindingStore
	now           func() time.Time
	lifecycle     sessionrt.DailyResetPolicy

	setupMu             sync.Mutex
	subscribedMu        sync.Mutex
	subscribed          map[sessionrt.SessionID]struct{}
	fallbackMu          sync.Mutex
	lastFallback        map[string]time.Time
	errorFallbackMu     sync.Mutex
	errorFallbacks      map[string]pendingErrorFallback
	errorFallbackSeq    uint64
	errorRecoveryMu     sync.Mutex
	errorRecoveryCount  map[string]int
	processingMu        sync.Mutex
	processing          map[string]int
	draftMu             sync.Mutex
	draftSeq            uint64
	draftState          map[string]draftStreamState
	typingMu            sync.Mutex
	typingCancel        map[string]context.CancelFunc
	attachmentMu        sync.Mutex
	pendingFiles        map[string][]transport.OutboundAttachment
	messageDedupeMu     sync.Mutex
	pendingMessageText  map[string][]string
	routeMu             sync.RWMutex
	routes              map[string]conversationRoute
	traceRouteMu        sync.RWMutex
	traceByDM           map[string]string
	dmByTrace           map[string]string
	traceSetupMu        sync.Mutex
	traceSetup          map[string]traceProvisionState
	heartbeatMu         sync.Mutex
	heartbeats          map[string]heartbeatState
	tracePublisher      TracePublisher
	traceProvisioner    TraceConversationProvisioner
	attachmentResolver  func(conversationID string, agentID sessionrt.ActorID, event sessionrt.Event) []transport.OutboundAttachment
	attachmentWorkspace func(agentID sessionrt.ActorID) string
	modelPolicyCommand  ModelPolicyCommandHandler

	laneMu             sync.Mutex
	laneByConversation map[string]*sync.Mutex
	outboundLaneMu     sync.Mutex
	outboundLaneByConv map[string]*sync.Mutex
	outboundPending    int64
}

type heartbeatState struct {
	pending []heartbeatPending
}

type heartbeatPending struct {
	AckMaxChars       int
	SessionID         sessionrt.SessionID
	PreviousUpdatedAt time.Time
	DispatchedAt      time.Time
}

type heartbeatConsumeResult struct {
	Suppress    bool
	Normalized  string
	IsHeartbeat bool
}

type traceProvisionState struct {
	InFlight      bool
	LastFailureAt time.Time
}

type pendingErrorFallback struct {
	seq    uint64
	rawErr string
	timer  *time.Timer
}

type draftStreamState struct {
	DraftID      int64
	Text         string
	ThinkingText string
	ToolProgress []string
	LastSentAt   time.Time
	LastSentN    int
	Disabled     bool
}

type dmErrorClass int

const (
	dmErrorClassUnknown dmErrorClass = iota
	dmErrorClassRateLimit
	dmErrorClassTimeout
	dmErrorClassConnection
	dmErrorClassProviderUnavailable
	dmErrorClassAuth
	dmErrorClassForbidden
	dmErrorClassModelNotFound
	dmErrorClassValidation
)

const (
	dmRateLimitFallbackReply = "I hit a temporary rate limit while processing that. Please try again in a moment."
	dmErrorFallbackReply     = "I ran into an upstream error while processing that message. Please try again."
	dmContextClearedReply    = "Context cleared. Started a fresh session for this chat."
	dmFallbackMinInterval    = 5 * time.Second
	dmErrorFallbackDelay     = 750 * time.Millisecond
	dmFallbackDetailMaxChars = 120
	dmErrorRecoveryMaxTries  = 1
	dmTypingTimeout          = 3 * time.Second
	dmTypingKeepaliveDefault = 5 * time.Second
	dmDraftMaxChars          = 4096
	dmDraftMinSendChars      = 48
	dmDraftMinSendInterval   = 900 * time.Millisecond
	dmDraftToolPrefix        = "Working:"
	dmDraftThinkingPrefix    = "Thinking:"
	heartbeatOKToken         = "HEARTBEAT_OK"
	noReplyToken             = "NO_REPLY"
	heartbeatAckDefaultChars = 300
	heartbeatDedupeWindow    = 24 * time.Hour
	heartbeatRestoreGrace    = 2 * time.Second
	traceProvisionBackoff    = 30 * time.Second
	dmModelCommandDisabled   = "Model switching is not available on this gateway."
	dmModelCommandUsage      = "Usage: /model <provider:model> (or /model status)"
	dmThinkingCommandUsage   = "Usage: /thinking <on|off|status>"
)

var dmTypingKeepaliveInterval = dmTypingKeepaliveDefault

const dmSummarizeCommandPrompt = "Summarize this conversation so far in 8 bullets max. Include: objective, key decisions, important constraints, open questions, and next steps."

var (
	fallbackAuthBearerPattern     = regexp.MustCompile(`(?i)\bauthorization\b\s*[:=]\s*bearer\s+[^\s,;]+`)
	fallbackBearerPattern         = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`)
	fallbackSensitiveValuePattern = regexp.MustCompile(`(?i)\b(authorization|bearer|api[-_ ]?key|token|secret|password)\b\s*[:=]?\s*([^\s,;]+)`)
	fallbackLongTokenPattern      = regexp.MustCompile(`\b[A-Za-z0-9_-]{24,}\b`)
	heartbeatHTMLTagPattern       = regexp.MustCompile(`<[^>]*>`)
	heartbeatTokenSuffixPattern   = regexp.MustCompile(regexp.QuoteMeta(heartbeatOKToken) + `[^\w]{0,4}$`)
	dmToolEmojiFallback           = "🛠️"
)

type typingTransport interface {
	SendTyping(ctx context.Context, conversationID string, typing bool) error
}

type typingSenderTransport interface {
	SendTypingAs(ctx context.Context, conversationID, senderID string, typing bool) error
}

type draftStreamingTransport interface {
	SendMessageDraft(ctx context.Context, conversationID string, draftID int64, text string) error
}

type managedConversationUsersReader interface {
	ManagedUsersForConversation(conversationID string) []string
}

type sessionRecordReadWriter interface {
	GetSessionRecord(ctx context.Context, sessionID sessionrt.SessionID) (sessionrt.SessionRecord, error)
	UpsertSessionRecord(ctx context.Context, record sessionrt.SessionRecord) error
}

func NewDMPipeline(opts DMPipelineOptions) (*DMPipeline, error) {
	if opts.Manager == nil {
		slog.Error("dm_pipeline: session manager is required")
		return nil, fmt.Errorf("session manager is required")
	}
	if opts.Transport == nil {
		slog.Error("dm_pipeline: transport is required")
		return nil, fmt.Errorf("transport is required")
	}
	if strings.TrimSpace(string(opts.AgentID)) == "" {
		slog.Error("dm_pipeline: agent id is required")
		return nil, fmt.Errorf("agent id is required")
	}
	conversations := opts.Conversations
	if conversations == nil {
		conversations = NewConversationSessionMap()
	}
	bindings := opts.Bindings
	if bindings == nil {
		bindings = NewInMemoryConversationBindingStore()
	}
	agentByRecip := normalizeRecipientAgents(opts.AgentByRecipient)
	recipByAgent := normalizeAgentRecipients(opts.RecipientByAgent)
	for recipientID, actorID := range agentByRecip {
		if _, exists := recipByAgent[actorID]; !exists {
			recipByAgent[actorID] = recipientID
		}
	}
	slog.Info("dm_pipeline: creating pipeline",
		"agent_id", opts.AgentID,
		"agents_by_recipient_count", len(agentByRecip),
	)
	pipeline := &DMPipeline{
		manager:             opts.Manager,
		transport:           opts.Transport,
		eventStore:          opts.EventStore,
		agentID:             opts.AgentID,
		agentByRecip:        agentByRecip,
		recipByAgent:        recipByAgent,
		conversations:       conversations,
		bindings:            bindings,
		now:                 opts.Now,
		subscribed:          map[sessionrt.SessionID]struct{}{},
		lastFallback:        map[string]time.Time{},
		errorFallbacks:      map[string]pendingErrorFallback{},
		errorRecoveryCount:  map[string]int{},
		processing:          map[string]int{},
		draftState:          map[string]draftStreamState{},
		typingCancel:        map[string]context.CancelFunc{},
		pendingFiles:        map[string][]transport.OutboundAttachment{},
		pendingMessageText:  map[string][]string{},
		routes:              map[string]conversationRoute{},
		traceByDM:           map[string]string{},
		dmByTrace:           map[string]string{},
		traceSetup:          map[string]traceProvisionState{},
		heartbeats:          map[string]heartbeatState{},
		tracePublisher:      opts.TracePublisher,
		traceProvisioner:    opts.TraceProvisioner,
		attachmentResolver:  opts.AttachmentResolver,
		attachmentWorkspace: opts.AttachmentWorkspace,
		modelPolicyCommand:  opts.ModelPolicyCommand,
		laneByConversation:  map[string]*sync.Mutex{},
		outboundLaneByConv:  map[string]*sync.Mutex{},
	}
	if pipeline.now == nil {
		pipeline.now = time.Now
	}
	if opts.SessionLifecycle != nil {
		pipeline.lifecycle = *opts.SessionLifecycle
	} else {
		pipeline.lifecycle = sessionrt.DefaultDailyResetPolicy()
	}
	existingBindings := bindings.List()
	for _, binding := range existingBindings {
		pipeline.conversations.Set(binding.ConversationID, binding.SessionID)
		pipeline.setConversationRoute(binding.ConversationID, binding.AgentID, binding.RecipientID, binding.Mode)
		pipeline.setTraceConversationRoute(binding.ConversationID, binding.TraceConversationID)
	}
	pipeline.restoreBindingSubscriptions(existingBindings)
	pipeline.transport.SetInboundHandler(pipeline.HandleInbound)
	slog.Info("dm_pipeline: pipeline created", "agent_id", opts.AgentID)
	return pipeline, nil
}

func (p *DMPipeline) restoreBindingSubscriptions(bindings []ConversationBinding) {
	for _, binding := range bindings {
		conversationID := strings.TrimSpace(binding.ConversationID)
		sessionID := sessionrt.SessionID(strings.TrimSpace(string(binding.SessionID)))
		if conversationID == "" || strings.TrimSpace(string(sessionID)) == "" {
			continue
		}
		if err := p.ensureSubscription(conversationID, sessionID); err != nil {
			slog.Warn(
				"dm_pipeline: failed to restore existing conversation subscription",
				"conversation_id", conversationID,
				"session_id", sessionID,
				"error", err,
			)
		}
	}
}

func (p *DMPipeline) HandleInbound(ctx context.Context, inbound transport.InboundMessage) error {
	conversationID := strings.TrimSpace(inbound.ConversationID)
	if conversationID == "" {
		return nil
	}
	return p.withConversationLane(conversationID, func() error {
		return p.handleInboundUnlocked(ctx, inbound)
	})
}

func (p *DMPipeline) handleInboundUnlocked(ctx context.Context, inbound transport.InboundMessage) error {
	conversationID := strings.TrimSpace(inbound.ConversationID)
	if conversationID == "" {
		return nil
	}
	if p.isTraceConversation(conversationID) {
		p.recordTraceInboundIgnored()
		return nil
	}
	slog.Debug("dm_pipeline: handling inbound",
		"conversation_id", conversationID,
		"sender_id", inbound.SenderID,
		"recipient_id", inbound.RecipientID,
		"sender_managed", inbound.SenderManaged,
	)
	if strings.TrimSpace(inbound.Text) == "" && len(inbound.Attachments) == 0 {
		return nil
	}
	if inbound.SenderManaged {
		mode := p.conversationModeFor(conversationID)
		if mode != ConversationModeDelegation {
			slog.Debug("dm_pipeline: skipping managed sender - not delegation mode",
				"conversation_id", conversationID,
				"mode", mode,
			)
			return nil
		}
		if existingSessionID, ok := p.conversations.Get(conversationID); !ok || !p.isSessionActive(ctx, existingSessionID) {
			slog.Debug("dm_pipeline: skipping managed sender - no active session",
				"conversation_id", conversationID,
			)
			return nil
		}
	}

	agentID, recipientID := p.routeForInbound(inbound.RecipientID)
	if handled, err := p.handleInboundCommand(ctx, inbound, agentID, recipientID); handled || err != nil {
		return err
	}

	sessionID, err := p.resolveConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID)
	if err != nil {
		slog.Error("dm_pipeline: failed to resolve session",
			"conversation_id", conversationID,
			"sender_id", inbound.SenderID,
			"error", err,
		)
		return err
	}
	p.ensureTraceConversation(ctx, TraceConversationRequest{
		ConversationID:   conversationID,
		ConversationName: inbound.ConversationName,
		SessionID:        sessionID,
		AgentID:          agentID,
		SenderID:         inbound.SenderID,
		RecipientID:      recipientID,
		TriggerEventID:   inbound.EventID,
	})
	p.startProcessing(conversationID)
	attachments := p.stageInboundAttachments(agentID, inbound.Attachments)

	from := externalActorID(inbound.SenderID)
	if inbound.SenderManaged {
		if managedActor, ok := p.actorForManagedSender(inbound.SenderID); ok {
			from = managedActor
		} else {
			p.finishProcessing(conversationID)
			return nil
		}
	}

	// Appservice transactions should be acknowledged quickly; dispatch session work async.
	p.dispatchInboundEvent(sessionrt.Event{
		SessionID: sessionID,
		From:      from,
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:        sessionrt.RoleUser,
			Content:     inbound.Text,
			Attachments: sessionAttachmentsFromInbound(attachments),
		},
	}, conversationID, inbound.SenderID, inbound.EventID)
	return nil
}

func sessionAttachmentsFromInbound(in []transport.InboundAttachment) []sessionrt.Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]sessionrt.Attachment, 0, len(in))
	for _, attachment := range in {
		out = append(out, sessionrt.Attachment{
			Path:     strings.TrimSpace(attachment.Path),
			Name:     strings.TrimSpace(attachment.Name),
			MIMEType: strings.TrimSpace(attachment.MIMEType),
			Text:     attachment.Text,
			Data:     append([]byte(nil), attachment.Data...),
		})
	}
	return out
}

func (p *DMPipeline) stageInboundAttachments(agentID sessionrt.ActorID, in []transport.InboundAttachment) []transport.InboundAttachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]transport.InboundAttachment, 0, len(in))
	workspace := ""
	if p != nil && p.attachmentWorkspace != nil {
		workspace = filepath.Clean(strings.TrimSpace(p.attachmentWorkspace(agentID)))
	}
	now := time.Now()
	if p != nil && p.now != nil {
		now = p.now()
	}
	for idx, attachment := range in {
		staged := transport.InboundAttachment{
			Path:     strings.TrimSpace(attachment.Path),
			Name:     strings.TrimSpace(attachment.Name),
			MIMEType: strings.TrimSpace(attachment.MIMEType),
			Text:     attachment.Text,
			Data:     append([]byte(nil), attachment.Data...),
		}
		if staged.Path == "" && workspace != "" && len(staged.Data) > 0 {
			path, err := persistInboundAttachment(workspace, now, idx, staged)
			if err != nil {
				slog.Warn("dm_pipeline: failed to stage inbound attachment",
					"agent_id", agentID,
					"workspace", workspace,
					"name", staged.Name,
					"error", err,
				)
			} else {
				staged.Path = path
			}
		}
		out = append(out, staged)
	}
	return out
}

func persistInboundAttachment(workspace string, now time.Time, index int, attachment transport.InboundAttachment) (string, error) {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if workspace == "" {
		return "", fmt.Errorf("workspace is required")
	}
	dir := filepath.Join(workspace, ".gopher", "inbound", now.UTC().Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create inbound attachment directory: %w", err)
	}
	name := safeInboundAttachmentName(attachment.Name)
	target := filepath.Join(dir, fmt.Sprintf("%s-%02d-%s", now.UTC().Format("20060102T150405.000000000Z"), index+1, name))
	if err := os.WriteFile(target, attachment.Data, 0o644); err != nil {
		return "", fmt.Errorf("write inbound attachment: %w", err)
	}
	return target, nil
}

func safeInboundAttachmentName(name string) string {
	base := strings.TrimSpace(filepath.Base(name))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "attachment.bin"
	}
	var b strings.Builder
	b.Grow(len(base))
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	clean := strings.Trim(b.String(), "._-")
	if clean == "" {
		return "attachment.bin"
	}
	return clean
}

func (p *DMPipeline) handleInboundCommand(ctx context.Context, inbound transport.InboundMessage, agentID sessionrt.ActorID, recipientID string) (bool, error) {
	command, ok := parseDMCommand(inbound.Text)
	if !ok {
		return false, nil
	}

	conversationID := strings.TrimSpace(inbound.ConversationID)
	if strings.HasPrefix(command.Kind, "trace.") && p.traceProvisioner == nil {
		p.sendTraceCommandReply(conversationID, recipientID, inbound.EventID, "Trace is disabled in gateway config.")
		return true, nil
	}
	switch command.Kind {
	case "status.show":
		if err := p.sendStatusCommandReply(ctx, inbound, agentID, recipientID); err != nil {
			return true, err
		}
		return true, nil
	case "model.status":
		if err := p.handleModelPolicyStatusCommand(ctx, conversationID, recipientID, inbound.EventID, agentID); err != nil {
			return true, err
		}
		return true, nil
	case "model.set":
		if err := p.handleModelPolicySetCommand(ctx, conversationID, recipientID, inbound.EventID, agentID, command.Value); err != nil {
			return true, err
		}
		return true, nil
	case "thinking.status":
		if _, err := p.resolveConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID); err != nil {
			return true, err
		}
		p.sendCommandReply(conversationID, recipientID, inbound.EventID, thinkingStatusReply(p.thinkingModeForConversation(conversationID)))
		return true, nil
	case "thinking.on":
		if _, err := p.resolveConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID); err != nil {
			return true, err
		}
		if err := p.setThinkingModeForConversation(conversationID, ThinkingModeOn); err != nil {
			return true, err
		}
		p.sendCommandReply(conversationID, recipientID, inbound.EventID, thinkingStatusReply(ThinkingModeOn))
		return true, nil
	case "thinking.off":
		if _, err := p.resolveConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID); err != nil {
			return true, err
		}
		if err := p.setThinkingModeForConversation(conversationID, ThinkingModeOff); err != nil {
			return true, err
		}
		p.sendCommandReply(conversationID, recipientID, inbound.EventID, thinkingStatusReply(ThinkingModeOff))
		return true, nil
	case "context.clear":
		if err := p.resetConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID); err != nil {
			return true, err
		}
		if err := p.sendMessage(context.Background(), transport.OutboundMessage{
			ConversationID:    conversationID,
			SenderID:          recipientID,
			Text:              dmContextClearedReply,
			ThreadRootEventID: inbound.EventID,
		}); err != nil {
			slog.Error("dm_pipeline: failed to send context cleared acknowledgement",
				"conversation_id", conversationID,
				"error", err,
			)
		}
		return true, nil
	case "context.summarize":
		sessionID, err := p.resolveConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID)
		if err != nil {
			return true, err
		}
		p.ensureTraceConversation(ctx, TraceConversationRequest{
			ConversationID:   conversationID,
			ConversationName: inbound.ConversationName,
			SessionID:        sessionID,
			AgentID:          agentID,
			SenderID:         inbound.SenderID,
			RecipientID:      recipientID,
			TriggerEventID:   inbound.EventID,
		})
		p.startProcessing(conversationID)
		from := externalActorID(inbound.SenderID)
		if inbound.SenderManaged {
			if managedActor, ok := p.actorForManagedSender(inbound.SenderID); ok {
				from = managedActor
			}
		}
		p.dispatchInboundEvent(sessionrt.Event{
			SessionID: sessionID,
			From:      from,
			Type:      sessionrt.EventMessage,
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleUser,
				Content: dmSummarizeCommandPrompt,
			},
		}, conversationID, inbound.SenderID, "")
		return true, nil
	case "trace.link", "trace.on":
		sessionID, err := p.resolveConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID)
		if err != nil {
			return true, err
		}
		if err := p.setTraceModeForConversation(conversationID, TraceModeReadOnly); err != nil {
			return true, err
		}
		p.ensureTraceConversationWithOptions(ctx, TraceConversationRequest{
			ConversationID:   conversationID,
			ConversationName: inbound.ConversationName,
			SessionID:        sessionID,
			AgentID:          agentID,
			SenderID:         inbound.SenderID,
			RecipientID:      recipientID,
			TriggerEventID:   inbound.EventID,
		}, true)
		traceConversationID, ok := p.traceConversationFor(conversationID)
		if !ok {
			p.sendTraceCommandReply(conversationID, recipientID, inbound.EventID, "Trace room is not available yet. Try again in a moment.")
			return true, nil
		}
		p.sendTraceConversationReadyNotice(conversationID, traceConversationID, inbound.EventID)
		return true, nil
	case "trace.off":
		_, err := p.resolveConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID)
		if err != nil {
			return true, err
		}
		if err := p.setTraceModeForConversation(conversationID, TraceModeOff); err != nil {
			return true, err
		}
		p.sendTraceCommandReply(conversationID, recipientID, inbound.EventID, "Trace is now off for this conversation.")
		return true, nil
	case "trace.status":
		_, err := p.resolveConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID)
		if err != nil {
			return true, err
		}
		mode := p.traceModeForConversation(conversationID)
		traceConversationID, hasTraceRoom := p.traceConversationFor(conversationID)
		if mode == TraceModeOff {
			p.sendTraceCommandReply(conversationID, recipientID, inbound.EventID, "Trace is off for this conversation.")
			return true, nil
		}
		if !hasTraceRoom {
			p.sendTraceCommandReply(conversationID, recipientID, inbound.EventID, "Trace is on for this conversation. No trace room is bound yet.")
			return true, nil
		}
		p.sendTraceConversationReadyNotice(conversationID, traceConversationID, inbound.EventID)
		return true, nil
	default:
		return false, nil
	}
}

type dmCommand struct {
	Kind  string
	Value string
}

func parseDMCommand(text string) (dmCommand, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return dmCommand{}, false
	}
	commandToken := strings.TrimSpace(fields[0])
	if commandToken == "" {
		return dmCommand{}, false
	}
	if !strings.HasPrefix(commandToken, "!") && !strings.HasPrefix(commandToken, "/") {
		return dmCommand{}, false
	}
	command := normalizeDMCommandToken(commandToken)
	if command == "" {
		return dmCommand{}, false
	}

	switch command {
	case "status":
		return dmCommand{Kind: "status.show"}, true
	case "model":
		if len(fields) == 1 {
			return dmCommand{Kind: "model.status"}, true
		}
		subcommand := strings.ToLower(strings.TrimSpace(fields[1]))
		switch subcommand {
		case "status":
			return dmCommand{Kind: "model.status"}, true
		case "set":
			if len(fields) < 3 {
				return dmCommand{}, false
			}
			value := strings.TrimSpace(fields[2])
			if value == "" {
				return dmCommand{}, false
			}
			return dmCommand{Kind: "model.set", Value: value}, true
		default:
			value := strings.TrimSpace(fields[1])
			if value == "" {
				return dmCommand{}, false
			}
			return dmCommand{Kind: "model.set", Value: value}, true
		}
	case "thinking":
		if len(fields) == 1 {
			return dmCommand{Kind: "thinking.status"}, true
		}
		switch strings.ToLower(strings.TrimSpace(fields[1])) {
		case "status":
			return dmCommand{Kind: "thinking.status"}, true
		case "on":
			return dmCommand{Kind: "thinking.on"}, true
		case "off":
			return dmCommand{Kind: "thinking.off"}, true
		default:
			return dmCommand{}, false
		}
	case "context":
		if len(fields) < 2 {
			return dmCommand{}, false
		}
		switch strings.ToLower(strings.TrimSpace(fields[1])) {
		case "clear", "reset":
			return dmCommand{Kind: "context.clear"}, true
		case "summarize", "summary":
			return dmCommand{Kind: "context.summarize"}, true
		default:
			return dmCommand{}, false
		}
	case "trace":
		if len(fields) == 1 {
			return dmCommand{Kind: "trace.link"}, true
		}
		switch strings.ToLower(strings.TrimSpace(fields[1])) {
		case "link":
			return dmCommand{Kind: "trace.link"}, true
		case "on":
			return dmCommand{Kind: "trace.on"}, true
		case "off":
			return dmCommand{Kind: "trace.off"}, true
		case "status":
			return dmCommand{Kind: "trace.status"}, true
		default:
			return dmCommand{}, false
		}
	default:
		return dmCommand{}, false
	}
}

func normalizeDMCommandToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if strings.HasPrefix(token, "!") || strings.HasPrefix(token, "/") {
		token = token[1:]
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if at := strings.Index(token, "@"); at >= 0 {
		token = token[:at]
	}
	return strings.ToLower(strings.TrimSpace(token))
}

type dmEventSummary struct {
	Total                  int
	UserMessages           int
	AgentMessages          int
	ToolCalls              int
	ToolResults            int
	Errors                 int
	EstimatedMessageTokens int
	LastEventAt            time.Time
}

type dmStatePatchSnapshot struct {
	UpdatedAt               string
	AgentID                 string
	ModelID                 string
	ModelProvider           string
	ModelContextWindow      int
	SessionMessageCount     int
	CompactionSummaryCount  int
	SelectedMemoryCount     int
	ReserveTokens           int
	EstimatedInputTokens    int
	OverflowRetries         int
	RecentUsedTokens        int
	RecentCapTokens         int
	RetrievedUsedTokens     int
	RetrievedCapTokens      int
	CompactionUsedTokens    int
	CompactionCapTokens     int
	WorkingMemoryUsedTokens int
	WorkingMemoryCapTokens  int
	BootstrapUsedTokens     int
	BootstrapCapTokens      int
	Warnings                []string
	PruneActions            []string
	CompactionActions       []string
}

func (p *DMPipeline) sendStatusCommandReply(ctx context.Context, inbound transport.InboundMessage, agentID sessionrt.ActorID, recipientID string) error {
	conversationID := strings.TrimSpace(inbound.ConversationID)
	if conversationID == "" {
		return nil
	}
	if strings.TrimSpace(recipientID) == "" {
		recipientID = p.recipientForConversation(conversationID)
	}
	if route, ok := p.currentRoute(conversationID); ok {
		if strings.TrimSpace(string(route.AgentID)) != "" {
			agentID = route.AgentID
		}
		if strings.TrimSpace(route.RecipientID) != "" {
			recipientID = route.RecipientID
		}
	}

	traceMode := p.traceModeForConversation(conversationID)
	traceConversationID, hasTraceConversation := p.traceConversationFor(conversationID)

	lines := []string{
		"status",
		fmt.Sprintf("conversation: %s", conversationID),
	}
	if strings.TrimSpace(inbound.ConversationName) != "" {
		lines = append(lines, fmt.Sprintf("conversation_name: %s", strings.TrimSpace(inbound.ConversationName)))
	}
	if strings.TrimSpace(string(agentID)) != "" {
		lines = append(lines, fmt.Sprintf("agent: %s", strings.TrimSpace(string(agentID))))
	}
	lines = append(lines, fmt.Sprintf("processing: %s", yesNo(p.IsConversationProcessing(conversationID))))
	if hasTraceConversation {
		lines = append(lines, fmt.Sprintf("trace: %s (%s)", traceMode, traceConversationID))
	} else {
		lines = append(lines, fmt.Sprintf("trace: %s", traceMode))
	}

	sessionID, hasSession := p.lookupConversationSession(conversationID)
	if !hasSession {
		lines = append(lines, "session: none")
		lines = append(lines, "context: unavailable (no active session yet)")
		p.sendCommandReply(conversationID, recipientID, inbound.EventID, strings.Join(lines, "\n"))
		return nil
	}

	loadedSession, err := p.manager.GetSession(ctx, sessionID)
	if err != nil && !errors.Is(err, sessionrt.ErrSessionNotFound) {
		return fmt.Errorf("load session %s: %w", sessionID, err)
	}
	if loadedSession != nil {
		lines = append(lines, fmt.Sprintf("session: %s (%s)", sessionID, sessionStatusText(loadedSession.Status)))
	} else {
		lines = append(lines, fmt.Sprintf("session: %s", sessionID))
	}

	events := []sessionrt.Event(nil)
	if p.eventStore != nil {
		listed, err := p.eventStore.List(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("list session events %s: %w", sessionID, err)
		}
		events = listed
	}

	eventSummary := summarizeSessionEvents(events)
	if eventSummary.Total > 0 {
		lines = append(lines, fmt.Sprintf(
			"events: total=%d user=%d agent=%d tool_calls=%d tool_results=%d errors=%d",
			eventSummary.Total,
			eventSummary.UserMessages,
			eventSummary.AgentMessages,
			eventSummary.ToolCalls,
			eventSummary.ToolResults,
			eventSummary.Errors,
		))
		if !eventSummary.LastEventAt.IsZero() {
			lines = append(lines, fmt.Sprintf("last_event_at: %s", eventSummary.LastEventAt.UTC().Format(time.RFC3339)))
		}
	}

	statePatch, hasStatePatch := latestStatePatch(events)
	if hasStatePatch {
		modelLine := strings.TrimSpace(statePatch.ModelID)
		if strings.TrimSpace(statePatch.ModelProvider) != "" {
			if modelLine == "" {
				modelLine = statePatch.ModelProvider
			} else {
				modelLine = modelLine + " (" + statePatch.ModelProvider + ")"
			}
		}
		if modelLine != "" {
			lines = append(lines, "model: "+modelLine)
		}
		if strings.TrimSpace(statePatch.UpdatedAt) != "" {
			lines = append(lines, fmt.Sprintf("status_updated_at: %s", strings.TrimSpace(statePatch.UpdatedAt)))
		}
		if statePatch.SessionMessageCount > 0 {
			lines = append(lines, fmt.Sprintf(
				"context_messages: %d (compaction_summaries=%d)",
				statePatch.SessionMessageCount,
				statePatch.CompactionSummaryCount,
			))
		}

		contextLineParts := []string{}
		if statePatch.ModelContextWindow > 0 {
			utilization := 0.0
			if statePatch.EstimatedInputTokens > 0 {
				utilization = (float64(statePatch.EstimatedInputTokens) * 100) / float64(statePatch.ModelContextWindow)
			}
			contextLineParts = append(contextLineParts, fmt.Sprintf(
				"context: %d/%d tokens (%.1f%%)",
				statePatch.EstimatedInputTokens,
				statePatch.ModelContextWindow,
				utilization,
			))
		} else if statePatch.EstimatedInputTokens > 0 {
			contextLineParts = append(contextLineParts, fmt.Sprintf("context: ~%d tokens", statePatch.EstimatedInputTokens))
		}
		if statePatch.ReserveTokens > 0 {
			usable := statePatch.ModelContextWindow - statePatch.ReserveTokens
			if usable < 0 {
				usable = 0
			}
			contextLineParts = append(contextLineParts, fmt.Sprintf("reserve=%d usable=%d", statePatch.ReserveTokens, usable))
		}
		if len(contextLineParts) > 0 {
			lines = append(lines, strings.Join(contextLineParts, " | "))
		}

		if statePatch.RecentCapTokens > 0 || statePatch.RecentUsedTokens > 0 {
			lines = append(lines, fmt.Sprintf("lane_recent: %d/%d", statePatch.RecentUsedTokens, statePatch.RecentCapTokens))
		}
		if statePatch.RetrievedCapTokens > 0 || statePatch.RetrievedUsedTokens > 0 {
			lines = append(lines, fmt.Sprintf("lane_memory: %d/%d", statePatch.RetrievedUsedTokens, statePatch.RetrievedCapTokens))
		}
		if statePatch.CompactionCapTokens > 0 || statePatch.CompactionUsedTokens > 0 {
			lines = append(lines, fmt.Sprintf("lane_compaction: %d/%d", statePatch.CompactionUsedTokens, statePatch.CompactionCapTokens))
		}
		if statePatch.OverflowRetries > 0 {
			lines = append(lines, fmt.Sprintf("overflow_retries: %d", statePatch.OverflowRetries))
		}
		if len(statePatch.Warnings) > 0 {
			lines = append(lines, fmt.Sprintf("warnings: %d (latest: %s)", len(statePatch.Warnings), statePatch.Warnings[len(statePatch.Warnings)-1]))
		}
	} else {
		if eventSummary.EstimatedMessageTokens > 0 {
			lines = append(lines, fmt.Sprintf("context: ~%d tokens (message text estimate)", eventSummary.EstimatedMessageTokens))
			lines = append(lines, "context_window: unavailable (no runtime snapshot yet)")
		} else {
			lines = append(lines, "context: unavailable (no runtime snapshot yet)")
		}
	}

	p.sendCommandReply(conversationID, recipientID, inbound.EventID, strings.Join(lines, "\n"))
	return nil
}

func (p *DMPipeline) handleModelPolicyStatusCommand(
	ctx context.Context,
	conversationID, recipientID, triggerEventID string,
	agentID sessionrt.ActorID,
) error {
	if p.modelPolicyCommand == nil {
		p.sendCommandReply(conversationID, recipientID, triggerEventID, dmModelCommandDisabled)
		return nil
	}
	result, err := p.modelPolicyCommand(ctx, ModelPolicyCommandRequest{
		AgentID: agentID,
	})
	if err != nil {
		p.sendCommandReply(conversationID, recipientID, triggerEventID, fmt.Sprintf("Model status failed: %v", err))
		return nil
	}
	current := strings.TrimSpace(result.CurrentModelPolicy)
	if current == "" {
		current = "(unset)"
	}
	p.sendCommandReply(conversationID, recipientID, triggerEventID, "Current model policy: "+current)
	return nil
}

func (p *DMPipeline) handleModelPolicySetCommand(
	ctx context.Context,
	conversationID, recipientID, triggerEventID string,
	agentID sessionrt.ActorID,
	requestedPolicy string,
) error {
	requestedPolicy = strings.TrimSpace(requestedPolicy)
	if requestedPolicy == "" {
		p.sendCommandReply(conversationID, recipientID, triggerEventID, dmModelCommandUsage)
		return nil
	}
	if p.modelPolicyCommand == nil {
		p.sendCommandReply(conversationID, recipientID, triggerEventID, dmModelCommandDisabled)
		return nil
	}
	result, err := p.modelPolicyCommand(ctx, ModelPolicyCommandRequest{
		AgentID:              agentID,
		RequestedModelPolicy: requestedPolicy,
	})
	if err != nil {
		p.sendCommandReply(conversationID, recipientID, triggerEventID, fmt.Sprintf("Model update failed: %v", err))
		return nil
	}

	current := strings.TrimSpace(result.CurrentModelPolicy)
	if current == "" {
		current = requestedPolicy
	}
	lines := []string{}
	if result.Updated {
		lines = append(lines, fmt.Sprintf("Model set to %s.", current))
	} else {
		lines = append(lines, fmt.Sprintf("Model already set to %s.", current))
	}
	if result.RestartScheduled {
		lines = append(lines, "Restart scheduled. The new model will be active after the gateway comes back.")
	} else {
		lines = append(lines, "Restart required. Run `gopher restart` to apply it.")
	}
	p.sendCommandReply(conversationID, recipientID, triggerEventID, strings.Join(lines, " "))
	return nil
}

func summarizeSessionEvents(events []sessionrt.Event) dmEventSummary {
	summary := dmEventSummary{
		Total: len(events),
	}
	for _, event := range events {
		if event.Timestamp.After(summary.LastEventAt) {
			summary.LastEventAt = event.Timestamp
		}
		switch event.Type {
		case sessionrt.EventMessage:
			msg, ok := messageFromPayload(event.Payload)
			if !ok {
				continue
			}
			if msg.Role == sessionrt.RoleUser {
				summary.UserMessages++
			}
			if msg.Role == sessionrt.RoleAgent {
				summary.AgentMessages++
			}
			summary.EstimatedMessageTokens += ctxbundle.EstimateTextTokens(msg.Content)
		case sessionrt.EventToolCall:
			summary.ToolCalls++
		case sessionrt.EventToolResult:
			summary.ToolResults++
		case sessionrt.EventError:
			summary.Errors++
		}
	}
	return summary
}

func latestStatePatch(events []sessionrt.Event) (dmStatePatchSnapshot, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != sessionrt.EventStatePatch {
			continue
		}
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			continue
		}
		out := dmStatePatchSnapshot{
			UpdatedAt:               stringFromAny(payload["updated_at"]),
			AgentID:                 stringFromAny(payload["agent_id"]),
			ModelID:                 stringFromAny(payload["model_id"]),
			ModelProvider:           stringFromAny(payload["model_provider"]),
			ModelContextWindow:      intFromAny(payload["model_context_window"]),
			SessionMessageCount:     intFromAny(payload["session_message_count"]),
			CompactionSummaryCount:  intFromAny(payload["compaction_summary_count"]),
			SelectedMemoryCount:     intFromAny(payload["selected_memory_count"]),
			ReserveTokens:           intFromAny(payload["reserve_tokens"]),
			EstimatedInputTokens:    intFromAny(payload["estimated_input_tokens"]),
			OverflowRetries:         intFromAny(payload["overflow_retries"]),
			RecentUsedTokens:        intFromAny(payload["recent_messages_used_tokens"]),
			RecentCapTokens:         intFromAny(payload["recent_messages_cap_tokens"]),
			RetrievedUsedTokens:     intFromAny(payload["retrieved_memory_used_tokens"]),
			RetrievedCapTokens:      intFromAny(payload["retrieved_memory_cap_tokens"]),
			CompactionUsedTokens:    intFromAny(payload["compaction_used_tokens"]),
			CompactionCapTokens:     intFromAny(payload["compaction_cap_tokens"]),
			WorkingMemoryUsedTokens: intFromAny(payload["working_memory_used_tokens"]),
			WorkingMemoryCapTokens:  intFromAny(payload["working_memory_cap_tokens"]),
			BootstrapUsedTokens:     intFromAny(payload["bootstrap_used_tokens"]),
			BootstrapCapTokens:      intFromAny(payload["bootstrap_cap_tokens"]),
			Warnings:                stringSliceFromAny(payload["warnings"]),
			PruneActions:            stringSliceFromAny(payload["prune_actions"]),
			CompactionActions:       stringSliceFromAny(payload["compaction_actions"]),
		}
		if out.UpdatedAt == "" && !event.Timestamp.IsZero() {
			out.UpdatedAt = event.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		return out, true
	}
	return dmStatePatchSnapshot{}, false
}

func messageFromPayload(payload any) (sessionrt.Message, bool) {
	switch typed := payload.(type) {
	case sessionrt.Message:
		return typed, true
	case map[string]any:
		role, _ := typed["role"].(string)
		content, _ := typed["content"].(string)
		target, _ := typed["target_actor_id"].(string)
		return sessionrt.Message{
			Role:          sessionrt.Role(strings.TrimSpace(role)),
			Content:       content,
			TargetActorID: sessionrt.ActorID(strings.TrimSpace(target)),
		}, true
	default:
		return sessionrt.Message{}, false
	}
}

func sessionStatusText(status sessionrt.SessionStatus) string {
	switch status {
	case sessionrt.SessionActive:
		return "active"
	case sessionrt.SessionPaused:
		return "paused"
	case sessionrt.SessionCompleted:
		return "completed"
	case sessionrt.SessionFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

func stringSliceFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			trimmed := strings.TrimSpace(item)
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			trimmed := stringFromAny(item)
			if trimmed != "" && trimmed != "<nil>" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		return nil
	}
}

func (p *DMPipeline) resetConversationSession(ctx context.Context, conversationID, conversationName, senderID string, agentID sessionrt.ActorID, recipientID string) error {
	if existing, ok := p.lookupConversationSession(conversationID); ok {
		if err := p.manager.CancelSession(ctx, existing); err != nil &&
			err != sessionrt.ErrSessionNotFound &&
			err != sessionrt.ErrSessionNotActive {
			return fmt.Errorf("cancel previous session: %w", err)
		}
	}
	_, err := p.resolveConversationSession(ctx, conversationID, conversationName, senderID, agentID, recipientID)
	if err != nil {
		return fmt.Errorf("create replacement session: %w", err)
	}
	return nil
}

func (p *DMPipeline) dispatchInboundEvent(event sessionrt.Event, conversationID, senderID, inboundEventID string) {
	p.runBackground("dispatch_inbound_event", conversationID, event.SessionID, func() {
		if err := p.manager.SendEvent(context.Background(), event); err != nil {
			p.finishProcessing(conversationID)
			slog.Error("dm_pipeline: send dm session event failed",
				"conversation_id", conversationID,
				"sender_id", senderID,
				"error", err,
			)
			if p.shouldNotifyTerminalErrorFallback(conversationID, event.SessionID) {
				p.sendErrorFallback(conversationID, p.recipientForConversation(conversationID), err.Error())
			} else {
				slog.Info("dm_pipeline: suppressing non-terminal send-event fallback",
					"conversation_id", conversationID,
					"session_id", event.SessionID,
				)
			}
			return
		}
		p.markInboundEventProcessed(conversationID, event.SessionID, inboundEventID)
	})
}

func (p *DMPipeline) resolveConversationSession(ctx context.Context, conversationID, conversationName, senderID string, desiredAgentID sessionrt.ActorID, recipientID string) (sessionrt.SessionID, error) {
	conversationName = strings.TrimSpace(conversationName)
	if strings.TrimSpace(string(desiredAgentID)) == "" {
		desiredAgentID = p.agentID
	}
	if route, ok := p.currentRoute(conversationID); ok {
		if strings.TrimSpace(string(route.AgentID)) != "" {
			desiredAgentID = route.AgentID
		}
		if strings.TrimSpace(route.RecipientID) != "" {
			recipientID = route.RecipientID
		}
	}
	if existing, ok := p.lookupConversationSession(conversationID); ok {
		if !p.isSessionActive(ctx, existing) {
			slog.Debug("dm_pipeline: conversation has inactive session; creating replacement",
				"conversation_id", conversationID,
				"session_id", existing,
			)
		} else {
			route, routeExists := p.currentRoute(conversationID)
			if !routeExists {
				p.setConversationRoute(conversationID, desiredAgentID, recipientID, ConversationModeDM)
				route = conversationRoute{
					AgentID:     desiredAgentID,
					RecipientID: recipientID,
					Mode:        ConversationModeDM,
				}
			}
			if !p.sessionHasAgentParticipant(ctx, existing, route.AgentID) {
				slog.Warn("dm_pipeline: conversation has mismatched agent session; creating replacement",
					"conversation_id", conversationID,
					"session_id", existing,
					"expected_agent_id", route.AgentID,
				)
			} else if p.isConversationSessionStale(conversationID, existing) {
				slog.Info("dm_pipeline: conversation session expired by lifecycle; creating replacement",
					"conversation_id", conversationID,
					"session_id", existing,
				)
				p.cancelConversationSession(ctx, existing)
			} else {
				if err := p.ensureSubscription(conversationID, existing); err != nil {
					return "", err
				}
				if err := p.maybeUpdateConversationName(conversationID, existing, route, conversationName); err != nil {
					return "", err
				}
				return existing, nil
			}
		}
	}

	p.setupMu.Lock()
	defer p.setupMu.Unlock()

	if existing, ok := p.lookupConversationSession(conversationID); ok {
		if !p.isSessionActive(ctx, existing) {
			slog.Debug("dm_pipeline: conversation has inactive session after lock; creating replacement",
				"conversation_id", conversationID,
				"session_id", existing,
			)
		} else {
			route, routeExists := p.currentRoute(conversationID)
			if !routeExists {
				p.setConversationRoute(conversationID, desiredAgentID, recipientID, ConversationModeDM)
				route = conversationRoute{
					AgentID:     desiredAgentID,
					RecipientID: recipientID,
					Mode:        ConversationModeDM,
				}
			}
			if !p.sessionHasAgentParticipant(ctx, existing, route.AgentID) {
				slog.Warn("dm_pipeline: conversation has mismatched agent session after lock; creating replacement",
					"conversation_id", conversationID,
					"session_id", existing,
					"expected_agent_id", route.AgentID,
				)
			} else if p.isConversationSessionStale(conversationID, existing) {
				slog.Info("dm_pipeline: conversation session expired by lifecycle after lock; creating replacement",
					"conversation_id", conversationID,
					"session_id", existing,
				)
				p.cancelConversationSession(ctx, existing)
			} else {
				if err := p.ensureSubscription(conversationID, existing); err != nil {
					return "", err
				}
				if err := p.maybeUpdateConversationName(conversationID, existing, route, conversationName); err != nil {
					return "", err
				}
				return existing, nil
			}
		}
	}
	if route, ok := p.currentRoute(conversationID); ok {
		if strings.TrimSpace(string(route.AgentID)) != "" {
			desiredAgentID = route.AgentID
		}
		if strings.TrimSpace(route.RecipientID) != "" {
			recipientID = route.RecipientID
		}
	}

	slog.Info("dm_pipeline: creating new session",
		"conversation_id", conversationID,
		"agent_id", desiredAgentID,
		"sender_id", senderID,
	)
	createOpts := sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: desiredAgentID, Type: sessionrt.ActorAgent},
			{ID: externalActorID(senderID), Type: sessionrt.ActorHuman},
		},
	}
	if conversationName != "" {
		createOpts.DisplayName = conversationName
	}
	created, err := p.manager.CreateSession(ctx, createOpts)
	if err != nil {
		slog.Error("dm_pipeline: failed to create session",
			"conversation_id", conversationID,
			"error", err,
		)
		return "", fmt.Errorf("create dm session: %w", err)
	}
	if err := p.ensureSubscription(conversationID, created.ID); err != nil {
		_ = p.manager.CancelSession(context.Background(), created.ID)
		return "", err
	}
	if err := p.bindConversation(conversationID, created.ID, desiredAgentID, recipientID, ConversationModeDM, conversationName); err != nil {
		_ = p.manager.CancelSession(context.Background(), created.ID)
		return "", err
	}
	slog.Info("dm_pipeline: session created",
		"conversation_id", conversationID,
		"session_id", created.ID,
	)
	return created.ID, nil
}

func (p *DMPipeline) sessionHasAgentParticipant(ctx context.Context, sessionID sessionrt.SessionID, expectedAgentID sessionrt.ActorID) bool {
	expectedAgentID = sessionrt.ActorID(strings.TrimSpace(string(expectedAgentID)))
	if strings.TrimSpace(string(expectedAgentID)) == "" {
		return true
	}
	session, err := p.manager.GetSession(ctx, sessionID)
	if err != nil || session == nil {
		return false
	}
	participant, ok := session.Participants[expectedAgentID]
	if !ok {
		return false
	}
	return participant.Type == sessionrt.ActorAgent
}

func (p *DMPipeline) maybeUpdateConversationName(conversationID string, sessionID sessionrt.SessionID, route conversationRoute, conversationName string) error {
	conversationName = strings.TrimSpace(conversationName)
	if conversationName == "" || p.bindings == nil {
		return nil
	}
	existing, ok := p.bindings.GetByConversation(conversationID)
	if ok && strings.TrimSpace(existing.ConversationName) == conversationName {
		return nil
	}
	return p.bindConversation(conversationID, sessionID, route.AgentID, route.RecipientID, route.Mode, conversationName)
}

func (p *DMPipeline) isSessionActive(ctx context.Context, sessionID sessionrt.SessionID) bool {
	session, err := p.manager.GetSession(ctx, sessionID)
	if err != nil || session == nil {
		return false
	}
	return session.Status == sessionrt.SessionActive
}

func (p *DMPipeline) isConversationSessionStale(conversationID string, sessionID sessionrt.SessionID) bool {
	if !p.lifecycle.Enabled || p.bindings == nil {
		return false
	}
	binding, ok := p.bindings.GetByConversation(strings.TrimSpace(conversationID))
	if !ok {
		return false
	}
	boundSessionID := sessionrt.SessionID(strings.TrimSpace(string(binding.SessionID)))
	if boundSessionID != "" && boundSessionID != sessionID {
		return false
	}

	lastActivity := binding.UpdatedAt
	if lastActivity.IsZero() {
		lastActivity = binding.CreatedAt
	}
	return sessionrt.IsStaleByDailyReset(lastActivity, p.now(), p.lifecycle)
}

func (p *DMPipeline) cancelConversationSession(ctx context.Context, sessionID sessionrt.SessionID) {
	err := p.manager.CancelSession(ctx, sessionID)
	if err != nil && err != sessionrt.ErrSessionNotFound && err != sessionrt.ErrSessionNotActive {
		slog.Warn("dm_pipeline: cancel stale conversation session failed",
			"session_id", sessionID,
			"error", err,
		)
	}
}

func (p *DMPipeline) ensureSubscription(conversationID string, sessionID sessionrt.SessionID) error {
	p.subscribedMu.Lock()
	if _, exists := p.subscribed[sessionID]; exists {
		p.subscribedMu.Unlock()
		return nil
	}
	p.subscribedMu.Unlock()

	stream, err := p.manager.Subscribe(context.Background(), sessionID)
	if err != nil {
		return fmt.Errorf("subscribe session events: %w", err)
	}

	p.subscribedMu.Lock()
	if _, exists := p.subscribed[sessionID]; exists {
		p.subscribedMu.Unlock()
		return nil
	}
	p.subscribed[sessionID] = struct{}{}
	p.subscribedMu.Unlock()

	p.runBackground("session_subscription", conversationID, sessionID, func() {
		for event := range stream {
			if !p.isCurrentConversationSession(conversationID, sessionID) {
				continue
			}
			if err := p.publishTraceEvent(conversationID, event); err != nil {
				slog.Warn("dm_pipeline: publish trace event failed",
					"conversation_id", conversationID,
					"session_id", sessionID,
					"event_type", event.Type,
					"error", err,
				)
			}
			if event.Type == sessionrt.EventError {
				p.scheduleErrorFallback(conversationID, errorTextFromPayload(event.Payload))
				continue
			}
			if event.Type == sessionrt.EventControl {
				control, ok := controlPayloadFromAny(event.Payload)
				if ok && strings.TrimSpace(control.Action) == sessionrt.ControlActionSessionFailed {
					p.scheduleErrorFallback(conversationID, control.Reason)
				}
			}
			if event.Type == sessionrt.EventToolResult {
				p.captureAttachmentsFromEvent(conversationID, event)
				p.captureMessageEchoFromEvent(conversationID, event)
			}
			if event.Type == sessionrt.EventToolCall {
				p.sendToolCallDraft(conversationID, event)
				continue
			}
			if event.Type == sessionrt.EventAgentThinkingDelta {
				p.sendThinkingDraft(conversationID, event)
				continue
			}
			if event.Type == sessionrt.EventAgentDelta {
				p.sendDraftDelta(conversationID, event)
				continue
			}

			if event.Type != sessionrt.EventMessage {
				continue
			}
			msg, ok := messageFromPayload(event.Payload)
			if !ok {
				continue
			}
			if msg.Role != sessionrt.RoleAgent {
				continue
			}
			if strings.TrimSpace(string(msg.TargetActorID)) != "" {
				// Agent-targeted messages are internal session traffic. Keep them
				// inside the session so the target actor can continue the workflow.
				p.cancelScheduledErrorFallback(conversationID)
				continue
			}
			p.cancelScheduledErrorFallback(conversationID)
			p.finishProcessing(conversationID)
			attachments := p.takePendingAttachments(conversationID)
			content := strings.TrimSpace(msg.Content)
			heartbeatResult := p.consumeHeartbeatReply(conversationID, content)
			if heartbeatResult.Suppress {
				continue
			}
			if heartbeatResult.Normalized != "" && heartbeatResult.Normalized != content {
				msg.Content = heartbeatResult.Normalized
				content = heartbeatResult.Normalized
			}
			if p.consumeSuppressedReply(conversationID, content, len(attachments)) {
				continue
			}
			if content == "" && len(attachments) == 0 {
				continue
			}
			if heartbeatResult.IsHeartbeat && len(attachments) == 0 && p.shouldSuppressDuplicateHeartbeat(conversationID, content) {
				continue
			}
			slog.Debug("dm_pipeline: sending message",
				"conversation_id", conversationID,
				"content_length", len(content),
				"attachments", len(attachments),
			)
			if err := p.sendMessage(context.Background(), transport.OutboundMessage{
				ConversationID: conversationID,
				SenderID:       p.senderForConversationEvent(conversationID, event.From),
				Text:           msg.Content,
				Attachments:    attachments,
			}); err != nil {
				slog.Error("dm_pipeline: send dm response failed",
					"conversation_id", conversationID,
					"error", err,
				)
				continue
			}
			if heartbeatResult.IsHeartbeat && len(attachments) == 0 {
				p.recordHeartbeatDelivery(conversationID, content)
			}
		}
	})
	return nil
}

func (p *DMPipeline) startProcessing(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	p.cancelScheduledErrorFallback(conversationID)

	shouldEnable := false
	p.processingMu.Lock()
	if p.processing[conversationID] == 0 {
		shouldEnable = true
	}
	p.processing[conversationID]++
	p.processingMu.Unlock()

	if shouldEnable {
		p.resetErrorRecoveryState(conversationID)
		p.clearPendingAttachments(conversationID)
		p.clearPendingMessageDedupe(conversationID)
		p.clearDraftState(conversationID)
		p.sendTyping(conversationID, true)
		p.startTypingKeepalive(conversationID)
	}
}

func (p *DMPipeline) finishProcessing(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}

	shouldDisable := false
	p.processingMu.Lock()
	count := p.processing[conversationID]
	switch {
	case count <= 0:
		p.processingMu.Unlock()
		return
	case count == 1:
		delete(p.processing, conversationID)
		shouldDisable = true
	default:
		p.processing[conversationID] = count - 1
	}
	p.processingMu.Unlock()

	if shouldDisable {
		p.clearErrorRecoveryState(conversationID)
		p.clearDraftState(conversationID)
		p.stopTypingKeepalive(conversationID)
		p.sendTyping(conversationID, false)
	}
}

func (p *DMPipeline) scheduleErrorFallback(conversationID, rawErr string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	rawErr = strings.TrimSpace(rawErr)

	p.errorFallbackMu.Lock()
	existing, hasExisting := p.errorFallbacks[conversationID]
	if hasExisting && existing.timer != nil {
		existing.timer.Stop()
	}
	p.errorFallbackSeq++
	seq := p.errorFallbackSeq
	timer := time.AfterFunc(dmErrorFallbackDelay, func() {
		p.fireScheduledErrorFallback(conversationID, seq)
	})
	p.errorFallbacks[conversationID] = pendingErrorFallback{
		seq:    seq,
		rawErr: rawErr,
		timer:  timer,
	}
	p.errorFallbackMu.Unlock()
}

func (p *DMPipeline) cancelScheduledErrorFallback(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	p.errorFallbackMu.Lock()
	state, ok := p.errorFallbacks[conversationID]
	if ok {
		delete(p.errorFallbacks, conversationID)
	}
	p.errorFallbackMu.Unlock()
	if ok && state.timer != nil {
		state.timer.Stop()
	}
}

func (p *DMPipeline) fireScheduledErrorFallback(conversationID string, seq uint64) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	var rawErr string
	p.errorFallbackMu.Lock()
	state, ok := p.errorFallbacks[conversationID]
	if !ok || state.seq != seq {
		p.errorFallbackMu.Unlock()
		return
	}
	rawErr = state.rawErr
	delete(p.errorFallbacks, conversationID)
	p.errorFallbackMu.Unlock()

	if shouldAttemptLLMErrorRecovery(rawErr) && p.consumeErrorRecoveryAttempt(conversationID) {
		if p.dispatchErrorRecoveryPrompt(conversationID, rawErr) {
			// Wait for recovery completion before the final fallback.
			p.scheduleErrorFallback(conversationID, rawErr)
			return
		}
	}

	if p.shouldNotifyTerminalErrorFallback(conversationID, "") {
		p.finishProcessing(conversationID)
		p.clearPendingAttachments(conversationID)
		p.sendErrorFallback(conversationID, p.recipientForConversation(conversationID), rawErr)
		return
	}
	if p.isConversationSessionInFlight(conversationID, "") {
		slog.Debug("dm_pipeline: deferring non-terminal error fallback while session is in-flight",
			"conversation_id", conversationID,
			"error_detail", fallbackErrorDetail(rawErr),
		)
		p.scheduleErrorFallback(conversationID, rawErr)
		return
	}
	p.finishProcessing(conversationID)
	p.clearPendingAttachments(conversationID)
	slog.Info("dm_pipeline: suppressing non-terminal error fallback",
		"conversation_id", conversationID,
		"error_detail", fallbackErrorDetail(rawErr),
	)
}

func (p *DMPipeline) dispatchErrorRecoveryPrompt(conversationID, rawErr string) bool {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return false
	}
	sessionID, ok := p.lookupConversationSession(conversationID)
	if !ok {
		return false
	}

	event := sessionrt.Event{
		SessionID: sessionID,
		From:      p.recoveryActorForSession(sessionID),
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: buildErrorRecoveryPrompt(rawErr),
		},
	}
	if err := p.manager.SendEvent(context.Background(), event); err != nil {
		slog.Warn("dm_pipeline: failed to dispatch error recovery prompt",
			"conversation_id", conversationID,
			"session_id", sessionID,
			"error", err,
		)
		return false
	}
	slog.Info("dm_pipeline: dispatched error recovery prompt",
		"conversation_id", conversationID,
		"session_id", sessionID,
		"error_detail", fallbackErrorDetail(rawErr),
	)
	return true
}

func buildErrorRecoveryPrompt(rawErr string) string {
	detail := strings.TrimSpace(fallbackErrorDetail(rawErr))
	if detail == "" {
		detail = "upstream request failed"
	}
	return fmt.Sprintf(
		"The previous attempt failed with an upstream issue (%s). Continue helping the user if possible. If it cannot be completed now, explain briefly and suggest the next step.",
		detail,
	)
}

func (p *DMPipeline) recoveryActorForSession(sessionID sessionrt.SessionID) sessionrt.ActorID {
	session, err := p.manager.GetSession(context.Background(), sessionID)
	if err == nil && session != nil {
		for actorID, participant := range session.Participants {
			if participant.Type == sessionrt.ActorHuman && strings.TrimSpace(string(actorID)) != "" {
				return actorID
			}
		}
	}
	return externalActorID("dm-error-recovery")
}

func (p *DMPipeline) consumeErrorRecoveryAttempt(conversationID string) bool {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return false
	}
	p.errorRecoveryMu.Lock()
	defer p.errorRecoveryMu.Unlock()
	count := p.errorRecoveryCount[conversationID]
	if count >= dmErrorRecoveryMaxTries {
		return false
	}
	p.errorRecoveryCount[conversationID] = count + 1
	return true
}

func (p *DMPipeline) resetErrorRecoveryState(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	p.errorRecoveryMu.Lock()
	p.errorRecoveryCount[conversationID] = 0
	p.errorRecoveryMu.Unlock()
}

func (p *DMPipeline) clearErrorRecoveryState(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	p.errorRecoveryMu.Lock()
	delete(p.errorRecoveryCount, conversationID)
	p.errorRecoveryMu.Unlock()
}

func (p *DMPipeline) captureAttachmentsFromEvent(conversationID string, event sessionrt.Event) {
	if p == nil || p.attachmentResolver == nil {
		return
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	route, ok := p.currentRoute(conversationID)
	agentID := p.agentID
	if ok && strings.TrimSpace(string(route.AgentID)) != "" {
		agentID = route.AgentID
	}
	attachments := p.attachmentResolver(conversationID, agentID, event)
	if len(attachments) == 0 {
		return
	}
	p.attachmentMu.Lock()
	existing := p.pendingFiles[conversationID]
	seen := make(map[string]struct{}, len(existing)+len(attachments))
	for _, attachment := range existing {
		key := strings.TrimSpace(attachment.Path) + "\x00" + strings.TrimSpace(attachment.Name)
		seen[key] = struct{}{}
	}
	for _, attachment := range attachments {
		pathValue := strings.TrimSpace(attachment.Path)
		if pathValue == "" {
			continue
		}
		normalized := transport.OutboundAttachment{
			Path:     pathValue,
			Name:     strings.TrimSpace(attachment.Name),
			MIMEType: strings.TrimSpace(attachment.MIMEType),
		}
		key := normalized.Path + "\x00" + normalized.Name
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, normalized)
	}
	if len(existing) == 0 {
		delete(p.pendingFiles, conversationID)
	} else {
		p.pendingFiles[conversationID] = existing
	}
	p.attachmentMu.Unlock()
}

func (p *DMPipeline) takePendingAttachments(conversationID string) []transport.OutboundAttachment {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil
	}
	p.attachmentMu.Lock()
	defer p.attachmentMu.Unlock()
	attachments := p.pendingFiles[conversationID]
	if len(attachments) == 0 {
		return nil
	}
	delete(p.pendingFiles, conversationID)
	out := make([]transport.OutboundAttachment, len(attachments))
	copy(out, attachments)
	return out
}

func (p *DMPipeline) clearPendingAttachments(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	p.attachmentMu.Lock()
	delete(p.pendingFiles, conversationID)
	p.attachmentMu.Unlock()
}

func (p *DMPipeline) captureMessageEchoFromEvent(conversationID string, event sessionrt.Event) {
	if p == nil {
		return
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	text, ok := messageToolResultText(event.Payload)
	if !ok {
		return
	}
	p.enqueuePendingMessageText(conversationID, text)
}

func messageToolResultText(payload any) (string, bool) {
	eventPayload, ok := payload.(map[string]any)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(stringFromAny(eventPayload["name"])) != "message" {
		return "", false
	}
	status := strings.TrimSpace(stringFromAny(eventPayload["status"]))
	if status != "" && status != "ok" {
		return "", false
	}
	result, ok := eventPayload["result"].(map[string]any)
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(stringFromAny(result["text"]))
	if text == "" {
		return "", false
	}
	return text, true
}

func (p *DMPipeline) consumeSuppressedReply(conversationID, text string, attachmentCount int) bool {
	conversationID = strings.TrimSpace(conversationID)
	text = strings.TrimSpace(text)
	if conversationID == "" || text == "" {
		return false
	}
	if text == noReplyToken {
		p.clearPendingMessageDedupe(conversationID)
		return true
	}
	if attachmentCount > 0 {
		return false
	}
	return p.consumePendingMessageText(conversationID, text)
}

func (p *DMPipeline) enqueuePendingMessageText(conversationID, text string) {
	conversationID = strings.TrimSpace(conversationID)
	text = strings.TrimSpace(text)
	if conversationID == "" || text == "" {
		return
	}
	p.messageDedupeMu.Lock()
	p.pendingMessageText[conversationID] = append(p.pendingMessageText[conversationID], text)
	p.messageDedupeMu.Unlock()
}

func (p *DMPipeline) consumePendingMessageText(conversationID, text string) bool {
	conversationID = strings.TrimSpace(conversationID)
	text = strings.TrimSpace(text)
	if conversationID == "" || text == "" {
		return false
	}
	p.messageDedupeMu.Lock()
	defer p.messageDedupeMu.Unlock()
	pending := p.pendingMessageText[conversationID]
	if len(pending) == 0 {
		return false
	}
	matchIndex := -1
	for idx, candidate := range pending {
		if strings.TrimSpace(candidate) == text {
			matchIndex = idx
			break
		}
	}
	if matchIndex < 0 {
		return false
	}
	pending = append(pending[:matchIndex], pending[matchIndex+1:]...)
	if len(pending) == 0 {
		delete(p.pendingMessageText, conversationID)
	} else {
		p.pendingMessageText[conversationID] = pending
	}
	return true
}

func (p *DMPipeline) clearPendingMessageDedupe(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	p.messageDedupeMu.Lock()
	delete(p.pendingMessageText, conversationID)
	p.messageDedupeMu.Unlock()
}

func (p *DMPipeline) startTypingKeepalive(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}

	p.typingMu.Lock()
	if _, exists := p.typingCancel[conversationID]; exists {
		p.typingMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.typingCancel[conversationID] = cancel
	p.typingMu.Unlock()

	interval := dmTypingKeepaliveInterval
	if interval <= 0 {
		interval = dmTypingKeepaliveDefault
	}

	p.runBackground("typing_keepalive", conversationID, "", func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !p.IsConversationProcessing(conversationID) {
					return
				}
				p.sendTyping(conversationID, true)
			}
		}
	})
}

func (p *DMPipeline) stopTypingKeepalive(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}

	p.typingMu.Lock()
	cancel, exists := p.typingCancel[conversationID]
	if exists {
		delete(p.typingCancel, conversationID)
	}
	p.typingMu.Unlock()

	if exists {
		cancel()
	}
}

func (p *DMPipeline) runBackground(name string, conversationID string, sessionID sessionrt.SessionID, fn func()) {
	if fn == nil {
		return
	}
	go func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			slog.Error(
				"dm_pipeline: background panic",
				"task", strings.TrimSpace(name),
				"conversation_id", strings.TrimSpace(conversationID),
				"session_id", strings.TrimSpace(string(sessionID)),
				"panic", fmt.Sprint(recovered),
				"stack", string(debug.Stack()),
			)
			if strings.TrimSpace(conversationID) != "" {
				p.finishProcessing(conversationID)
			}
		}()
		fn()
	}()
}

func (p *DMPipeline) sendTyping(conversationID string, typing bool) {
	typingAPI, ok := p.transport.(typingTransport)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dmTypingTimeout)
	defer cancel()
	if senderTypingAPI, ok := p.transport.(typingSenderTransport); ok {
		senderID := p.recipientForConversation(conversationID)
		if err := senderTypingAPI.SendTypingAs(ctx, conversationID, senderID, typing); err != nil {
			slog.Warn("dm_pipeline: send dm typing failed",
				"conversation_id", conversationID,
				"sender_id", senderID,
				"typing", typing,
				"error", err,
			)
		}
		return
	}
	if err := typingAPI.SendTyping(ctx, conversationID, typing); err != nil {
		slog.Warn("dm_pipeline: send dm typing failed",
			"conversation_id", conversationID,
			"typing", typing,
			"error", err,
		)
	}
}

func (p *DMPipeline) sendDraftDelta(conversationID string, event sessionrt.Event) {
	delta := deltaTextFromPayload(event.Payload)
	if delta == "" {
		return
	}
	p.updateAndSendDraft(conversationID, event.SessionID, false, func(state *draftStreamState) bool {
		if p.thinkingEnabledForConversation(conversationID) && strings.TrimSpace(state.ThinkingText) != "" {
			return false
		}
		state.Text += delta
		return true
	})
}

func (p *DMPipeline) sendThinkingDraft(conversationID string, event sessionrt.Event) {
	if !p.thinkingEnabledForConversation(conversationID) {
		return
	}
	delta := deltaTextFromPayload(event.Payload)
	if delta == "" {
		return
	}
	p.updateAndSendDraft(conversationID, event.SessionID, false, func(state *draftStreamState) bool {
		state.ThinkingText += delta
		return true
	})
}

func (p *DMPipeline) sendToolCallDraft(conversationID string, event sessionrt.Event) {
	toolName := toolCallNameFromPayload(event.Payload)
	if toolName == "" {
		return
	}
	emoji := toolCallEmoji(toolName)
	p.updateAndSendDraft(conversationID, event.SessionID, true, func(state *draftStreamState) bool {
		if emoji == "" {
			return false
		}
		if len(state.ToolProgress) > 0 && state.ToolProgress[len(state.ToolProgress)-1] == emoji {
			return false
		}
		state.ToolProgress = append(state.ToolProgress, emoji)
		if len(state.ToolProgress) > 16 {
			state.ToolProgress = append([]string(nil), state.ToolProgress[len(state.ToolProgress)-16:]...)
		}
		return true
	})
}

func (p *DMPipeline) updateAndSendDraft(
	conversationID string,
	sessionID sessionrt.SessionID,
	force bool,
	update func(state *draftStreamState) bool,
) {
	streamer, ok := p.transport.(draftStreamingTransport)
	if !ok {
		return
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}

	now := p.now()
	var draftID int64
	var draftText string

	p.draftMu.Lock()
	state := p.draftState[conversationID]
	if state.Disabled {
		p.draftMu.Unlock()
		return
	}
	if state.DraftID <= 0 {
		p.draftSeq++
		state.DraftID = int64((p.draftSeq % (1<<31 - 1)) + 1)
	}
	if update != nil {
		if !update(&state) {
			p.draftState[conversationID] = state
			p.draftMu.Unlock()
			return
		}
	}
	draftText = draftTextForState(state)
	if draftText == "" {
		p.draftState[conversationID] = state
		p.draftMu.Unlock()
		return
	}
	if !force && !state.LastSentAt.IsZero() {
		charDelta := runeLen(draftText) - state.LastSentN
		if charDelta < dmDraftMinSendChars && now.Sub(state.LastSentAt) < dmDraftMinSendInterval {
			p.draftState[conversationID] = state
			p.draftMu.Unlock()
			return
		}
	}
	state.LastSentAt = now
	state.LastSentN = runeLen(draftText)
	p.draftState[conversationID] = state
	draftID = state.DraftID
	p.draftMu.Unlock()

	if err := streamer.SendMessageDraft(context.Background(), conversationID, draftID, draftText); err != nil {
		slog.Warn("dm_pipeline: send dm draft delta failed",
			"conversation_id", conversationID,
			"session_id", sessionID,
			"error", err,
		)
		p.draftMu.Lock()
		state := p.draftState[conversationID]
		state.Disabled = true
		p.draftState[conversationID] = state
		p.draftMu.Unlock()
	}
}

func (p *DMPipeline) clearDraftState(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	p.draftMu.Lock()
	delete(p.draftState, conversationID)
	p.draftMu.Unlock()
}

func deltaTextFromPayload(payload any) string {
	value, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	delta, _ := value["delta"].(string)
	return delta
}

func toolCallNameFromPayload(payload any) string {
	value, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	name, _ := value["name"].(string)
	return strings.TrimSpace(name)
}

func toolCallEmoji(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return dmToolEmojiFallback
	}
	switch {
	case strings.Contains(normalized, "search"):
		return "🔎"
	case strings.Contains(normalized, "fetch"), strings.Contains(normalized, "http"), strings.Contains(normalized, "web"):
		return "🌐"
	case strings.Contains(normalized, "exec"), strings.Contains(normalized, "process"), strings.Contains(normalized, "shell"), strings.Contains(normalized, "command"):
		return "🖥️"
	case strings.Contains(normalized, "read"), strings.Contains(normalized, "file"), strings.Contains(normalized, "fs"):
		return "📄"
	case strings.Contains(normalized, "write"), strings.Contains(normalized, "edit"), strings.Contains(normalized, "patch"):
		return "✍️"
	case strings.Contains(normalized, "memory"):
		return "🧠"
	case strings.Contains(normalized, "message"):
		return "💬"
	case strings.Contains(normalized, "reaction"):
		return "👍"
	case strings.Contains(normalized, "cron"), strings.Contains(normalized, "schedule"):
		return "⏰"
	case strings.Contains(normalized, "heartbeat"):
		return "💓"
	case strings.Contains(normalized, "delegate"):
		return "🤝"
	default:
		return dmToolEmojiFallback
	}
}

func draftTextForState(state draftStreamState) string {
	body := draftBodyForState(state)
	toolSummary := strings.TrimSpace(strings.Join(state.ToolProgress, " "))
	switch {
	case toolSummary == "":
		return trimDraftText(body)
	case strings.TrimSpace(body) == "":
		return trimDraftText(dmDraftToolPrefix + " " + toolSummary)
	default:
		return trimDraftText(dmDraftToolPrefix + " " + toolSummary + "\n\n" + body)
	}
}

func draftBodyForState(state draftStreamState) string {
	if thinking := strings.TrimSpace(sanitizeDraftBody(state.ThinkingText)); thinking != "" {
		return dmDraftThinkingPrefix + "\n\n" + thinking
	}
	return sanitizeDraftBody(state.Text)
}

func sanitizeDraftBody(text string) string {
	if text == "" {
		return ""
	}
	return strings.ReplaceAll(text, noReplyToken, "")
}

func trimDraftText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= dmDraftMaxChars {
		return trimmed
	}
	if dmDraftMaxChars <= 1 {
		return string(runes[:1])
	}
	return string(runes[:dmDraftMaxChars-1]) + "…"
}

func runeLen(text string) int {
	return len([]rune(text))
}

func (p *DMPipeline) isCurrentConversationSession(conversationID string, sessionID sessionrt.SessionID) bool {
	current, ok := p.conversations.Get(conversationID)
	return ok && current == sessionID
}

func (p *DMPipeline) lookupConversationSession(conversationID string) (sessionrt.SessionID, bool) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", false
	}
	if existing, ok := p.conversations.Get(conversationID); ok {
		return existing, true
	}
	if p.bindings == nil {
		return "", false
	}
	binding, ok := p.bindings.GetByConversation(conversationID)
	if !ok {
		return "", false
	}
	p.conversations.Set(conversationID, binding.SessionID)
	p.setConversationRoute(conversationID, binding.AgentID, binding.RecipientID, binding.Mode)
	p.setTraceConversationRoute(conversationID, binding.TraceConversationID)
	return binding.SessionID, true
}

func (p *DMPipeline) bindConversation(conversationID string, sessionID sessionrt.SessionID, agentID sessionrt.ActorID, recipientID string, mode ConversationMode, conversationName string) error {
	conversationID = strings.TrimSpace(conversationID)
	conversationName = strings.TrimSpace(conversationName)
	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	if conversationID == "" || strings.TrimSpace(string(sessionID)) == "" {
		return fmt.Errorf("conversation id and session id are required")
	}
	if strings.TrimSpace(string(agentID)) == "" {
		agentID = p.agentID
	}
	if strings.TrimSpace(recipientID) == "" {
		recipientID = strings.TrimSpace(p.recipByAgent[agentID])
	}
	mode = normalizeConversationMode(mode)
	if p.bindings != nil {
		if err := p.bindings.Set(ConversationBinding{
			ConversationID:   conversationID,
			ConversationName: conversationName,
			SessionID:        sessionID,
			AgentID:          agentID,
			RecipientID:      recipientID,
			Mode:             mode,
		}); err != nil {
			return err
		}
		if stored, ok := p.bindings.GetByConversation(conversationID); ok {
			p.setTraceConversationRoute(conversationID, stored.TraceConversationID)
		}
	} else {
		p.setTraceConversationRoute(conversationID, "")
	}
	p.conversations.Set(conversationID, sessionID)
	p.setConversationRoute(conversationID, agentID, recipientID, mode)
	return nil
}

func (p *DMPipeline) markInboundEventProcessed(conversationID string, sessionID sessionrt.SessionID, eventID string) {
	if p.bindings == nil {
		return
	}
	eventID = strings.TrimSpace(eventID)
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	binding, ok := p.bindings.GetByConversation(conversationID)
	if !ok {
		return
	}
	if eventID != "" && binding.LastInboundEvent == eventID && strings.TrimSpace(string(sessionID)) == strings.TrimSpace(string(binding.SessionID)) {
		return
	}
	if eventID != "" {
		binding.LastInboundEvent = eventID
	}
	if strings.TrimSpace(string(sessionID)) != "" {
		binding.SessionID = sessionID
	}
	if err := p.bindings.Set(binding); err != nil {
		slog.Warn("dm_pipeline: failed to persist inbound event checkpoint",
			"conversation_id", conversationID,
			"event_id", eventID,
			"error", err,
		)
	}
}

func (p *DMPipeline) BindConversation(conversationID string, sessionID sessionrt.SessionID, agentID sessionrt.ActorID, recipientID string, mode ConversationMode) error {
	if err := p.bindConversation(conversationID, sessionID, agentID, recipientID, mode, ""); err != nil {
		return err
	}
	return p.ensureSubscription(conversationID, sessionID)
}

func (p *DMPipeline) EnsureTraceConversation(ctx context.Context, req TraceConversationRequest) {
	p.ensureTraceConversationWithOptions(ctx, req, false)
}

func (p *DMPipeline) TraceConversationFor(conversationID string) (string, bool) {
	return p.traceConversationFor(conversationID)
}

func (p *DMPipeline) thinkingModeForConversation(conversationID string) string {
	if p == nil || p.bindings == nil {
		return ThinkingModeOff
	}
	binding, ok := p.bindings.GetByConversation(strings.TrimSpace(conversationID))
	if !ok {
		return ThinkingModeOff
	}
	mode := normalizeThinkingMode(binding.ThinkingMode)
	if mode == "" {
		return ThinkingModeOff
	}
	return mode
}

func (p *DMPipeline) thinkingEnabledForConversation(conversationID string) bool {
	return p.thinkingModeForConversation(conversationID) == ThinkingModeOn
}

func (p *DMPipeline) setThinkingModeForConversation(conversationID, mode string) error {
	if p == nil || p.bindings == nil {
		return fmt.Errorf("conversation bindings are unavailable")
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return fmt.Errorf("conversation id is required")
	}
	mode = normalizeThinkingMode(mode)
	if mode == "" {
		return errors.New("usage: /thinking <on|off|status>")
	}
	binding, ok := p.bindings.GetByConversation(conversationID)
	if !ok {
		return fmt.Errorf("conversation %q is not bound", conversationID)
	}
	binding.ThinkingMode = mode
	return p.bindings.Set(binding)
}

func (p *DMPipeline) ConversationForSession(sessionID sessionrt.SessionID) (string, bool) {
	if p.bindings == nil {
		conversations := p.conversations.Snapshot()
		for conversationID, currentSessionID := range conversations {
			if currentSessionID == sessionID {
				return conversationID, true
			}
		}
		return "", false
	}
	binding, ok := p.bindings.GetBySession(sessionID)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(binding.ConversationID), true
}

func (p *DMPipeline) LastInboundEventForSession(sessionID sessionrt.SessionID) (string, bool) {
	if p == nil || p.bindings == nil {
		return "", false
	}
	binding, ok := p.bindings.GetBySession(sessionID)
	if !ok {
		return "", false
	}
	eventID := strings.TrimSpace(binding.LastInboundEvent)
	if eventID == "" {
		return "", false
	}
	return eventID, true
}

func (p *DMPipeline) routeForInbound(recipientID string) (sessionrt.ActorID, string) {
	recipientNorm := normalizeRecipientID(recipientID)
	if recipientNorm != "" {
		if actorID, ok := p.agentByRecip[recipientNorm]; ok {
			return actorID, recipientNorm
		}
	}
	agentID := p.agentID
	fallbackRecipient := strings.TrimSpace(p.recipByAgent[agentID])
	if recipientNorm != "" {
		fallbackRecipient = recipientNorm
	}
	return agentID, fallbackRecipient
}

func (p *DMPipeline) setConversationRoute(conversationID string, agentID sessionrt.ActorID, recipientID string, mode ConversationMode) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	if strings.TrimSpace(string(agentID)) == "" {
		agentID = p.agentID
	}
	recipientID = strings.TrimSpace(recipientID)
	if recipientID == "" {
		recipientID = strings.TrimSpace(p.recipByAgent[agentID])
	}
	mode = normalizeConversationMode(mode)
	p.routeMu.Lock()
	defer p.routeMu.Unlock()
	p.routes[conversationID] = conversationRoute{
		AgentID:     agentID,
		RecipientID: recipientID,
		Mode:        mode,
	}
}

func (p *DMPipeline) currentRoute(conversationID string) (conversationRoute, bool) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return conversationRoute{}, false
	}
	p.routeMu.RLock()
	defer p.routeMu.RUnlock()
	route, ok := p.routes[conversationID]
	return route, ok
}

func (p *DMPipeline) recipientForConversation(conversationID string) string {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ""
	}
	route, ok := p.currentRoute(conversationID)
	if !ok {
		if p.bindings != nil {
			if binding, exists := p.bindings.GetByConversation(conversationID); exists {
				if strings.TrimSpace(binding.RecipientID) != "" {
					return strings.TrimSpace(binding.RecipientID)
				}
				if strings.TrimSpace(string(binding.AgentID)) != "" {
					return strings.TrimSpace(p.recipByAgent[binding.AgentID])
				}
			}
		}
		return ""
	}
	if strings.TrimSpace(route.RecipientID) != "" {
		return strings.TrimSpace(route.RecipientID)
	}
	if strings.TrimSpace(string(route.AgentID)) != "" {
		return strings.TrimSpace(p.recipByAgent[route.AgentID])
	}
	return ""
}

func (p *DMPipeline) SenderForConversation(conversationID string) string {
	return p.recipientForConversation(conversationID)
}

func (p *DMPipeline) senderForConversationEvent(conversationID string, eventFrom sessionrt.ActorID) string {
	from := sessionrt.ActorID(strings.TrimSpace(string(eventFrom)))
	if strings.TrimSpace(string(from)) != "" {
		if sender := strings.TrimSpace(p.recipByAgent[from]); sender != "" {
			return sender
		}
	}
	return p.recipientForConversation(conversationID)
}

func (p *DMPipeline) conversationModeFor(conversationID string) ConversationMode {
	route, ok := p.currentRoute(conversationID)
	if ok {
		return normalizeConversationMode(route.Mode)
	}
	if p.bindings != nil {
		if binding, ok := p.bindings.GetByConversation(conversationID); ok {
			return normalizeConversationMode(binding.Mode)
		}
	}
	return ConversationModeDM
}

func (p *DMPipeline) actorForManagedSender(senderID string) (sessionrt.ActorID, bool) {
	actor, ok := p.agentByRecip[normalizeRecipientID(senderID)]
	if !ok || strings.TrimSpace(string(actor)) == "" {
		return "", false
	}
	return actor, true
}

func (p *DMPipeline) CanDispatchHeartbeat(conversationID string, agentID sessionrt.ActorID) bool {
	conversationID = strings.TrimSpace(conversationID)
	agentID = sessionrt.ActorID(strings.TrimSpace(string(agentID)))
	if conversationID == "" || strings.TrimSpace(string(agentID)) == "" {
		return false
	}
	provider, ok := p.transport.(managedConversationUsersReader)
	if !ok {
		return true
	}
	recipientID := normalizeRecipientID(p.recipByAgent[agentID])
	if recipientID == "" {
		return false
	}
	managedUsers := provider.ManagedUsersForConversation(conversationID)
	for _, managedUserID := range managedUsers {
		if normalizeRecipientID(managedUserID) == recipientID {
			return true
		}
	}
	return false
}

func (p *DMPipeline) HeartbeatTargets() []HeartbeatTarget {
	if p.bindings == nil {
		conversations := p.conversations.Snapshot()
		if len(conversations) == 0 {
			return nil
		}
		out := make([]HeartbeatTarget, 0, len(conversations))
		for conversationID, sessionID := range conversations {
			conversationID = strings.TrimSpace(conversationID)
			if conversationID == "" || strings.TrimSpace(string(sessionID)) == "" {
				continue
			}
			out = append(out, HeartbeatTarget{
				ConversationID: conversationID,
				SessionID:      sessionID,
			})
		}
		return out
	}
	bindings := p.bindings.List()
	if len(bindings) == 0 {
		return nil
	}
	out := make([]HeartbeatTarget, 0, len(bindings))
	for _, binding := range bindings {
		conversationID := strings.TrimSpace(binding.ConversationID)
		sessionID := sessionrt.SessionID(strings.TrimSpace(string(binding.SessionID)))
		if conversationID == "" || strings.TrimSpace(string(sessionID)) == "" {
			continue
		}
		out = append(out, HeartbeatTarget{
			ConversationID: conversationID,
			SessionID:      sessionID,
		})
	}
	return out
}

func (p *DMPipeline) MarkHeartbeatPending(
	conversationID string,
	ackMaxChars int,
	sessionID sessionrt.SessionID,
	previousUpdatedAt time.Time,
	dispatchedAt time.Time,
) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	if ackMaxChars <= 0 {
		ackMaxChars = heartbeatAckDefaultChars
	}
	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	dispatchedAt = dispatchedAt.UTC()
	previousUpdatedAt = previousUpdatedAt.UTC()
	p.heartbeatMu.Lock()
	defer p.heartbeatMu.Unlock()
	state := p.heartbeats[conversationID]
	state.pending = append(state.pending, heartbeatPending{
		AckMaxChars:       ackMaxChars,
		SessionID:         sessionID,
		PreviousUpdatedAt: previousUpdatedAt,
		DispatchedAt:      dispatchedAt,
	})
	p.heartbeats[conversationID] = state
}

func (p *DMPipeline) UnmarkHeartbeatPending(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	p.heartbeatMu.Lock()
	defer p.heartbeatMu.Unlock()
	state, ok := p.heartbeats[conversationID]
	if !ok {
		return
	}
	count := len(state.pending)
	if count <= 1 {
		delete(p.heartbeats, conversationID)
		return
	}
	state.pending = append([]heartbeatPending(nil), state.pending[:count-1]...)
	p.heartbeats[conversationID] = state
}

func (p *DMPipeline) IsConversationProcessing(conversationID string) bool {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return false
	}
	p.processingMu.Lock()
	defer p.processingMu.Unlock()
	return p.processing[conversationID] > 0
}

func (p *DMPipeline) FilterHeartbeatOutbound(conversationID, text string) (normalized string, suppress bool, isHeartbeat bool) {
	result := p.consumeHeartbeatReply(conversationID, text)
	return result.Normalized, result.Suppress, result.IsHeartbeat
}

func (p *DMPipeline) consumeHeartbeatReply(conversationID, text string) heartbeatConsumeResult {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return heartbeatConsumeResult{Normalized: text}
	}

	var pending heartbeatPending
	hasPending := false
	p.heartbeatMu.Lock()
	state, ok := p.heartbeats[conversationID]
	if !ok || len(state.pending) == 0 {
		p.heartbeatMu.Unlock()
		return heartbeatConsumeResult{Normalized: text}
	}
	hasPending = true
	pending = state.pending[0]
	if len(state.pending) <= 1 {
		delete(p.heartbeats, conversationID)
	} else {
		state.pending = append([]heartbeatPending(nil), state.pending[1:]...)
		p.heartbeats[conversationID] = state
	}
	p.heartbeatMu.Unlock()

	ackMaxChars := pending.AckMaxChars
	if ackMaxChars <= 0 {
		ackMaxChars = heartbeatAckDefaultChars
	}
	normalized, hasToken := normalizeHeartbeatReply(text)
	if !hasToken {
		return heartbeatConsumeResult{
			Suppress:    false,
			Normalized:  text,
			IsHeartbeat: hasPending,
		}
	}
	if len([]rune(normalized)) <= ackMaxChars {
		p.restoreHeartbeatSessionUpdatedAt(pending)
		return heartbeatConsumeResult{
			Suppress:    true,
			Normalized:  "",
			IsHeartbeat: true,
		}
	}
	return heartbeatConsumeResult{
		Suppress:    false,
		Normalized:  normalized,
		IsHeartbeat: true,
	}
}

func normalizeHeartbeatReply(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	candidates := []string{trimmed}
	normalizedMarkup := stripHeartbeatMarkup(trimmed)
	if normalizedMarkup != trimmed {
		candidates = append(candidates, normalizedMarkup)
	}
	for _, candidate := range candidates {
		normalized, hasToken := stripHeartbeatTokenAtEdges(candidate)
		if hasToken {
			return normalized, true
		}
	}
	return trimmed, false
}

func stripHeartbeatMarkup(text string) string {
	normalized := heartbeatHTMLTagPattern.ReplaceAllString(text, " ")
	normalized = strings.ReplaceAll(normalized, "&nbsp;", " ")
	normalized = strings.ReplaceAll(normalized, "&NBSP;", " ")
	normalized = strings.TrimSpace(normalized)
	normalized = strings.TrimLeft(normalized, "*`~_")
	normalized = strings.TrimRight(normalized, "*`~_")
	return strings.TrimSpace(normalized)
}

func stripHeartbeatTokenAtEdges(text string) (string, bool) {
	normalized := strings.TrimSpace(text)
	if normalized == "" || !strings.Contains(normalized, heartbeatOKToken) {
		return normalized, false
	}
	stripped := false
	for {
		switch {
		case strings.HasPrefix(normalized, heartbeatOKToken):
			normalized = strings.TrimSpace(strings.TrimPrefix(normalized, heartbeatOKToken))
			stripped = true
		case heartbeatTokenSuffixPattern.MatchString(normalized):
			idx := strings.LastIndex(normalized, heartbeatOKToken)
			before := strings.TrimSpace(normalized[:idx])
			after := strings.TrimSpace(normalized[idx+len(heartbeatOKToken):])
			if before == "" {
				normalized = after
			} else {
				normalized = strings.TrimSpace(before + " " + after)
			}
			stripped = true
		default:
			return strings.Join(strings.Fields(normalized), " "), stripped
		}
	}
}

func (p *DMPipeline) restoreHeartbeatSessionUpdatedAt(pending heartbeatPending) {
	if pending.SessionID == "" || pending.PreviousUpdatedAt.IsZero() || pending.DispatchedAt.IsZero() {
		return
	}
	manager, ok := p.manager.(sessionRecordReadWriter)
	if !ok || manager == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	record, err := manager.GetSessionRecord(ctx, pending.SessionID)
	if err != nil {
		return
	}
	currentUpdatedAt := record.UpdatedAt.UTC()
	dispatchedAt := pending.DispatchedAt.UTC()
	previousUpdatedAt := pending.PreviousUpdatedAt.UTC()
	restoreTarget := previousUpdatedAt
	// Allow small clock skew between dispatch timestamp and persisted event timestamp.
	// If updated_at advanced beyond the grace window, preserve monotonic progression.
	if currentUpdatedAt.After(dispatchedAt.Add(heartbeatRestoreGrace)) && currentUpdatedAt.After(restoreTarget) {
		restoreTarget = currentUpdatedAt
	}
	if currentUpdatedAt.Equal(restoreTarget) {
		return
	}
	record.UpdatedAt = restoreTarget
	if err := manager.UpsertSessionRecord(ctx, record); err != nil {
		slog.Warn("dm_pipeline: failed to restore heartbeat updated_at",
			"session_id", pending.SessionID,
			"previous_updated_at", previousUpdatedAt,
			"current_updated_at", currentUpdatedAt,
			"restore_target", restoreTarget,
			"error", err,
		)
	}
}

func (p *DMPipeline) shouldSuppressDuplicateHeartbeat(conversationID, text string) bool {
	conversationID = strings.TrimSpace(conversationID)
	text = strings.TrimSpace(text)
	if conversationID == "" || text == "" || p.bindings == nil {
		return false
	}
	binding, ok := p.bindings.GetByConversation(conversationID)
	if !ok {
		return false
	}
	lastText := strings.TrimSpace(binding.LastHeartbeatText)
	if lastText == "" || lastText != text {
		return false
	}
	if binding.LastHeartbeatSentAt.IsZero() {
		return false
	}
	return time.Since(binding.LastHeartbeatSentAt.UTC()) < heartbeatDedupeWindow
}

func (p *DMPipeline) recordHeartbeatDelivery(conversationID, text string) {
	conversationID = strings.TrimSpace(conversationID)
	text = strings.TrimSpace(text)
	if conversationID == "" || text == "" || p.bindings == nil {
		return
	}
	binding, ok := p.bindings.GetByConversation(conversationID)
	if !ok {
		return
	}
	binding.LastHeartbeatText = text
	binding.LastHeartbeatSentAt = time.Now().UTC()
	if err := p.bindings.Set(binding); err != nil {
		slog.Warn("dm_pipeline: failed to persist heartbeat dedupe marker",
			"conversation_id", conversationID,
			"error", err,
		)
	}
}

func errorTextFromPayload(payload any) string {
	switch value := payload.(type) {
	case sessionrt.ErrorPayload:
		return strings.TrimSpace(value.Message)
	case map[string]any:
		raw, _ := value["message"].(string)
		return strings.TrimSpace(raw)
	default:
		return ""
	}
}

func controlPayloadFromAny(payload any) (sessionrt.ControlPayload, bool) {
	switch value := payload.(type) {
	case sessionrt.ControlPayload:
		return value, true
	case map[string]any:
		actionRaw, _ := value["action"].(string)
		reasonRaw, _ := value["reason"].(string)
		action := strings.TrimSpace(actionRaw)
		if action == "" {
			return sessionrt.ControlPayload{}, false
		}
		return sessionrt.ControlPayload{
			Action: action,
			Reason: strings.TrimSpace(reasonRaw),
		}, true
	default:
		return sessionrt.ControlPayload{}, false
	}
}

func fallbackReplyForError(message string) string {
	classification, detail := classifyDMError(message)
	if classification == dmErrorClassRateLimit {
		if detail == "" {
			detail = "rate limit (429)"
		}
		return appendFallbackDetail(dmRateLimitFallbackReply, detail)
	}
	return appendFallbackDetail(dmErrorFallbackReply, detail)
}

func fallbackErrorDetail(message string) string {
	_, detail := classifyDMError(message)
	return detail
}

func classifyDMError(message string) (dmErrorClass, string) {
	sanitized := sanitizeFallbackErrorDetail(message)
	if sanitized == "" {
		return dmErrorClassUnknown, ""
	}
	text := strings.ToLower(sanitized)
	switch {
	case strings.Contains(text, "rate limit"), strings.Contains(text, "429"), strings.Contains(text, "too many requests"):
		return dmErrorClassRateLimit, "rate limit (429)"
	case strings.Contains(text, "deadline exceeded"), strings.Contains(text, "timed out"), strings.Contains(text, "timeout"):
		return dmErrorClassTimeout, "request timed out"
	case strings.Contains(text, "connection refused"), strings.Contains(text, "upstream connect"), strings.Contains(text, "connection reset"), strings.Contains(text, "dial tcp"), strings.Contains(text, "no such host"), strings.Contains(text, "network unreachable"), strings.Contains(text, "broken pipe"):
		return dmErrorClassConnection, "connection to provider failed"
	case strings.Contains(text, "service unavailable"), strings.Contains(text, "overloaded"), strings.Contains(text, "502"), strings.Contains(text, "503"), strings.Contains(text, "500"), strings.Contains(text, "bad gateway"):
		return dmErrorClassProviderUnavailable, "provider service unavailable"
	case strings.Contains(text, "unauthorized"), strings.Contains(text, "invalid api key"), strings.Contains(text, "401"):
		return dmErrorClassAuth, "provider authentication failed (401)"
	case strings.Contains(text, "forbidden"), strings.Contains(text, "403"):
		return dmErrorClassForbidden, "provider rejected the request (403)"
	case strings.Contains(text, "model not found"), strings.Contains(text, "404"):
		return dmErrorClassModelNotFound, "model not found (404)"
	case strings.Contains(text, "validation failed"), strings.Contains(text, "required field"), strings.Contains(text, "invalid argument"), strings.Contains(text, "schema"):
		return dmErrorClassValidation, "request validation failed"
	}
	return dmErrorClassUnknown, sanitized
}

func shouldAttemptLLMErrorRecovery(message string) bool {
	classification, detail := classifyDMError(message)
	switch classification {
	case dmErrorClassTimeout, dmErrorClassConnection, dmErrorClassProviderUnavailable:
		return true
	case dmErrorClassRateLimit, dmErrorClassAuth, dmErrorClassForbidden, dmErrorClassModelNotFound, dmErrorClassValidation:
		return false
	default:
		text := strings.ToLower(strings.TrimSpace(detail))
		return strings.Contains(text, "temporary") || strings.Contains(text, "try again later")
	}
}

func sanitizeFallbackErrorDetail(message string) string {
	detail := strings.TrimSpace(message)
	if detail == "" {
		return ""
	}
	detail = strings.Join(strings.Fields(detail), " ")
	detail = fallbackAuthBearerPattern.ReplaceAllString(detail, "authorization=[redacted]")
	detail = fallbackBearerPattern.ReplaceAllString(detail, "bearer [redacted]")
	detail = fallbackSensitiveValuePattern.ReplaceAllString(detail, "$1=[redacted]")
	detail = fallbackLongTokenPattern.ReplaceAllString(detail, "[redacted]")
	if len([]rune(detail)) > dmFallbackDetailMaxChars {
		runes := []rune(detail)
		detail = strings.TrimSpace(string(runes[:dmFallbackDetailMaxChars])) + "..."
	}
	return strings.TrimSpace(detail)
}

func appendFallbackDetail(base, detail string) string {
	base = strings.TrimSpace(base)
	detail = strings.TrimSpace(strings.TrimRight(detail, "."))
	if base == "" || detail == "" {
		return base
	}
	base = strings.TrimSuffix(base, ".")
	return base + " Details: " + detail + "."
}

func (p *DMPipeline) sendErrorFallback(conversationID, senderID, rawErr string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}

	now := time.Now().UTC()
	p.fallbackMu.Lock()
	last, ok := p.lastFallback[conversationID]
	if ok && now.Sub(last) < dmFallbackMinInterval {
		p.fallbackMu.Unlock()
		return
	}
	p.lastFallback[conversationID] = now
	p.fallbackMu.Unlock()

	reply := fallbackReplyForError(rawErr)
	slog.Warn("dm_pipeline: sending error fallback",
		"conversation_id", conversationID,
		"error_message", rawErr,
	)
	if err := p.sendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: conversationID,
		SenderID:       strings.TrimSpace(senderID),
		Text:           reply,
	}); err != nil {
		slog.Error("dm_pipeline: send dm error fallback failed",
			"conversation_id", conversationID,
			"error", err,
		)
	}
}

func (p *DMPipeline) shouldNotifyTerminalErrorFallback(conversationID string, sessionID sessionrt.SessionID) bool {
	if p == nil || p.manager == nil {
		return false
	}
	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	if sessionID == "" {
		conversationID = strings.TrimSpace(conversationID)
		if conversationID == "" {
			return false
		}
		currentSessionID, ok := p.lookupConversationSession(conversationID)
		if !ok {
			return false
		}
		sessionID = currentSessionID
	}
	loaded, err := p.manager.GetSession(context.Background(), sessionID)
	if err != nil || loaded == nil {
		return false
	}
	return loaded.Status == sessionrt.SessionFailed
}

func (p *DMPipeline) isConversationSessionInFlight(conversationID string, sessionID sessionrt.SessionID) bool {
	if p == nil || p.manager == nil {
		return false
	}
	manager, ok := p.manager.(sessionRecordReadWriter)
	if !ok || manager == nil {
		return false
	}

	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	if sessionID == "" {
		conversationID = strings.TrimSpace(conversationID)
		if conversationID == "" {
			return false
		}
		currentSessionID, exists := p.lookupConversationSession(conversationID)
		if !exists {
			return false
		}
		sessionID = currentSessionID
	}
	if strings.TrimSpace(string(sessionID)) == "" {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	record, err := manager.GetSessionRecord(ctx, sessionID)
	if err != nil {
		return false
	}
	return record.InFlight
}

func (p *DMPipeline) ensureTraceConversation(ctx context.Context, req TraceConversationRequest) {
	p.ensureTraceConversationWithOptions(ctx, req, false)
}

func (p *DMPipeline) ensureTraceConversationWithOptions(ctx context.Context, req TraceConversationRequest, force bool) {
	if p == nil || p.traceProvisioner == nil {
		return
	}
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	req.ConversationName = strings.TrimSpace(req.ConversationName)
	req.SessionID = sessionrt.SessionID(strings.TrimSpace(string(req.SessionID)))
	req.AgentID = sessionrt.ActorID(strings.TrimSpace(string(req.AgentID)))
	req.SenderID = strings.TrimSpace(req.SenderID)
	req.RecipientID = strings.TrimSpace(req.RecipientID)
	req.TriggerEventID = strings.TrimSpace(req.TriggerEventID)
	if req.ConversationID == "" || strings.TrimSpace(string(req.SessionID)) == "" {
		return
	}
	if p.traceModeForConversation(req.ConversationID) == TraceModeOff {
		return
	}
	if _, exists := p.traceConversationFor(req.ConversationID); exists {
		return
	}

	if !p.beginTraceProvision(req.ConversationID, force) {
		return
	}
	provisionSucceeded := false
	defer func() {
		p.finishTraceProvision(req.ConversationID, provisionSucceeded)
	}()

	created, err := p.traceProvisioner.CreateTraceConversation(ctx, req)
	if err != nil {
		slog.Warn("dm_pipeline: failed to create trace conversation",
			"conversation_id", req.ConversationID,
			"session_id", req.SessionID,
			"error", err,
		)
		return
	}
	if strings.TrimSpace(created.ConversationID) == "" {
		return
	}
	if err := p.setConversationTraceBinding(req.ConversationID, created); err != nil {
		slog.Warn("dm_pipeline: failed to persist trace conversation",
			"conversation_id", req.ConversationID,
			"session_id", req.SessionID,
			"trace_conversation_id", created.ConversationID,
			"error", err,
		)
		return
	}
	provisionSucceeded = true
	p.sendTraceConversationReadyNotice(req.ConversationID, created.ConversationID, req.TriggerEventID)
}

func (p *DMPipeline) beginTraceProvision(conversationID string, force bool) bool {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return false
	}
	p.traceSetupMu.Lock()
	defer p.traceSetupMu.Unlock()
	state := p.traceSetup[conversationID]
	if state.InFlight {
		return false
	}
	if !force && !state.LastFailureAt.IsZero() && time.Since(state.LastFailureAt) < traceProvisionBackoff {
		return false
	}
	state.InFlight = true
	p.traceSetup[conversationID] = state
	return true
}

func (p *DMPipeline) finishTraceProvision(conversationID string, success bool) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	p.traceSetupMu.Lock()
	defer p.traceSetupMu.Unlock()
	state, ok := p.traceSetup[conversationID]
	if !ok {
		return
	}
	if success {
		delete(p.traceSetup, conversationID)
		return
	}
	state.InFlight = false
	state.LastFailureAt = time.Now().UTC()
	p.traceSetup[conversationID] = state
}

func (p *DMPipeline) setConversationTraceBinding(conversationID string, trace TraceConversationBinding) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return fmt.Errorf("conversation id is required")
	}
	traceID := strings.TrimSpace(trace.ConversationID)
	traceName := strings.TrimSpace(trace.ConversationName)
	traceMode := normalizeTraceMode(trace.Mode)
	traceRender := normalizeTraceRender(trace.Render)
	if traceID != "" {
		if traceMode == "" {
			traceMode = TraceModeReadOnly
		}
		if traceRender == "" {
			traceRender = TraceRenderCards
		}
	}

	if p.bindings == nil {
		p.setTraceConversationRoute(conversationID, traceID)
		return nil
	}
	binding, ok := p.bindings.GetByConversation(conversationID)
	if !ok {
		return fmt.Errorf("conversation binding not found")
	}
	binding.TraceConversationID = traceID
	binding.TraceConversationName = traceName
	binding.TraceMode = traceMode
	binding.TraceRender = traceRender
	if err := p.bindings.Set(binding); err != nil {
		return err
	}
	p.setTraceConversationRoute(conversationID, traceID)
	return nil
}

func (p *DMPipeline) setTraceModeForConversation(conversationID string, mode string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return fmt.Errorf("conversation id is required")
	}
	mode = normalizeTraceMode(mode)
	if mode == "" {
		mode = TraceModeReadOnly
	}
	if p.bindings == nil {
		return nil
	}
	binding, ok := p.bindings.GetByConversation(conversationID)
	if !ok {
		return fmt.Errorf("conversation binding not found")
	}
	binding.TraceMode = mode
	if err := p.bindings.Set(binding); err != nil {
		return err
	}
	p.setTraceConversationRoute(conversationID, binding.TraceConversationID)
	return nil
}

func (p *DMPipeline) traceModeForConversation(conversationID string) string {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return TraceModeReadOnly
	}
	if p.bindings != nil {
		if binding, ok := p.bindings.GetByConversation(conversationID); ok {
			if mode := normalizeTraceMode(binding.TraceMode); mode != "" {
				return mode
			}
		}
	}
	return TraceModeReadOnly
}

func (p *DMPipeline) publishTraceEvent(conversationID string, event sessionrt.Event) error {
	if p == nil || p.tracePublisher == nil {
		return nil
	}
	if p.traceModeForConversation(conversationID) == TraceModeOff {
		return nil
	}
	traceConversationID, ok := p.traceConversationFor(conversationID)
	if !ok {
		return nil
	}
	return p.tracePublisher.PublishEvent(context.Background(), traceConversationID, event)
}

func (p *DMPipeline) setTraceConversationRoute(conversationID, traceConversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	traceConversationID = strings.TrimSpace(traceConversationID)
	p.traceRouteMu.Lock()
	defer p.traceRouteMu.Unlock()
	if existingTrace, ok := p.traceByDM[conversationID]; ok && existingTrace != "" {
		delete(p.dmByTrace, existingTrace)
	}
	if traceConversationID == "" {
		delete(p.traceByDM, conversationID)
		return
	}
	p.traceByDM[conversationID] = traceConversationID
	p.dmByTrace[traceConversationID] = conversationID
}

func (p *DMPipeline) traceConversationFor(conversationID string) (string, bool) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", false
	}
	p.traceRouteMu.RLock()
	defer p.traceRouteMu.RUnlock()
	traceConversationID, ok := p.traceByDM[conversationID]
	if !ok || strings.TrimSpace(traceConversationID) == "" {
		return "", false
	}
	return traceConversationID, true
}

func (p *DMPipeline) isTraceConversation(conversationID string) bool {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return false
	}
	p.traceRouteMu.RLock()
	defer p.traceRouteMu.RUnlock()
	_, ok := p.dmByTrace[conversationID]
	return ok
}

type traceInboundIgnoredRecorder interface {
	RecordTraceInboundIgnored()
}

func (p *DMPipeline) recordTraceInboundIgnored() {
	if p == nil || p.transport == nil {
		return
	}
	recorder, ok := p.transport.(traceInboundIgnoredRecorder)
	if !ok {
		return
	}
	recorder.RecordTraceInboundIgnored()
}

func (p *DMPipeline) sendTraceConversationReadyNotice(conversationID, traceConversationID, triggerEventID string) {
	conversationID = strings.TrimSpace(conversationID)
	traceConversationID = strings.TrimSpace(traceConversationID)
	triggerEventID = strings.TrimSpace(triggerEventID)
	if conversationID == "" || traceConversationID == "" {
		return
	}
	if p == nil || p.transport == nil {
		return
	}
	senderID := p.recipientForConversation(conversationID)
	message := traceConversationReadyMessage(traceConversationID)
	if strings.TrimSpace(message) == "" {
		return
	}
	if err := p.sendMessage(context.Background(), transport.OutboundMessage{
		ConversationID:    conversationID,
		SenderID:          senderID,
		Text:              message,
		ThreadRootEventID: triggerEventID,
	}); err != nil {
		slog.Warn("dm_pipeline: failed to send trace conversation notice",
			"conversation_id", conversationID,
			"trace_conversation_id", traceConversationID,
			"error", err,
		)
	}
}

func (p *DMPipeline) sendTraceCommandReply(conversationID, senderID, triggerEventID, text string) {
	p.sendCommandReply(conversationID, senderID, triggerEventID, text)
}

func thinkingStatusReply(mode string) string {
	if normalizeThinkingMode(mode) == ThinkingModeOn {
		return "Thinking stream is on for this conversation."
	}
	return "Thinking stream is off for this conversation."
}

func (p *DMPipeline) sendCommandReply(conversationID, senderID, triggerEventID, text string) {
	conversationID = strings.TrimSpace(conversationID)
	senderID = strings.TrimSpace(senderID)
	triggerEventID = strings.TrimSpace(triggerEventID)
	text = strings.TrimSpace(text)
	if conversationID == "" || text == "" || p == nil || p.transport == nil {
		return
	}
	if err := p.sendMessage(context.Background(), transport.OutboundMessage{
		ConversationID:    conversationID,
		SenderID:          senderID,
		Text:              text,
		ThreadRootEventID: triggerEventID,
	}); err != nil {
		slog.Warn("dm_pipeline: failed to send command reply",
			"conversation_id", conversationID,
			"error", err,
		)
	}
}

func (p *DMPipeline) withConversationLane(conversationID string, fn func() error) error {
	if fn == nil {
		return nil
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return fn()
	}
	lane := p.conversationLane(conversationID)
	lane.Lock()
	defer lane.Unlock()
	return fn()
}

func (p *DMPipeline) conversationLane(conversationID string) *sync.Mutex {
	p.laneMu.Lock()
	defer p.laneMu.Unlock()
	if p.laneByConversation == nil {
		p.laneByConversation = map[string]*sync.Mutex{}
	}
	lane, ok := p.laneByConversation[conversationID]
	if !ok {
		lane = &sync.Mutex{}
		p.laneByConversation[conversationID] = lane
	}
	return lane
}

func (p *DMPipeline) outboundLane(conversationID string) *sync.Mutex {
	p.outboundLaneMu.Lock()
	defer p.outboundLaneMu.Unlock()
	if p.outboundLaneByConv == nil {
		p.outboundLaneByConv = map[string]*sync.Mutex{}
	}
	lane, ok := p.outboundLaneByConv[conversationID]
	if !ok {
		lane = &sync.Mutex{}
		p.outboundLaneByConv[conversationID] = lane
	}
	return lane
}

func (p *DMPipeline) sendMessage(ctx context.Context, message transport.OutboundMessage) error {
	if p == nil || p.transport == nil {
		return fmt.Errorf("transport is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	conversationID := strings.TrimSpace(message.ConversationID)
	atomic.AddInt64(&p.outboundPending, 1)
	defer atomic.AddInt64(&p.outboundPending, -1)
	if conversationID == "" {
		return p.transport.SendMessage(ctx, message)
	}
	lane := p.outboundLane(conversationID)
	lane.Lock()
	defer lane.Unlock()
	return p.transport.SendMessage(ctx, message)
}

func (p *DMPipeline) PendingOutboundReplies() int64 {
	if p == nil {
		return 0
	}
	return atomic.LoadInt64(&p.outboundPending)
}

func (p *DMPipeline) WaitForOutboundIdle(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if p.PendingOutboundReplies() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func traceConversationReadyMessage(traceConversationID string) string {
	traceConversationID = strings.TrimSpace(traceConversationID)
	if traceConversationID == "" {
		return ""
	}
	return "Trace channel (read-only): " + traceConversationID
}

func externalActorID(sender string) sessionrt.ActorID {
	sender = strings.TrimSpace(sender)
	if sender == "" {
		return "external:unknown"
	}
	return sessionrt.ActorID("external:" + sender)
}

func normalizeRecipientAgents(in map[string]sessionrt.ActorID) map[string]sessionrt.ActorID {
	out := map[string]sessionrt.ActorID{}
	for recipientID, actorID := range in {
		recip := normalizeRecipientID(recipientID)
		actor := sessionrt.ActorID(strings.TrimSpace(string(actorID)))
		if recip == "" || strings.TrimSpace(string(actor)) == "" {
			continue
		}
		out[recip] = actor
	}
	return out
}

func normalizeAgentRecipients(in map[sessionrt.ActorID]string) map[sessionrt.ActorID]string {
	out := map[sessionrt.ActorID]string{}
	for actorID, recipientID := range in {
		actor := sessionrt.ActorID(strings.TrimSpace(string(actorID)))
		recip := normalizeRecipientID(recipientID)
		if strings.TrimSpace(string(actor)) == "" || recip == "" {
			continue
		}
		out[actor] = recip
	}
	return out
}

func normalizeRecipientID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToLower(value)
}
