package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	matrixtransport "github.com/bstncartwright/gopher/pkg/transport/matrix"
)

const (
	defaultAgentAvatarOpenAIModel = "gpt-image-1.5"
	maxAvatarBytes                = 8 * 1024 * 1024
)

var avatarHTTPClient = &http.Client{Timeout: 30 * time.Second}

const baseAgentAvatarPrompt = `A cute upright gopher mascot character, head-and-shoulders close-up portrait, face centered and filling the frame, large in frame with minimal empty space, small chubby body implied, oversized head, soft light-brown fur with a lighter beige belly, round ears, big glossy dark eyes, small oval nose, prominent buck teeth, subtle friendly smile, clean stylized 3D cartoon render, smooth soft materials, simple soft gradient background, square composition, icon style, high detail, cohesive mascot design, same character design across images, consistent proportions and face, no text, no watermark, face clearly visible within a circular crop

personality variant: %s`

type identityProfile struct {
	Name              string
	Role              string
	StyleVibe         string
	AvatarPersonality string
	Avatar            string
}

func newMatrixManagedAvatarProvider(runtime *gatewayAgentRuntime, identities agentMatrixIdentitySet, logger *log.Logger) matrixtransport.ManagedAvatarProvider {
	if runtime == nil {
		return nil
	}
	agentByUserID := make(map[string]*agentcore.Agent, len(runtime.Agents))
	for actorID, userID := range identities.UserByActorID {
		agent := runtime.Agents[actorID]
		if agent == nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(userID))
		if key == "" {
			continue
		}
		agentByUserID[key] = agent
	}
	if len(agentByUserID) == 0 {
		return nil
	}

	return func(ctx context.Context, userID string) (matrixtransport.ManagedAvatar, error) {
		key := strings.ToLower(strings.TrimSpace(userID))
		agent := agentByUserID[key]
		if agent == nil {
			return matrixtransport.ManagedAvatar{}, nil
		}
		avatar, err := ensureAgentAvatar(ctx, agent)
		if err != nil {
			if logger != nil {
				logger.Printf("agent avatar ensure failed agent_id=%s user_id=%s err=%v", strings.TrimSpace(agent.ID), userID, err)
			}
			return matrixtransport.ManagedAvatar{}, nil
		}
		return avatar, nil
	}
}

func ensureAgentAvatar(ctx context.Context, agent *agentcore.Agent) (matrixtransport.ManagedAvatar, error) {
	if agent == nil {
		return matrixtransport.ManagedAvatar{}, nil
	}
	identityPath, identityContent, profile, err := loadIdentityProfile(agent.Workspace)
	if err != nil {
		return matrixtransport.ManagedAvatar{}, err
	}

	avatarSpec := strings.TrimSpace(profile.Avatar)
	if avatarSpec != "" {
		avatar, err := resolveManagedAvatar(ctx, agent.Workspace, avatarSpec)
		if err == nil {
			return avatar, nil
		}
	}

	imageData, contentType, err := generateAvatarImage(ctx, buildAgentAvatarPrompt(agent, profile))
	if err != nil {
		return matrixtransport.ManagedAvatar{}, err
	}
	if len(imageData) == 0 {
		return matrixtransport.ManagedAvatar{}, nil
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = http.DetectContentType(imageData)
	}
	avatarFilename := "avatar.png"
	avatarPath := filepath.Join(agent.Workspace, avatarFilename)
	if err := os.WriteFile(avatarPath, imageData, 0o644); err != nil {
		return matrixtransport.ManagedAvatar{}, fmt.Errorf("write avatar file %s: %w", avatarPath, err)
	}
	if identityPath != "" {
		updated := upsertIdentityAvatar(identityContent, "./"+avatarFilename)
		if err := os.WriteFile(identityPath, []byte(updated), 0o644); err != nil {
			return matrixtransport.ManagedAvatar{}, fmt.Errorf("update identity avatar in %s: %w", identityPath, err)
		}
	}

	return matrixtransport.ManagedAvatar{
		ContentType: strings.TrimSpace(contentType),
		Data:        imageData,
	}, nil
}

func buildAgentAvatarPrompt(agent *agentcore.Agent, profile identityProfile) string {
	variant := strings.TrimSpace(profile.AvatarPersonality)
	if variant == "" {
		variant = strings.TrimSpace(profile.StyleVibe)
	}
	if variant == "" {
		variant = strings.TrimSpace(profile.Role)
	}
	if variant == "" && agent != nil {
		variant = strings.TrimSpace(agent.Role)
	}
	if variant == "" {
		variant = strings.TrimSpace(profile.Name)
	}
	if variant == "" && agent != nil {
		variant = strings.TrimSpace(agent.Name)
	}
	if variant == "" && agent != nil {
		variant = strings.TrimSpace(agent.ID)
	}
	if variant == "" {
		variant = "friendly, pragmatic, helpful assistant"
	}
	return fmt.Sprintf(baseAgentAvatarPrompt, variant)
}

func loadIdentityProfile(workspace string) (string, string, identityProfile, error) {
	identityPath, err := resolveIdentityFile(workspace)
	if err != nil {
		return "", "", identityProfile{}, err
	}
	if identityPath == "" {
		return "", "", identityProfile{}, nil
	}
	blob, err := os.ReadFile(identityPath)
	if err != nil {
		return "", "", identityProfile{}, fmt.Errorf("read identity file %s: %w", identityPath, err)
	}
	content := string(blob)
	return identityPath, content, parseIdentityProfile(content), nil
}

func resolveIdentityFile(workspace string) (string, error) {
	candidates := []string{
		filepath.Join(workspace, "IDENTITY.md"),
		filepath.Join(workspace, "identity.md"),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("stat identity file %s: %w", candidate, err)
		}
		if info.IsDir() {
			continue
		}
		return candidate, nil
	}
	return "", nil
}

func parseIdentityProfile(content string) identityProfile {
	out := identityProfile{}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		key, value, ok := parseIdentityLine(line)
		if !ok {
			continue
		}
		switch key {
		case "name":
			out.Name = value
		case "role":
			out.Role = value
		case "style/vibe", "style":
			out.StyleVibe = value
		case "avatar personality", "avatar variant", "personality variant":
			out.AvatarPersonality = value
		case "avatar":
			out.Avatar = value
		}
	}
	return out
}

func parseIdentityLine(line string) (string, string, bool) {
	raw := strings.TrimSpace(line)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return "", "", false
	}
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "-"))
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key := strings.ToLower(strings.TrimSpace(parts[0]))
	value := strings.TrimSpace(parts[1])
	if key == "" {
		return "", "", false
	}
	if key == "avatar" || strings.HasPrefix(key, "avatar (") {
		key = "avatar"
	}
	return key, value, true
}

func upsertIdentityAvatar(content string, avatarValue string) string {
	lines := strings.Split(content, "\n")
	for i := range lines {
		key, _, ok := parseIdentityLine(lines[i])
		if !ok || key != "avatar" {
			continue
		}
		prefix := lines[i]
		colon := strings.Index(prefix, ":")
		if colon < 0 {
			continue
		}
		lines[i] = prefix[:colon+1] + " " + avatarValue
		return strings.Join(lines, "\n")
	}
	if strings.TrimSpace(content) == "" {
		return "- Avatar: " + avatarValue + "\n"
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + "- Avatar: " + avatarValue + "\n"
}

func resolveManagedAvatar(ctx context.Context, workspace string, value string) (matrixtransport.ManagedAvatar, error) {
	avatarSpec := strings.Trim(strings.TrimSpace(value), `"'`)
	if avatarSpec == "" {
		return matrixtransport.ManagedAvatar{}, nil
	}
	if strings.HasPrefix(strings.ToLower(avatarSpec), "mxc://") {
		return matrixtransport.ManagedAvatar{MXCURL: avatarSpec}, nil
	}
	if strings.HasPrefix(strings.ToLower(avatarSpec), "data:") {
		contentType, data, err := decodeDataURI(avatarSpec)
		if err != nil {
			return matrixtransport.ManagedAvatar{}, err
		}
		return matrixtransport.ManagedAvatar{ContentType: contentType, Data: data}, nil
	}
	if looksLikeHTTPURL(avatarSpec) {
		contentType, data, err := fetchAvatarURL(ctx, avatarSpec)
		if err != nil {
			return matrixtransport.ManagedAvatar{}, err
		}
		return matrixtransport.ManagedAvatar{ContentType: contentType, Data: data}, nil
	}

	pathValue := avatarSpec
	if strings.HasPrefix(strings.ToLower(pathValue), "file://") {
		parsed, err := url.Parse(pathValue)
		if err != nil {
			return matrixtransport.ManagedAvatar{}, fmt.Errorf("parse file avatar url %q: %w", pathValue, err)
		}
		pathValue = parsed.Path
	}
	if !filepath.IsAbs(pathValue) {
		pathValue = filepath.Join(workspace, pathValue)
	}
	data, err := os.ReadFile(filepath.Clean(pathValue))
	if err != nil {
		return matrixtransport.ManagedAvatar{}, fmt.Errorf("read avatar file %s: %w", pathValue, err)
	}
	if len(data) == 0 {
		return matrixtransport.ManagedAvatar{}, fmt.Errorf("avatar file %s is empty", pathValue)
	}
	if len(data) > maxAvatarBytes {
		return matrixtransport.ManagedAvatar{}, fmt.Errorf("avatar file %s is too large (%d bytes)", pathValue, len(data))
	}
	return matrixtransport.ManagedAvatar{
		ContentType: http.DetectContentType(data),
		Data:        data,
	}, nil
}

func decodeDataURI(value string) (string, []byte, error) {
	raw := strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return "", nil, fmt.Errorf("invalid data uri")
	}
	body := raw[len("data:"):]
	parts := strings.SplitN(body, ",", 2)
	if len(parts) != 2 {
		return "", nil, fmt.Errorf("invalid data uri payload")
	}
	media := parts[0]
	dataPart := parts[1]
	contentType := ""
	isBase64 := false
	if media != "" {
		metaParts := strings.Split(media, ";")
		if len(metaParts) > 0 {
			contentType = strings.TrimSpace(metaParts[0])
		}
		for _, meta := range metaParts[1:] {
			if strings.EqualFold(strings.TrimSpace(meta), "base64") {
				isBase64 = true
			}
		}
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	var data []byte
	var err error
	if isBase64 {
		data, err = base64.StdEncoding.DecodeString(dataPart)
		if err != nil {
			return "", nil, fmt.Errorf("decode base64 avatar data uri: %w", err)
		}
	} else {
		decoded, err := url.QueryUnescape(dataPart)
		if err != nil {
			return "", nil, fmt.Errorf("decode escaped avatar data uri: %w", err)
		}
		data = []byte(decoded)
	}
	if len(data) == 0 {
		return "", nil, fmt.Errorf("avatar data uri is empty")
	}
	if len(data) > maxAvatarBytes {
		return "", nil, fmt.Errorf("avatar data uri exceeds %d bytes", maxAvatarBytes)
	}
	return contentType, data, nil
}

func looksLikeHTTPURL(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func fetchAvatarURL(ctx context.Context, value string) (string, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
	if err != nil {
		return "", nil, fmt.Errorf("build avatar download request: %w", err)
	}
	response, err := avatarHTTPClient.Do(request)
	if err != nil {
		return "", nil, fmt.Errorf("download avatar url: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", nil, fmt.Errorf("download avatar url status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAvatarBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("read avatar download body: %w", err)
	}
	if len(data) == 0 {
		return "", nil, fmt.Errorf("downloaded avatar is empty")
	}
	if len(data) > maxAvatarBytes {
		return "", nil, fmt.Errorf("downloaded avatar exceeds %d bytes", maxAvatarBytes)
	}
	contentType := strings.TrimSpace(response.Header.Get("content-type"))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	return contentType, data, nil
}

func generateAvatarImage(ctx context.Context, prompt string) ([]byte, string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return nil, "", nil
	}
	payload := map[string]any{
		"model":  avatarGenerationModel(),
		"prompt": strings.TrimSpace(prompt),
		"size":   "1024x1024",
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal avatar generation request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/images/generations", bytes.NewReader(blob))
	if err != nil {
		return nil, "", fmt.Errorf("build avatar generation request: %w", err)
	}
	request.Header.Set("authorization", "Bearer "+apiKey)
	request.Header.Set("content-type", "application/json")

	response, err := avatarHTTPClient.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("send avatar generation request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 10*1024*1024))
	if err != nil {
		return nil, "", fmt.Errorf("read avatar generation response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("avatar generation status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	payloadResp := struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}{}
	if err := json.Unmarshal(body, &payloadResp); err != nil {
		return nil, "", fmt.Errorf("decode avatar generation payload: %w", err)
	}
	if len(payloadResp.Data) == 0 {
		return nil, "", fmt.Errorf("avatar generation returned no data")
	}
	first := payloadResp.Data[0]
	if strings.TrimSpace(first.B64JSON) != "" {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(first.B64JSON))
		if err != nil {
			return nil, "", fmt.Errorf("decode generated avatar image data: %w", err)
		}
		if len(data) == 0 {
			return nil, "", fmt.Errorf("generated avatar image is empty")
		}
		if len(data) > maxAvatarBytes {
			return nil, "", fmt.Errorf("generated avatar image exceeds %d bytes", maxAvatarBytes)
		}
		return data, http.DetectContentType(data), nil
	}
	if looksLikeHTTPURL(first.URL) {
		contentType, data, err := fetchAvatarURL(ctx, strings.TrimSpace(first.URL))
		if err != nil {
			return nil, "", err
		}
		return data, contentType, nil
	}
	return nil, "", fmt.Errorf("avatar generation payload missing image data")
}

func avatarGenerationModel() string {
	value := strings.TrimSpace(os.Getenv("GOPHER_AGENT_AVATAR_MODEL"))
	if value != "" {
		return value
	}
	return defaultAgentAvatarOpenAIModel
}
