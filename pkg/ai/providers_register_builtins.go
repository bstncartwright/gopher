package ai

func RegisterBuiltInAPIProviders() {
	RegisterAPIProvider(APIProvider{
		API:          APIOpenAICompletions,
		Stream:       StreamOpenAICompletions,
		StreamSimple: StreamSimpleOpenAICompletions,
	}, "builtin")

	RegisterAPIProvider(APIProvider{
		API:          APIOpenAIResponses,
		Stream:       StreamOpenAIResponses,
		StreamSimple: StreamSimpleOpenAIResponses,
	}, "builtin")

	RegisterAPIProvider(APIProvider{
		API:          APIOpenAICodexResponse,
		Stream:       StreamOpenAICodexResponses,
		StreamSimple: StreamSimpleOpenAICodexResponses,
	}, "builtin")

	RegisterAPIProvider(APIProvider{
		API:          APIAnthropicMessages,
		Stream:       StreamAnthropicMessages,
		StreamSimple: StreamSimpleAnthropicMessages,
	}, "builtin")
}

func ResetAPIProviders() {
	ClearAPIProviders()
	RegisterBuiltInAPIProviders()
}

func init() {
	RegisterBuiltInAPIProviders()
}
