package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

const defaultTraceMessageMaxChars = 3500
const traceProgressDeltaMaxChars = 180

type TracePublisher interface {
	PublishEvent(ctx context.Context, traceConversationID string, event sessionrt.Event) error
}

type traceMessageSender interface {
	SendMessage(ctx context.Context, message transport.OutboundMessage) error
}

type traceMessageSenderWithResult interface {
	SendMessageWithResult(ctx context.Context, message transport.OutboundMessage) (transport.OutboundSendResult, error)
}

type tracePublishMetrics interface {
	RecordTracePublishSuccess()
	RecordTracePublishFailure()
}

type TraceEventPublisher struct {
	sender                traceMessageSender
	maxChars              int
	includeProgressDeltas bool
	mu                    sync.Mutex
	threads               map[sessionrt.SessionID]string
	deltaPosted           map[sessionrt.SessionID]bool
}

type TraceEventPublisherOptions struct {
	MaxChars              int
	IncludeProgressDeltas bool
}

func NewTraceEventPublisher(sender traceMessageSender) *TraceEventPublisher {
	return NewTraceEventPublisherWithOptions(sender, TraceEventPublisherOptions{
		MaxChars: defaultTraceMessageMaxChars,
	})
}

func NewTraceEventPublisherWithMaxChars(sender traceMessageSender, maxChars int) *TraceEventPublisher {
	return NewTraceEventPublisherWithOptions(sender, TraceEventPublisherOptions{
		MaxChars: maxChars,
	})
}

func NewTraceEventPublisherWithOptions(sender traceMessageSender, opts TraceEventPublisherOptions) *TraceEventPublisher {
	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = defaultTraceMessageMaxChars
	}
	return &TraceEventPublisher{
		sender:                sender,
		maxChars:              maxChars,
		includeProgressDeltas: opts.IncludeProgressDeltas,
		threads:               map[sessionrt.SessionID]string{},
		deltaPosted:           map[sessionrt.SessionID]bool{},
	}
}

func (p *TraceEventPublisher) PublishEvent(ctx context.Context, traceConversationID string, event sessionrt.Event) error {
	if p == nil || p.sender == nil {
		return nil
	}
	traceConversationID = strings.TrimSpace(traceConversationID)
	if traceConversationID == "" {
		return nil
	}
	isUserTrigger := traceIsUserTriggerEvent(event)
	if isUserTrigger {
		p.clearSessionThreadRoot(event.SessionID)
		p.clearSessionDeltaPosted(event.SessionID)
	}
	if event.Type == sessionrt.EventAgentDelta {
		if !p.includeProgressDeltas || p.sessionDeltaPosted(event.SessionID) {
			return nil
		}
	} else if shouldSuppressTraceEvent(event) {
		return nil
	}

	var messages []string
	if event.Type == sessionrt.EventAgentDelta {
		messages = renderTraceProgressDelta(event, p.maxChars)
	} else {
		messages = renderTraceEventCards(event, p.maxChars)
	}
	if len(messages) == 0 {
		return nil
	}
	threadRoot := ""
	if !isUserTrigger {
		threadRoot = p.sessionThreadRoot(event.SessionID)
	}
	for _, text := range messages {
		outbound := transport.OutboundMessage{
			ConversationID:    traceConversationID,
			Text:              text,
			ThreadRootEventID: threadRoot,
		}
		sendResult, err := p.sendTraceMessage(ctx, outbound)
		if err != nil {
			if metrics, ok := p.sender.(tracePublishMetrics); ok {
				metrics.RecordTracePublishFailure()
			}
			return err
		}
		if isUserTrigger && threadRoot == "" {
			threadRoot = strings.TrimSpace(sendResult.EventID)
			if threadRoot != "" {
				p.setSessionThreadRoot(event.SessionID, threadRoot)
			}
		}
	}
	if event.Type == sessionrt.EventAgentDelta {
		p.markSessionDeltaPosted(event.SessionID)
	}
	if metrics, ok := p.sender.(tracePublishMetrics); ok {
		metrics.RecordTracePublishSuccess()
	}
	return nil
}

func (p *TraceEventPublisher) sendTraceMessage(ctx context.Context, message transport.OutboundMessage) (transport.OutboundSendResult, error) {
	if senderWithResult, ok := p.sender.(traceMessageSenderWithResult); ok {
		return senderWithResult.SendMessageWithResult(ctx, message)
	}
	if err := p.sender.SendMessage(ctx, message); err != nil {
		return transport.OutboundSendResult{}, err
	}
	return transport.OutboundSendResult{}, nil
}

func (p *TraceEventPublisher) sessionThreadRoot(sessionID sessionrt.SessionID) string {
	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	if p == nil || sessionID == "" {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(p.threads[sessionID])
}

func (p *TraceEventPublisher) setSessionThreadRoot(sessionID sessionrt.SessionID, eventID string) {
	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	eventID = strings.TrimSpace(eventID)
	if p == nil || sessionID == "" || eventID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.threads[sessionID] = eventID
}

func (p *TraceEventPublisher) clearSessionThreadRoot(sessionID sessionrt.SessionID) {
	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.threads, sessionID)
}

func (p *TraceEventPublisher) sessionDeltaPosted(sessionID sessionrt.SessionID) bool {
	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	if p == nil || sessionID == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deltaPosted[sessionID]
}

func (p *TraceEventPublisher) markSessionDeltaPosted(sessionID sessionrt.SessionID) {
	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deltaPosted[sessionID] = true
}

func (p *TraceEventPublisher) clearSessionDeltaPosted(sessionID sessionrt.SessionID) {
	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.deltaPosted, sessionID)
}

func renderTraceEventCards(event sessionrt.Event, maxChars int) []string {
	if maxChars <= 0 {
		maxChars = defaultTraceMessageMaxChars
	}
	eventType := strings.TrimSpace(string(event.Type))
	if eventType == "" {
		eventType = "unknown"
	}
	from := strings.TrimSpace(string(event.From))
	if from == "" {
		from = string(sessionrt.SystemActorID)
	}
	timestamp := "-"
	if !event.Timestamp.IsZero() {
		timestamp = event.Timestamp.UTC().Format(time.RFC3339)
	}
	header := fmt.Sprintf("%s [#%d] %s • %s • %s", traceEventEmoji(event), event.Seq, eventType, from, timestamp)
	body := renderTraceEventBody(event)
	return splitTraceCard(header, body, maxChars)
}

func shouldSuppressTraceEvent(event sessionrt.Event) bool {
	switch event.Type {
	case sessionrt.EventAgentThinkingDelta:
		return true
	default:
		return false
	}
}

func renderTraceProgressDelta(event sessionrt.Event, maxChars int) []string {
	eventType := strings.TrimSpace(string(event.Type))
	if eventType == "" {
		eventType = "unknown"
	}
	from := strings.TrimSpace(string(event.From))
	if from == "" {
		from = string(sessionrt.SystemActorID)
	}
	timestamp := "-"
	if !event.Timestamp.IsZero() {
		timestamp = event.Timestamp.UTC().Format(time.RFC3339)
	}
	header := fmt.Sprintf("%s [#%d] %s • %s • %s", traceEventEmoji(event), event.Seq, eventType, from, timestamp)

	delta := strings.TrimSpace(tracePayloadString(tracePayloadMap(event.Payload), "delta"))
	delta = strings.Join(strings.Fields(delta), " ")
	if delta == "" {
		delta = "Working."
	}
	runes := []rune(delta)
	if len(runes) > traceProgressDeltaMaxChars {
		delta = strings.TrimSpace(string(runes[:traceProgressDeltaMaxChars-3])) + "..."
	}
	body := "progress: " + delta
	return splitTraceCard(header, body, maxChars)
}

func traceIsUserTriggerEvent(event sessionrt.Event) bool {
	if event.Type != sessionrt.EventMessage {
		return false
	}
	msg, ok := traceMessageFromPayload(event.Payload)
	if !ok {
		return false
	}
	return msg.Role == sessionrt.RoleUser
}

func traceEventEmoji(event sessionrt.Event) string {
	switch event.Type {
	case sessionrt.EventMessage:
		msg, ok := traceMessageFromPayload(event.Payload)
		if !ok {
			return "💬"
		}
		switch msg.Role {
		case sessionrt.RoleUser:
			return "🧑"
		case sessionrt.RoleAgent:
			return "🤖"
		case sessionrt.RoleSystem:
			return "🖥️"
		default:
			return "💬"
		}
	case sessionrt.EventToolCall:
		return "🛠️"
	case sessionrt.EventToolResult:
		status := strings.ToLower(strings.TrimSpace(tracePayloadString(tracePayloadMap(event.Payload), "status")))
		if strings.Contains(status, "fail") || strings.Contains(status, "error") || strings.Contains(status, "timeout") {
			return "❌"
		}
		if strings.Contains(status, "ok") || strings.Contains(status, "success") || strings.Contains(status, "done") || strings.Contains(status, "complete") || status == "" {
			return "✅"
		}
		return "📦"
	case sessionrt.EventControl:
		return "⚙️"
	case sessionrt.EventError:
		return "🚨"
	case sessionrt.EventAgentStart:
		return "▶️"
	case sessionrt.EventAgentStop:
		return "⏹️"
	case sessionrt.EventStatePatch:
		return "🧩"
	case sessionrt.EventAgentDelta:
		return "✏️"
	case sessionrt.EventAgentThinkingDelta:
		return "🧠"
	default:
		return "📌"
	}
}

func renderTraceEventBody(event sessionrt.Event) string {
	switch event.Type {
	case sessionrt.EventMessage:
		msg, ok := traceMessageFromPayload(event.Payload)
		if !ok {
			return "payload:\n" + indentBlock(formatTraceValue(event.Payload), "  ")
		}
		lines := []string{
			"role: " + strings.TrimSpace(string(msg.Role)),
		}
		if strings.TrimSpace(string(msg.TargetActorID)) != "" {
			lines = append(lines, "target_actor_id: "+strings.TrimSpace(string(msg.TargetActorID)))
		}
		lines = append(lines, "content:")
		lines = append(lines, indentBlock(msg.Content, "  "))
		return strings.Join(lines, "\n")
	case sessionrt.EventToolCall:
		payload := tracePayloadMap(event.Payload)
		name := strings.TrimSpace(tracePayloadString(payload, "name"))
		args := tracePayloadAny(payload, "args")
		lines := []string{"tool: " + valueOrFallback(name, "-")}
		lines = append(lines, "args:")
		lines = append(lines, indentBlock(formatTraceValue(args), "  "))
		return strings.Join(lines, "\n")
	case sessionrt.EventToolResult:
		payload := tracePayloadMap(event.Payload)
		name := strings.TrimSpace(tracePayloadString(payload, "name"))
		status := strings.TrimSpace(tracePayloadString(payload, "status"))
		result := tracePayloadAny(payload, "result")
		lines := []string{
			"tool: " + valueOrFallback(name, "-"),
			"status: " + valueOrFallback(status, "-"),
			"result:",
			indentBlock(formatTraceValue(result), "  "),
		}
		return strings.Join(lines, "\n")
	case sessionrt.EventAgentDelta, sessionrt.EventAgentThinkingDelta:
		payload := tracePayloadMap(event.Payload)
		delta := strings.TrimSpace(tracePayloadString(payload, "delta"))
		return "delta:\n" + indentBlock(valueOrFallback(delta, "-"), "  ")
	case sessionrt.EventControl:
		payload := tracePayloadMap(event.Payload)
		action := strings.TrimSpace(tracePayloadString(payload, "action"))
		reason := strings.TrimSpace(tracePayloadString(payload, "reason"))
		metadata := tracePayloadAny(payload, "metadata")
		lines := []string{"action: " + valueOrFallback(action, "-")}
		if reason != "" {
			lines = append(lines, "reason: "+reason)
		}
		if metadata != nil {
			lines = append(lines, "metadata:")
			lines = append(lines, indentBlock(formatTraceValue(metadata), "  "))
		}
		return strings.Join(lines, "\n")
	case sessionrt.EventError:
		msg := traceErrorMessage(event.Payload)
		return "error:\n" + indentBlock(valueOrFallback(msg, "-"), "  ")
	default:
		return "payload:\n" + indentBlock(formatTraceValue(event.Payload), "  ")
	}
}

func splitTraceCard(header, body string, maxChars int) []string {
	header = strings.TrimSpace(header)
	body = strings.TrimSpace(body)
	if maxChars <= 0 {
		maxChars = defaultTraceMessageMaxChars
	}
	if body == "" {
		return splitTraceText(header, maxChars)
	}
	if traceRuneLen(header)+1+traceRuneLen(body) <= maxChars {
		return []string{header + "\n" + body}
	}
	perPartBodyLimit := maxChars - traceRuneLen(header) - 32
	if perPartBodyLimit < 64 {
		perPartBodyLimit = 64
	}
	bodyParts := splitTraceText(body, perPartBodyLimit)
	if len(bodyParts) == 1 {
		return []string{header + "\n" + bodyParts[0]}
	}
	out := make([]string, 0, len(bodyParts))
	total := len(bodyParts)
	for idx, part := range bodyParts {
		partHeader := fmt.Sprintf("%s (%d/%d)", header, idx+1, total)
		limit := maxChars - traceRuneLen(partHeader) - 1
		if limit < 32 {
			limit = 32
		}
		for _, split := range splitTraceText(part, limit) {
			out = append(out, partHeader+"\n"+split)
		}
	}
	return out
}

func splitTraceText(text string, maxRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if maxRunes <= 0 {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}
	}
	out := make([]string, 0, (len(runes)/maxRunes)+1)
	for start := 0; start < len(runes); start += maxRunes {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, strings.TrimSpace(string(runes[start:end])))
	}
	return out
}

func traceRuneLen(value string) int {
	return len([]rune(value))
}

func traceMessageFromPayload(payload any) (sessionrt.Message, bool) {
	switch value := payload.(type) {
	case sessionrt.Message:
		return value, true
	case map[string]any:
		roleRaw, _ := value["role"].(string)
		contentRaw, _ := value["content"].(string)
		if strings.TrimSpace(roleRaw) == "" && strings.TrimSpace(contentRaw) == "" {
			return sessionrt.Message{}, false
		}
		targetRaw, _ := value["target_actor_id"].(string)
		return sessionrt.Message{
			Role:          sessionrt.Role(strings.TrimSpace(roleRaw)),
			Content:       contentRaw,
			TargetActorID: sessionrt.ActorID(strings.TrimSpace(targetRaw)),
		}, true
	default:
		return sessionrt.Message{}, false
	}
}

func traceErrorMessage(payload any) string {
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

func tracePayloadMap(payload any) map[string]any {
	switch value := payload.(type) {
	case map[string]any:
		return value
	case map[string]string:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[key] = item
		}
		return out
	default:
		return map[string]any{}
	}
}

func tracePayloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	raw, ok := payload[key]
	if !ok || raw == nil {
		return ""
	}
	text, ok := raw.(string)
	if !ok {
		return ""
	}
	return text
}

func tracePayloadAny(payload map[string]any, key string) any {
	if payload == nil {
		return nil
	}
	value, ok := payload[key]
	if !ok {
		return nil
	}
	return value
}

func formatTraceValue(value any) string {
	if value == nil {
		return "{}"
	}
	redacted := redactTraceValue(value)
	switch v := redacted.(type) {
	case string:
		return v
	}
	blob, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", redacted)
	}
	return string(blob)
}

func redactTraceValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSensitiveTraceKey(key) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = redactTraceValue(item)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSensitiveTraceKey(key) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = redactTraceValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for idx := range typed {
			out[idx] = redactTraceValue(typed[idx])
		}
		return out
	case []string:
		out := make([]any, len(typed))
		for idx := range typed {
			out[idx] = redactTraceValue(typed[idx])
		}
		return out
	default:
		return value
	}
}

func isSensitiveTraceKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	key = strings.ReplaceAll(key, "-", "_")
	return strings.Contains(key, "authorization") ||
		strings.Contains(key, "api_key") ||
		strings.Contains(key, "apikey") ||
		strings.Contains(key, "token") ||
		strings.Contains(key, "secret") ||
		strings.Contains(key, "password")
}

func indentBlock(value string, prefix string) string {
	if strings.TrimSpace(prefix) == "" {
		prefix = "  "
	}
	lines := strings.Split(value, "\n")
	for idx := range lines {
		lines[idx] = prefix + lines[idx]
	}
	return strings.Join(lines, "\n")
}

func valueOrFallback(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
