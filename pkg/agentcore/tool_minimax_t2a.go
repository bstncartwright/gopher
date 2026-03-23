package agentcore

import (
	"context"
	"fmt"
	"strings"
)

type minimaxT2ATool struct {
	client *minimaxClient
}

func newMinimaxT2ATool() *minimaxT2ATool {
	return &minimaxT2ATool{client: newMinimaxClient()}
}

func (t *minimaxT2ATool) Name() string {
	return "minimax_t2a"
}

func (t *minimaxT2ATool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Generate speech audio from text using MiniMax text-to-speech API. Supports multiple voices, languages, audio effects, and subtitle generation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "The text to convert to speech. Maximum 10,000 characters. Use \\n for paragraph breaks. Use <#x#> for pauses (x in seconds, range [0.01, 99.99]). Supported interjections: (laughs), (chuckle), (coughs), (clear-throat), (groans), (breath), (pant), (inhale), (exhale), (gasps), (sniffs), (sighs), (snorts), (burps), (lip-smacking), (humming), (hissing), (emm), (sneezes) - only for speech-2.8-hd/turbo.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Speech model to use. Options: speech-2.8-hd, speech-2.8-turbo, speech-2.6-hd, speech-2.6-turbo, speech-02-hd, speech-02-turbo, speech-01-hd, speech-01-turbo.",
					"default":     "speech-2.8-hd",
				},
				"stream": map[string]any{
					"type":        "boolean",
					"description": "Enable streaming output. Default: false.",
					"default":     false,
				},
				"voice_id": map[string]any{
					"type":        "string",
					"description": "Voice ID to use. Example: English_Graceful_Lady, English_Insightful_Speaker. Leave empty for timbre mixing with timbre_weights.",
					"default":     "English_Graceful_Lady",
				},
				"speed": map[string]any{
					"type":        "number",
					"description": "Speech speed. Range: [0.5, 2]. Default: 1.0.",
					"default":     1.0,
				},
				"pitch": map[string]any{
					"type":        "integer",
					"description": "Pitch adjustment. Range: [-12, 12]. Default: 0 (original pitch).",
					"default":     0,
				},
				"volume": map[string]any{
					"type":        "number",
					"description": "Speech volume. Range: (0, 10]. Default: 1.0.",
					"default":     1.0,
				},
				"emotion": map[string]any{
					"type":        "string",
					"description": "Emotion control. Options: happy, sad, angry, fearful, disgusted, surprised, calm, fluent, whisper. whisper only for speech-2.6-turbo/hd.",
				},
				"text_normalization": map[string]any{
					"type":        "boolean",
					"description": "Enable text normalization for digit reading. Default: false.",
					"default":     false,
				},
				"latex_read": map[string]any{
					"type":        "boolean",
					"description": "Enable LaTeX formula reading (Chinese only, sets language_boost to Chinese). Wrap formulas with $$. Default: false.",
					"default":     false,
				},
				"output_format": map[string]any{
					"type":        "string",
					"description": "Output format. Options: url, hex. Default: hex. URL expires in 24 hours.",
					"default":     "hex",
				},
				"audio_format": map[string]any{
					"type":        "string",
					"description": "Audio format. Options: mp3, pcm, flac, wav. Default: mp3. wav only for non-streaming.",
					"default":     "mp3",
				},
				"sample_rate": map[string]any{
					"type":        "integer",
					"description": "Audio sample rate. Options: 8000, 16000, 22050, 24000, 32000, 44100.",
				},
				"bitrate": map[string]any{
					"type":        "integer",
					"description": "Audio bitrate. Options: 32000, 64000, 128000, 256000.",
				},
				"channel": map[string]any{
					"type":        "integer",
					"description": "Audio channels. Options: 1 (mono), 2 (stereo). Default: 1.",
					"default":     1,
				},
				"force_cbr": map[string]any{
					"type":        "boolean",
					"description": "Enable constant bitrate encoding for streaming MP3. Default: false.",
					"default":     false,
				},
				"language_boost": map[string]any{
					"type":        "string",
					"description": "Language boost for minority languages. Options: Chinese, Chinese Yue, English, Arabic, Russian, Spanish, French, Portuguese, German, Turkish, Dutch, Ukrainian, Vietnamese, Indonesian, Japanese, Italian, Korean, Thai, Polish, Romanian, Greek, Czech, Finnish, Hindi, Bulgarian, Danish, Hebrew, Malay, Persian, Slovak, Swedish, Croatian, Filipino, Hungarian, Norwegian, Slovenian, Catalan, Nynorsk, Tamil, Afrikaans, auto.",
				},
				"subtitle_enable": map[string]any{
					"type":        "boolean",
					"description": "Enable subtitle generation. Default: false.",
					"default":     false,
				},
				"pronunciation_dict": map[string]any{
					"type":        "object",
					"description": "Pronunciation dictionary. Example: {\"tone\": [\"omg/oh my god\"]}. For Chinese, use numbers 1-5 for tones.",
					"properties": map[string]any{
						"tone": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Array of pronunciation mappings.",
						},
					},
				},
				"timbre_weights": map[string]any{
					"type":        "array",
					"description": "Timbre weights for voice mixing. Each entry: {voice_id: string, weight: int [1-100]}. Up to 4 voices. Leave voice_id empty when using this.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"voice_id": map[string]any{"type": "string"},
							"weight":   map[string]any{"type": "integer"},
						},
					},
				},
				"voice_modify": map[string]any{
					"type":        "object",
					"description": "Voice effects (non-streaming MP3/FLAC/WAV only).",
					"properties": map[string]any{
						"pitch": map[string]any{
							"type":        "integer",
							"description": "Deepen/Brighten voice. Range: [-100, 100].",
						},
						"intensity": map[string]any{
							"type":        "integer",
							"description": "Stronger/Softer voice. Range: [-100, 100].",
						},
						"timbre": map[string]any{
							"type":        "integer",
							"description": "Nasal/Crisp voice. Range: [-100, 100].",
						},
						"sound_effects": map[string]any{
							"type":        "string",
							"description": "Sound effect. Options: spacious_echo, auditorium_echo, lofi_telephone, robotic.",
						},
					},
				},
			},
			"required": []any{"text"},
		},
	}
}

func (t *minimaxT2ATool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	text, err := requiredStringArg(input.Args, "text")
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": "text is required"}}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": "text cannot be empty"}}, fmt.Errorf("text cannot be empty")
	}

	model, _ := optionalStringArg(input.Args, "model")
	if model == "" {
		model = "speech-2.8-hd"
	}

	stream := false
	if raw, ok := input.Args["stream"]; ok {
		if b, ok := raw.(bool); ok {
			stream = b
		}
	}

	voiceID, _ := optionalStringArg(input.Args, "voice_id")
	speed := 1.0
	if raw, ok := input.Args["speed"]; ok {
		if v, ok := toFloat(raw); ok {
			speed = v
		}
	}

	pitch := 0
	if raw, ok := input.Args["pitch"]; ok {
		if v, ok := toInt(raw); ok {
			pitch = v
		}
	}

	vol := 1.0
	if raw, ok := input.Args["volume"]; ok {
		if v, ok := toFloat(raw); ok {
			vol = v
		}
	}

	outputFormat, _ := optionalStringArg(input.Args, "output_format")
	if outputFormat == "" {
		outputFormat = "hex"
	}

	reqBody := map[string]any{
		"model":         model,
		"text":          text,
		"stream":        stream,
		"output_format": outputFormat,
		"voice_setting": map[string]any{
			"voice_id": voiceID,
			"speed":    speed,
			"pitch":    pitch,
			"vol":      vol,
		},
		"audio_setting": map[string]any{
			"format": "mp3",
		},
	}

	if emotion, _ := optionalStringArg(input.Args, "emotion"); emotion != "" {
		if vs, ok := reqBody["voice_setting"].(map[string]any); ok {
			vs["emotion"] = emotion
		}
	}

	if textNorm, ok := input.Args["text_normalization"]; ok {
		if b, ok := textNorm.(bool); ok {
			if vs, ok := reqBody["voice_setting"].(map[string]any); ok {
				vs["text_normalization"] = b
			}
		}
	}

	if latexRead, ok := input.Args["latex_read"]; ok {
		if b, ok := latexRead.(bool); ok && b {
			if vs, ok := reqBody["voice_setting"].(map[string]any); ok {
				vs["latex_read"] = true
			}
			reqBody["language_boost"] = "Chinese"
		}
	}

	if af, _ := optionalStringArg(input.Args, "audio_format"); af != "" {
		if as, ok := reqBody["audio_setting"].(map[string]any); ok {
			as["format"] = af
		}
	}

	if raw, ok := input.Args["sample_rate"]; ok {
		if v, ok := toInt(raw); ok {
			if as, ok := reqBody["audio_setting"].(map[string]any); ok {
				as["sample_rate"] = v
			}
		}
	}

	if raw, ok := input.Args["bitrate"]; ok {
		if v, ok := toInt(raw); ok {
			if as, ok := reqBody["audio_setting"].(map[string]any); ok {
				as["bitrate"] = v
			}
		}
	}

	if raw, ok := input.Args["channel"]; ok {
		if v, ok := toInt(raw); ok {
			if as, ok := reqBody["audio_setting"].(map[string]any); ok {
				as["channel"] = v
			}
		}
	}

	if raw, ok := input.Args["force_cbr"]; ok {
		if b, ok := raw.(bool); ok {
			if as, ok := reqBody["audio_setting"].(map[string]any); ok {
				as["force_cbr"] = b
			}
		}
	}

	if langBoost, _ := optionalStringArg(input.Args, "language_boost"); langBoost != "" {
		reqBody["language_boost"] = langBoost
	}

	if raw, ok := input.Args["subtitle_enable"]; ok {
		if b, ok := raw.(bool); ok {
			reqBody["subtitle_enable"] = b
		}
	}

	if raw, ok := input.Args["pronunciation_dict"]; ok {
		if pd, ok := raw.(map[string]any); ok {
			reqBody["pronunciation_dict"] = pd
		}
	}

	if raw, ok := input.Args["timbre_weights"]; ok {
		if tw, ok := raw.([]any); ok {
			reqBody["timbre_weights"] = tw
		}
	}

	if raw, ok := input.Args["voice_modify"]; ok {
		if vm, ok := raw.(map[string]any); ok {
			reqBody["voice_modify"] = vm
		}
	}

	result, err := t.client.doRequest(ctx, "POST", minimaxT2AEndpoint, reqBody)
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	data, _ := result["data"].(map[string]any)
	extraInfo, _ := result["extra_info"].(map[string]any)

	output := map[string]any{
		"model": model,
		"text":  text,
	}

	if stream && data != nil {
		output["data"] = data
	} else if data != nil {
		if audio, ok := data["audio"].(string); ok {
			if outputFormat == "hex" {
				output["audio_hex"] = audio
			} else {
				output["audio_url"] = audio
			}
		}
		if subFile, ok := data["subtitle_file"].(string); ok && subFile != "" {
			output["subtitle_file"] = subFile
		}
	}

	if extraInfo != nil {
		output["audio_length_ms"] = extraInfo["audio_length"]
		output["audio_sample_rate"] = extraInfo["audio_sample_rate"]
		output["audio_size_bytes"] = extraInfo["audio_size"]
		output["bitrate"] = extraInfo["bitrate"]
		output["audio_format"] = extraInfo["audio_format"]
		output["usage_characters"] = extraInfo["usage_characters"]
	}

	if traceID, ok := result["trace_id"].(string); ok {
		output["trace_id"] = traceID
	}

	return ToolOutput{Status: ToolStatusOK, Result: output}, nil
}

func toFloat(v any) (float64, bool) {
	switch typed := v.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}
