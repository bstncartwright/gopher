package agentcore

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

type minimaxUnderstandImageTool struct {
	client *minimaxClient
}

func newMinimaxUnderstandImageTool() *minimaxUnderstandImageTool {
	return &minimaxUnderstandImageTool{client: newMinimaxClient()}
}

func (t *minimaxUnderstandImageTool) Name() string {
	return "minimax_understand_image"
}

func (t *minimaxUnderstandImageTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Analyze and understand images using MiniMax vision API. Works with image URLs or base64-encoded image data.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "Question or analysis request for the image. Example: 'What does this image show?', 'Describe the contents of this image', 'What text is in this image?'",
				},
				"image_url": map[string]any{
					"type":        "string",
					"description": "Image URL (HTTP/HTTPS). If provided, image_data should not be provided.",
				},
				"image_data": map[string]any{
					"type":        "string",
					"description": "Base64-encoded image data. If provided, image_url should not be provided. Supported formats: JPEG, PNG, GIF, WebP (max 20MB).",
				},
			},
		},
	}
}

func (t *minimaxUnderstandImageTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	prompt, err := requiredStringArg(input.Args, "prompt")
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": "prompt is required"}}, err
	}

	imageURL, hasURL := optionalStringArg(input.Args, "image_url")
	imageData, hasData := optionalStringArg(input.Args, "image_data")

	if !hasURL && !hasData {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": "either image_url or image_data is required"}}, fmt.Errorf("either image_url or image_data is required")
	}

	reqBody := map[string]any{
		"model":  "MiniMax-VL-01",
		"prompt": prompt,
	}

	if hasURL && imageURL != "" {
		reqBody["image_urls"] = []string{imageURL}
	} else if hasData && imageData != "" {
		reqBody["images"] = []map[string]any{{
			"type":      "base64",
			"data":      imageData,
			"mime_type": "image/jpeg",
		}}
	}

	result, err := t.client.doRequest(ctx, "POST", "/v1/vision/understanding", reqBody)
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	output := map[string]any{
		"model":  "MiniMax-VL-01",
		"prompt": prompt,
	}

	if texts, ok := result["texts"].([]any); ok {
		var responses []string
		for _, t := range texts {
			if s, ok := t.(string); ok {
				responses = append(responses, s)
			}
		}
		if len(responses) > 0 {
			output["analysis"] = strings.Join(responses, "\n")
		}
	}

	return ToolOutput{Status: ToolStatusOK, Result: output}, nil
}

func extractImageDataFromAttachment(attachment Attachment) string {
	if len(attachment.Data) > 0 {
		return base64.StdEncoding.EncodeToString(attachment.Data)
	}
	return ""
}
