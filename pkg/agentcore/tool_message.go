package agentcore

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
)

type messageTool struct{}

func (t *messageTool) Name() string {
	return "message"
}

func (t *messageTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Send a user-visible message to the current conversation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string"},
				"attachments": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path":      map[string]any{"type": "string"},
							"name":      map[string]any{"type": "string"},
							"mime_type": map[string]any{"type": "string"},
						},
						"required": []any{"path"},
					},
				},
				"stream":   map[string]any{"type": "boolean"},
				"draft_id": map[string]any{"type": "integer"},
			},
		},
	}
}

func (t *messageTool) Available(input ToolInput) bool {
	return input.Agent != nil && input.Agent.MessageService != nil
}

func (t *messageTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if input.Agent == nil {
		err := fmt.Errorf("agent is required")
		slog.Error("message_tool: agent is required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if input.Agent.MessageService == nil {
		err := fmt.Errorf("message service is unavailable")
		slog.Warn("message_tool: message service is unavailable")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if input.Session == nil || strings.TrimSpace(input.Session.ID) == "" {
		err := fmt.Errorf("session is required")
		slog.Error("message_tool: session is required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	text, _ := optionalStringArg(input.Args, "text")
	text = strings.TrimSpace(text)
	attachments, err := messageAttachmentsFromArgs(input.Args)
	if err != nil {
		slog.Error("message_tool: invalid attachments", "error", err)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if text == "" && len(attachments) == 0 {
		err := fmt.Errorf("text or attachments is required")
		slog.Error("message_tool: text or attachments required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	stream, _ := optionalBoolArg(input.Args, "stream")

	if stream {
		if len(attachments) > 0 {
			err := fmt.Errorf("attachments are unsupported when stream is true")
			slog.Error("message_tool: attachments unsupported in stream mode")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		if text == "" {
			err := fmt.Errorf("text is required when stream is true")
			slog.Error("message_tool: text required in stream mode")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		streamingService, ok := input.Agent.MessageService.(MessageToolStreamingService)
		if !ok {
			err := fmt.Errorf("streaming is unsupported by active message service")
			slog.Error("message_tool: streaming unsupported by active service")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		draftID, hasDraftID, err := optionalInt64Arg(input.Args, "draft_id")
		if err != nil {
			slog.Error("message_tool: invalid draft id", "error", err)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		req := MessageDraftRequest{
			SessionID: strings.TrimSpace(input.Session.ID),
			Text:      text,
		}
		if hasDraftID {
			if draftID <= 0 {
				err := fmt.Errorf("draft_id must be greater than 0")
				slog.Error("message_tool: invalid draft id", "draft_id", draftID)
				return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
			}
			req.DraftID = draftID
		}
		result, sendErr := streamingService.SendMessageDraft(ctx, req)
		if sendErr != nil {
			slog.Error("message_tool: failed to stream message draft", "error", sendErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": sendErr.Error()}}, sendErr
		}
		output := map[string]any{
			"drafted":         result.Drafted,
			"conversation_id": strings.TrimSpace(result.ConversationID),
			"text":            strings.TrimSpace(result.Text),
			"draft_id":        result.DraftID,
		}
		return ToolOutput{Status: ToolStatusOK, Result: output}, nil
	}

	result, sendErr := input.Agent.MessageService.SendMessage(ctx, MessageSendRequest{
		SessionID:   strings.TrimSpace(input.Session.ID),
		Text:        text,
		Attachments: attachments,
	})
	if sendErr != nil {
		slog.Error("message_tool: failed to send message", "error", sendErr)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": sendErr.Error()}}, sendErr
	}

	output := map[string]any{
		"sent":             result.Sent,
		"conversation_id":  strings.TrimSpace(result.ConversationID),
		"text":             strings.TrimSpace(result.Text),
		"attachment_count": result.AttachmentCount,
	}
	return ToolOutput{Status: ToolStatusOK, Result: output}, nil
}

func optionalInt64Arg(args map[string]any, key string) (int64, bool, error) {
	if args == nil {
		return 0, false, nil
	}
	value, exists := args[key]
	if !exists || value == nil {
		return 0, false, nil
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), true, nil
	case int32:
		return int64(typed), true, nil
	case int64:
		return typed, true, nil
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return 0, false, fmt.Errorf("%s must be a finite integer", key)
		}
		value := int64(typed)
		if float32(value) != typed {
			return 0, false, fmt.Errorf("%s must be an integer", key)
		}
		return value, true, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false, fmt.Errorf("%s must be a finite integer", key)
		}
		value := int64(typed)
		if float64(value) != typed {
			return 0, false, fmt.Errorf("%s must be an integer", key)
		}
		return value, true, nil
	default:
		return 0, false, fmt.Errorf("%s must be an integer", key)
	}
}

func messageAttachmentsFromArgs(args map[string]any) ([]MessageAttachment, error) {
	if args == nil {
		return nil, nil
	}
	rawAttachments, ok := args["attachments"]
	if !ok || rawAttachments == nil {
		return nil, nil
	}
	items, ok := rawAttachments.([]any)
	if !ok {
		return nil, fmt.Errorf("attachments must be an array")
	}
	out := make([]MessageAttachment, 0, len(items))
	for idx, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("attachments[%d] must be an object", idx)
		}
		pathValue, err := requiredStringArg(entry, "path")
		if err != nil {
			return nil, fmt.Errorf("attachments[%d].path: %w", idx, err)
		}
		name, _ := optionalStringArg(entry, "name")
		mimeType, _ := optionalStringArg(entry, "mime_type")
		out = append(out, MessageAttachment{
			Path:     strings.TrimSpace(pathValue),
			Name:     strings.TrimSpace(name),
			MIMEType: strings.TrimSpace(mimeType),
		})
	}
	return out, nil
}
