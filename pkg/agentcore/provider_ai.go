package agentcore

import "github.com/bstncartwright/gopher/pkg/ai"

type AIStreamProvider struct{}

func (AIStreamProvider) Stream(model ai.Model, conversation ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamSimple(model, conversation, options)
}
