package agentcore

import (
	"context"
	"fmt"
	"log/slog"
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
