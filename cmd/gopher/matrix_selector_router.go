package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

const (
	matrixRouterModelOverrideEnv = "GOPHER_MATRIX_ROUTER_MODEL"
	matrixRouterTimeout          = 4 * time.Second
)

var matrixRouterPreferredModels = []string{
	"kimi-coding:k2p5",
	"openai:gpt-5-mini",
}

type matrixLLMUntaggedResponderRouter struct {
	model  ai.Model
	apiKey string
	logger *log.Logger
}

func newMatrixLLMUntaggedResponderRouter(runtime *gatewayAgentRuntime, logger *log.Logger) matrixUntaggedResponderRouter {
	model, apiKey, err := resolveMatrixRouterModel(runtime)
	if err != nil {
		if logger != nil {
			logger.Printf("matrix untagged router disabled: %v", err)
		}
		return nil
	}
	if logger != nil {
		logger.Printf("matrix untagged router enabled model=%s:%s", model.Provider, model.ID)
	}
	return &matrixLLMUntaggedResponderRouter{
		model:  model,
		apiKey: apiKey,
		logger: logger,
	}
}

func (r *matrixLLMUntaggedResponderRouter) SelectResponders(ctx context.Context, input matrixUntaggedResponderSelectionInput) ([]sessionrt.ActorID, error) {
	if r == nil {
		return nil, nil
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		return nil, nil
	}
	candidates := normalizeActorIDs(input.CandidateActors)
	if len(candidates) == 0 {
		return nil, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, matrixRouterTimeout)
	defer cancel()

	maxTokens := 120
	result, err := ai.CompleteSimple(r.model, ai.Context{
		SystemPrompt: matrixRouterSystemPrompt,
		Messages: []ai.Message{
			ai.NewUserTextMessage(buildMatrixRouterUserPrompt(message, candidates, input.CandidateUserByActor)),
		},
	}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			RequestContext: requestCtx,
			APIKey:         r.apiKey,
			MaxTokens:      &maxTokens,
		},
		Reasoning: ai.ThinkingMinimal,
	})
	if err != nil {
		return nil, err
	}

	raw := strings.TrimSpace(assistantText(result.Content))
	selected := parseRouterSelection(raw)
	selected = limitToCandidateActors(selected, candidates)
	if r.logger != nil {
		r.logger.Printf("matrix untagged route candidates=%v selected=%v", actorIDsToStrings(candidates), actorIDsToStrings(selected))
	}
	return selected, nil
}

func resolveMatrixRouterModel(runtime *gatewayAgentRuntime) (ai.Model, string, error) {
	specs := make([]string, 0, 4)
	seen := map[string]struct{}{}
	addSpec := func(spec string) {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			return
		}
		if _, exists := seen[spec]; exists {
			return
		}
		seen[spec] = struct{}{}
		specs = append(specs, spec)
	}

	addSpec(os.Getenv(matrixRouterModelOverrideEnv))
	for _, spec := range matrixRouterPreferredModels {
		addSpec(spec)
	}
	if spec := defaultAgentModelSpec(runtime); spec != "" {
		addSpec(spec)
	}

	lastErr := error(nil)
	for _, spec := range specs {
		provider, modelID, err := parseModelSpec(spec)
		if err != nil {
			lastErr = err
			continue
		}
		model, ok := ai.GetModel(provider, modelID)
		if !ok {
			lastErr = fmt.Errorf("model not found for %q", spec)
			continue
		}
		apiKey := strings.TrimSpace(ai.GetEnvAPIKey(provider))
		if providerRequiresAPIKey(model.Provider) && apiKey == "" {
			lastErr = fmt.Errorf("missing API key for provider %q", provider)
			continue
		}
		return model, apiKey, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no router model candidates available")
	}
	return ai.Model{}, "", lastErr
}

func defaultAgentModelSpec(runtime *gatewayAgentRuntime) string {
	if runtime == nil || len(runtime.Agents) == 0 {
		return ""
	}
	defaultActorID := runtime.DefaultActorID
	if strings.TrimSpace(string(defaultActorID)) == "" {
		actorIDs := make([]string, 0, len(runtime.Agents))
		for actorID := range runtime.Agents {
			actorIDs = append(actorIDs, string(actorID))
		}
		sort.Strings(actorIDs)
		if len(actorIDs) == 0 {
			return ""
		}
		defaultActorID = sessionrt.ActorID(actorIDs[0])
	}
	agent, ok := runtime.Agents[defaultActorID]
	if !ok || agent == nil {
		return ""
	}
	return strings.TrimSpace(agent.Config.ModelPolicy)
}

func parseModelSpec(spec string) (provider string, modelID string, err error) {
	parts := strings.SplitN(strings.TrimSpace(spec), ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid model spec %q", spec)
	}
	provider = strings.TrimSpace(parts[0])
	modelID = strings.TrimSpace(parts[1])
	if provider == "" || modelID == "" {
		return "", "", fmt.Errorf("invalid model spec %q", spec)
	}
	return provider, modelID, nil
}

func providerRequiresAPIKey(provider ai.Provider) bool {
	return provider != ai.ProviderOllama
}

func buildMatrixRouterUserPrompt(message string, candidates []sessionrt.ActorID, candidateUsers map[sessionrt.ActorID]string) string {
	lines := make([]string, 0, len(candidates)+8)
	lines = append(lines, "User message:")
	lines = append(lines, message)
	lines = append(lines, "")
	lines = append(lines, "Candidate gophers:")
	for _, actorID := range candidates {
		userID := ""
		if candidateUsers != nil {
			userID = strings.TrimSpace(candidateUsers[actorID])
		}
		if userID != "" {
			lines = append(lines, fmt.Sprintf("- actor_id=%s matrix_user=%s", actorID, userID))
			continue
		}
		lines = append(lines, fmt.Sprintf("- actor_id=%s", actorID))
	}
	lines = append(lines, "")
	lines = append(lines, "Return JSON only.")
	lines = append(lines, "Schema: {\"actor_ids\": [\"...\"]}")
	lines = append(lines, "Use only listed actor_ids. Return [] when nobody should respond.")
	return strings.Join(lines, "\n")
}

const matrixRouterSystemPrompt = "You route room messages to one or more gophers. " +
	"Only choose gophers that should actively answer this specific user message. " +
	"If no gopher should answer, return an empty list. " +
	"Output JSON only with shape {\"actor_ids\":[...]} and no extra text."

func assistantText(blocks []ai.ContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == ai.ContentTypeText {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

func parseRouterSelection(raw string) []sessionrt.ActorID {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	type response struct {
		ActorIDs []string `json:"actor_ids"`
		Actors   []string `json:"actors"`
	}
	out := response{}
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		if len(out.ActorIDs) > 0 {
			return stringSliceToActorIDs(out.ActorIDs)
		}
		if len(out.Actors) > 0 {
			return stringSliceToActorIDs(out.Actors)
		}
	}

	obj := ai.ParseStreamingJSON(raw)
	values := stringSliceFromJSONField(obj, "actor_ids")
	if len(values) == 0 {
		values = stringSliceFromJSONField(obj, "actors")
	}
	return stringSliceToActorIDs(values)
}

func stringSliceFromJSONField(obj map[string]any, key string) []string {
	if len(obj) == 0 {
		return nil
	}
	raw, ok := obj[key]
	if !ok || raw == nil {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

func limitToCandidateActors(selected []sessionrt.ActorID, candidates []sessionrt.ActorID) []sessionrt.ActorID {
	if len(selected) == 0 || len(candidates) == 0 {
		return nil
	}
	candidateSet := make(map[sessionrt.ActorID]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidateSet[candidate] = struct{}{}
	}
	out := make([]sessionrt.ActorID, 0, len(selected))
	seen := make(map[sessionrt.ActorID]struct{}, len(selected))
	for _, actorID := range selected {
		actorID = sessionrt.ActorID(strings.TrimSpace(string(actorID)))
		if strings.TrimSpace(string(actorID)) == "" {
			continue
		}
		if _, ok := candidateSet[actorID]; !ok {
			continue
		}
		if _, ok := seen[actorID]; ok {
			continue
		}
		seen[actorID] = struct{}{}
		out = append(out, actorID)
	}
	return out
}

func normalizeActorIDs(in []sessionrt.ActorID) []sessionrt.ActorID {
	if len(in) == 0 {
		return nil
	}
	out := make([]sessionrt.ActorID, 0, len(in))
	seen := map[sessionrt.ActorID]struct{}{}
	for _, actorID := range in {
		actorID = sessionrt.ActorID(strings.TrimSpace(string(actorID)))
		if strings.TrimSpace(string(actorID)) == "" {
			continue
		}
		if _, exists := seen[actorID]; exists {
			continue
		}
		seen[actorID] = struct{}{}
		out = append(out, actorID)
	}
	return out
}

func stringSliceToActorIDs(in []string) []sessionrt.ActorID {
	if len(in) == 0 {
		return nil
	}
	out := make([]sessionrt.ActorID, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, sessionrt.ActorID(item))
	}
	return out
}

func actorIDsToStrings(in []sessionrt.ActorID) []string {
	out := make([]string, 0, len(in))
	for _, actorID := range in {
		out = append(out, string(actorID))
	}
	return out
}
