package agentcore

import (
	"context"
	"fmt"
	"strings"
)

type minimaxImageTool struct {
	client *minimaxClient
}

func newMinimaxImageTool() *minimaxImageTool {
	return &minimaxImageTool{client: newMinimaxClient()}
}

func (t *minimaxImageTool) Name() string {
	return "minimax_image"
}

func (t *minimaxImageTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Generate images from text descriptions using MiniMax image generation API (image-01 model).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "Text description of the image to generate. Maximum 1500 characters. Be specific about subjects, setting, style, lighting, etc.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Image model to use. Currently only image-01 is available.",
					"default":     "image-01",
				},
				"aspect_ratio": map[string]any{
					"type":        "string",
					"description": "Image aspect ratio. Options: 1:1 (1024x1024), 16:9 (1280x720), 4:3 (1152x864), 3:2 (1248x832), 2:3 (832x1248), 3:4 (864x1152), 9:16 (720x1280), 21:9 (1344x576).",
					"default":     "1:1",
				},
				"width": map[string]any{
					"type":        "integer",
					"description": "Image width in pixels. Range [512, 2048], must be divisible by 8. If set, height must also be set. aspect_ratio takes priority if both are provided.",
				},
				"height": map[string]any{
					"type":        "integer",
					"description": "Image height in pixels. Range [512, 2048], must be divisible by 8. If set, width must also be set.",
				},
				"response_format": map[string]any{
					"type":        "string",
					"description": "Response format. Options: url, base64. Default: url. URL expires in 24 hours.",
					"default":     "url",
				},
				"n": map[string]any{
					"type":        "integer",
					"description": "Number of images to generate. Range [1, 9]. Default: 1.",
					"default":     1,
				},
				"seed": map[string]any{
					"type":        "integer",
					"description": "Random seed for reproducible images. Using the same seed and parameters produces the same image.",
				},
				"prompt_optimizer": map[string]any{
					"type":        "boolean",
					"description": "Enable automatic prompt optimization. Default: false.",
					"default":     false,
				},
			},
			"required": []any{"prompt"},
		},
	}
}

func (t *minimaxImageTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	prompt, err := requiredStringArg(input.Args, "prompt")
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": "prompt is required"}}, err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": "prompt cannot be empty"}}, fmt.Errorf("prompt cannot be empty")
	}

	model := "image-01"

	aspectRatio, _ := optionalStringArg(input.Args, "aspect_ratio")
	if aspectRatio == "" {
		aspectRatio = "1:1"
	}

	responseFormat, _ := optionalStringArg(input.Args, "response_format")
	if responseFormat == "" {
		responseFormat = "url"
	}

	n := 1
	if raw, ok := input.Args["n"]; ok {
		if v, ok := toInt(raw); ok && v > 0 && v <= 9 {
			n = v
		}
	}

	seed, hasSeed := toInt(input.Args["seed"])

	promptOptimizer := false
	if raw, ok := input.Args["prompt_optimizer"]; ok {
		if b, ok := raw.(bool); ok {
			promptOptimizer = b
		}
	}

	reqBody := map[string]any{
		"model":            model,
		"prompt":           prompt,
		"aspect_ratio":     aspectRatio,
		"response_format":  responseFormat,
		"n":                n,
		"prompt_optimizer": promptOptimizer,
	}

	if width, ok := toInt(input.Args["width"]); ok && width > 0 {
		if height, ok := toInt(input.Args["height"]); ok && height > 0 {
			reqBody["width"] = width
			reqBody["height"] = height
		}
	}

	if hasSeed && seed >= 0 {
		reqBody["seed"] = seed
	}

	result, err := t.client.doRequest(ctx, "POST", minimaxImageEndpoint, reqBody)
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	data, _ := result["data"].(map[string]any)
	metadata, _ := result["metadata"].(map[string]any)

	output := map[string]any{
		"model":  model,
		"prompt": prompt,
	}

	if responseFormat == "url" {
		if imageURLs, ok := data["image_urls"].([]any); ok {
			urls := make([]string, 0, len(imageURLs))
			for _, u := range imageURLs {
				if s, ok := u.(string); ok {
					urls = append(urls, s)
				}
			}
			output["image_urls"] = urls
		}
	} else {
		if imageBase64, ok := data["image_base64"].([]any); ok {
			images := make([]string, 0, len(imageBase64))
			for _, b := range imageBase64 {
				if s, ok := b.(string); ok {
					images = append(images, s)
				}
			}
			output["image_base64"] = images
		}
	}

	if metadata != nil {
		if successCount, ok := metadata["success_count"].(float64); ok {
			output["success_count"] = int(successCount)
		}
		if failedCount, ok := metadata["failed_count"].(float64); ok {
			output["failed_count"] = int(failedCount)
		}
	}

	if traceID, ok := result["id"].(string); ok {
		output["trace_id"] = traceID
	}

	return ToolOutput{Status: ToolStatusOK, Result: output}, nil
}
