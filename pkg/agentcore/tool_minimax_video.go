package agentcore

import (
	"context"
	"fmt"
	"strings"
)

type minimaxVideoTool struct {
	client *minimaxClient
}

func newMinimaxVideoTool() *minimaxVideoTool {
	return &minimaxVideoTool{client: newMinimaxClient()}
}

func (t *minimaxVideoTool) Name() string {
	return "minimax_video"
}

func (t *minimaxVideoTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Generate videos from text descriptions using MiniMax video generation API. Creates an async task and returns task_id for status polling.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "Text description of the video. Maximum 2000 characters. Use [command] syntax for camera control: [Truck left], [Truck right], [Pan left], [Pan right], [Push in], [Pull out], [Pedestal up], [Pedestal down], [Tilt up], [Tilt down], [Zoom in], [Zoom out], [Shake], [Tracking shot], [Static shot].",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Video model. Options: MiniMax-Hailuo-2.3, MiniMax-Hailuo-02, T2V-01-Director, T2V-01.",
					"default":     "MiniMax-Hailuo-2.3",
				},
				"duration": map[string]any{
					"type":        "integer",
					"description": "Video length in seconds. Options: 6 or 10. Depends on model and resolution.",
					"default":     6,
				},
				"resolution": map[string]any{
					"type":        "string",
					"description": "Video resolution. Options: 720P, 768P, 1080P. Depends on model and duration.",
					"default":     "768P",
				},
				"prompt_optimizer": map[string]any{
					"type":        "boolean",
					"description": "Enable automatic prompt optimization. Default: true.",
					"default":     true,
				},
				"fast_pretreatment": map[string]any{
					"type":        "boolean",
					"description": "Reduce optimization time when prompt_optimizer is enabled. Only for MiniMax-Hailuo-2.3 and MiniMax-Hailuo-02.",
					"default":     false,
				},
				"callback_url": map[string]any{
					"type":        "string",
					"description": "Webhook URL to receive async task status updates. MiniMax will POST status changes (processing, success, failed) to this URL.",
				},
			},
			"required": []any{"prompt"},
		},
	}
}

func (t *minimaxVideoTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
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

	model, _ := optionalStringArg(input.Args, "model")
	if model == "" {
		model = "MiniMax-Hailuo-2.3"
	}

	duration := 6
	if raw, ok := input.Args["duration"]; ok {
		if v, ok := toInt(raw); ok && (v == 6 || v == 10) {
			duration = v
		}
	}

	resolution, _ := optionalStringArg(input.Args, "resolution")
	if resolution == "" {
		resolution = "768P"
	}

	promptOptimizer := true
	if raw, ok := input.Args["prompt_optimizer"]; ok {
		if b, ok := raw.(bool); ok {
			promptOptimizer = b
		}
	}

	fastPretreatment := false
	if raw, ok := input.Args["fast_pretreatment"]; ok {
		if b, ok := raw.(bool); ok {
			fastPretreatment = b
		}
	}

	reqBody := map[string]any{
		"model":            model,
		"prompt":           prompt,
		"duration":         duration,
		"resolution":       resolution,
		"prompt_optimizer": promptOptimizer,
	}

	if fastPretreatment && (model == "MiniMax-Hailuo-2.3" || model == "MiniMax-Hailuo-02") {
		reqBody["fast_pretreatment"] = true
	}

	if callbackURL, _ := optionalStringArg(input.Args, "callback_url"); callbackURL != "" {
		reqBody["callback_url"] = callbackURL
	}

	result, err := t.client.doRequest(ctx, "POST", minimaxVideoEndpoint, reqBody)
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	output := map[string]any{
		"model":      model,
		"prompt":     prompt,
		"duration":   duration,
		"resolution": resolution,
	}

	if taskID, ok := result["task_id"].(string); ok {
		output["task_id"] = taskID
		output["status"] = "processing"
		output["message"] = "Video generation task created. Poll for completion using minimax_video_status tool with task_id."
	}

	return ToolOutput{Status: ToolStatusOK, Result: output}, nil
}

type minimaxVideoStatusTool struct {
	client *minimaxClient
}

func newMinimaxVideoStatusTool() *minimaxVideoStatusTool {
	return &minimaxVideoStatusTool{client: newMinimaxClient()}
}

func (t *minimaxVideoStatusTool) Name() string {
	return "minimax_video_status"
}

func (t *minimaxVideoStatusTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Query the status of a MiniMax video generation task and get the result when complete.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "The task_id returned from a minimax_video call.",
				},
			},
			"required": []any{"task_id"},
		},
	}
}

func (t *minimaxVideoStatusTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	taskID, err := requiredStringArg(input.Args, "task_id")
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": "task_id is required"}}, err
	}

	result, err := t.client.doRequestWithQuery(ctx, "GET", "/v1/query/video_generation", nil, map[string]string{"task_id": taskID})
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	status, _ := result["status"].(string)
	fileID, _ := result["file_id"].(string)

	output := map[string]any{
		"task_id": taskID,
		"status":  status,
	}

	if width, ok := result["video_width"].(float64); ok {
		output["video_width"] = int(width)
	}
	if height, ok := result["video_height"].(float64); ok {
		output["video_height"] = int(height)
	}

	if fileID != "" && status == "Success" {
		output["file_id"] = fileID

		fileResult, err := t.client.doRequestWithQuery(ctx, "GET", minimaxFileRetrieveEndpoint, nil, map[string]string{"file_id": fileID})
		if err != nil {
			output["message"] = "Video generation complete but failed to fetch download URL: " + err.Error()
			return ToolOutput{Status: ToolStatusOK, Result: output}, nil
		}

		if fileObj, ok := fileResult["file"].(map[string]any); ok {
			if downloadURL, ok := fileObj["download_url"].(string); ok && downloadURL != "" {
				output["download_url"] = downloadURL
			}
			if filename, ok := fileObj["filename"].(string); ok {
				output["filename"] = filename
			}
			if bytes, ok := fileObj["bytes"].(float64); ok {
				output["bytes"] = int(bytes)
			}
			if createdAt, ok := fileObj["created_at"].(float64); ok {
				output["created_at"] = int(createdAt)
			}
		}

		output["message"] = "Video generation complete. Use download_url to download the video (valid for 1 hour)."
	}

	return ToolOutput{Status: ToolStatusOK, Result: output}, nil
}
