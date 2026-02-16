package ai

func BuildBaseOptions(model Model, options *SimpleStreamOptions, apiKey string) *StreamOptions {
	if options == nil {
		return &StreamOptions{APIKey: apiKey}
	}
	maxTokens := options.MaxTokens
	if maxTokens == nil {
		limit := model.MaxTokens
		if limit > 32000 {
			limit = 32000
		}
		maxTokens = &limit
	}
	return &StreamOptions{
		Temperature:     options.Temperature,
		MaxTokens:       maxTokens,
		RequestContext:  options.RequestContext,
		APIKey:          firstNonEmpty(apiKey, options.APIKey),
		Transport:       options.Transport,
		CacheRetention:  options.CacheRetention,
		SessionID:       options.SessionID,
		OnPayload:       options.OnPayload,
		Headers:         options.Headers,
		MaxRetryDelayMS: options.MaxRetryDelayMS,
		Metadata:        options.Metadata,
		ProviderOptions: options.ProviderOptions,
	}
}

func ClampReasoning(effort ThinkingLevel) ThinkingLevel {
	if effort == ThinkingXHigh {
		return ThinkingHigh
	}
	return effort
}

func AdjustMaxTokensForThinking(baseMaxTokens, modelMaxTokens int, reasoningLevel ThinkingLevel, customBudgets *ThinkingBudgets) (maxTokens, thinkingBudget int) {
	defaultBudgets := ThinkingBudgets{Minimal: 1024, Low: 2048, Medium: 8192, High: 16384}
	if customBudgets != nil {
		if customBudgets.Minimal > 0 {
			defaultBudgets.Minimal = customBudgets.Minimal
		}
		if customBudgets.Low > 0 {
			defaultBudgets.Low = customBudgets.Low
		}
		if customBudgets.Medium > 0 {
			defaultBudgets.Medium = customBudgets.Medium
		}
		if customBudgets.High > 0 {
			defaultBudgets.High = customBudgets.High
		}
	}
	level := ClampReasoning(reasoningLevel)
	switch level {
	case ThinkingMinimal:
		thinkingBudget = defaultBudgets.Minimal
	case ThinkingLow:
		thinkingBudget = defaultBudgets.Low
	case ThinkingMedium:
		thinkingBudget = defaultBudgets.Medium
	default:
		thinkingBudget = defaultBudgets.High
	}

	maxTokens = baseMaxTokens + thinkingBudget
	if modelMaxTokens > 0 && maxTokens > modelMaxTokens {
		maxTokens = modelMaxTokens
	}
	const minOutputTokens = 1024
	if maxTokens <= thinkingBudget {
		thinkingBudget = max(0, maxTokens-minOutputTokens)
	}
	return maxTokens, thinkingBudget
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
