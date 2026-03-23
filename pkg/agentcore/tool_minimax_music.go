package agentcore

import (
	"context"
	"fmt"
)

type minimaxMusicTool struct {
	client *minimaxClient
}

func newMinimaxMusicTool() *minimaxMusicTool {
	return &minimaxMusicTool{client: newMinimaxClient()}
}

func (t *minimaxMusicTool) Name() string {
	return "minimax_music"
}

func (t *minimaxMusicTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Generate music from text prompts and optional lyrics using MiniMax music generation API (music-2.0 model).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "Description of the music style, mood, and scenario. For instrumental music with music-2.5+, this is required. Length: 1-2000 characters.",
				},
				"lyrics": map[string]any{
					"type":        "string",
					"description": "Song lyrics with \\n separated lines. Use structure tags: [Intro], [Verse], [Pre Chorus], [Chorus], [Interlude], [Bridge], [Outro], [Post Chorus], [Transition], [Break], [Hook], [Build Up], [Inst], [Solo]. Required for non-instrumental music.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Music model. Options: music-2.5+ (recommended), music-2.5.",
					"default":     "music-2.5+",
				},
				"is_instrumental": map[string]any{
					"type":        "boolean",
					"description": "Generate instrumental music without vocals. Only supported on music-2.5+. Default: false.",
					"default":     false,
				},
				"output_format": map[string]any{
					"type":        "string",
					"description": "Output format. Options: url, hex. Default: hex.",
					"default":     "hex",
				},
				"lyrics_optimizer": map[string]any{
					"type":        "boolean",
					"description": "Auto-generate lyrics from prompt when lyrics is empty. Default: false.",
					"default":     false,
				},
				"audio_format": map[string]any{
					"type":        "string",
					"description": "Audio format. Options: mp3, wav, pcm. Default: mp3.",
					"default":     "mp3",
				},
				"sample_rate": map[string]any{
					"type":        "integer",
					"description": "Audio sample rate. Options: 16000, 24000, 32000, 44100.",
				},
				"bitrate": map[string]any{
					"type":        "integer",
					"description": "Audio bitrate. Options: 32000, 64000, 128000, 256000.",
				},
			},
		},
	}
}

func (t *minimaxMusicTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	model, _ := optionalStringArg(input.Args, "model")
	if model == "" {
		model = "music-2.5+"
	}

	isInstrumental := false
	if raw, ok := input.Args["is_instrumental"]; ok {
		if b, ok := raw.(bool); ok {
			isInstrumental = b
		}
	}

	prompt, _ := optionalStringArg(input.Args, "prompt")
	lyrics, _ := optionalStringArg(input.Args, "lyrics")

	if !isInstrumental && lyrics == "" && prompt == "" {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": "prompt or lyrics is required"}}, fmt.Errorf("prompt or lyrics is required")
	}

	if isInstrumental {
		if prompt == "" {
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": "prompt is required for instrumental music"}}, fmt.Errorf("prompt is required for instrumental music")
		}
	}

	outputFormat, _ := optionalStringArg(input.Args, "output_format")
	if outputFormat == "" {
		outputFormat = "hex"
	}

	lyricsOptimizer := false
	if raw, ok := input.Args["lyrics_optimizer"]; ok {
		if b, ok := raw.(bool); ok {
			lyricsOptimizer = b
		}
	}

	reqBody := map[string]any{
		"model":            model,
		"is_instrumental":  isInstrumental,
		"output_format":    outputFormat,
		"lyrics_optimizer": lyricsOptimizer,
	}

	if prompt != "" {
		reqBody["prompt"] = prompt
	}

	if lyrics != "" {
		reqBody["lyrics"] = lyrics
	}

	audioSetting := map[string]any{}
	if format, _ := optionalStringArg(input.Args, "audio_format"); format != "" {
		audioSetting["format"] = format
	}
	if raw, ok := input.Args["sample_rate"]; ok {
		if v, ok := toInt(raw); ok {
			audioSetting["sample_rate"] = v
		}
	}
	if raw, ok := input.Args["bitrate"]; ok {
		if v, ok := toInt(raw); ok {
			audioSetting["bitrate"] = v
		}
	}
	if len(audioSetting) > 0 {
		reqBody["audio_setting"] = audioSetting
	}

	result, err := t.client.doRequest(ctx, "POST", minimaxMusicEndpoint, reqBody)
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	data, _ := result["data"].(map[string]any)
	extraInfo, _ := result["extra_info"].(map[string]any)

	output := map[string]any{
		"model":  model,
		"status": data["status"],
	}

	if audio, ok := data["audio"].(string); ok {
		if outputFormat == "hex" {
			output["audio_hex"] = audio
		} else {
			output["audio_url"] = audio
		}
	}

	if extraInfo != nil {
		output["music_duration_ms"] = extraInfo["music_duration"]
		output["music_sample_rate"] = extraInfo["music_sample_rate"]
		output["music_channel"] = extraInfo["music_channel"]
		output["bitrate"] = extraInfo["bitrate"]
		output["music_size_bytes"] = extraInfo["music_size"]
	}

	if traceID, ok := result["trace_id"].(string); ok {
		output["trace_id"] = traceID
	}

	return ToolOutput{Status: ToolStatusOK, Result: output}, nil
}
