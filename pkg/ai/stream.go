package ai

import (
	"context"
	"fmt"
)

func resolveAPIProvider(api API) (APIProvider, error) {
	provider, ok := GetAPIProvider(api)
	if !ok {
		return APIProvider{}, fmt.Errorf("no API provider registered for api: %s", api)
	}
	return provider, nil
}

func Stream(model Model, conversation Context, options *StreamOptions) *AssistantMessageEventStream {
	provider, err := resolveAPIProvider(model.API)
	if err != nil {
		s := CreateAssistantMessageEventStream()
		msg := NewAssistantMessage(model)
		msg.StopReason = StopReasonError
		msg.ErrorMessage = err.Error()
		s.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &msg})
		s.End(&msg)
		return s
	}
	return provider.Stream(model, conversation, options)
}

func Complete(model Model, conversation Context, options *StreamOptions) (AssistantMessage, error) {
	s := Stream(model, conversation, options)
	ctx := context.Background()
	if options != nil && options.RequestContext != nil {
		ctx = options.RequestContext
	}
	return s.Result(ctx)
}

func StreamSimple(model Model, conversation Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
	provider, err := resolveAPIProvider(model.API)
	if err != nil {
		s := CreateAssistantMessageEventStream()
		msg := NewAssistantMessage(model)
		msg.StopReason = StopReasonError
		msg.ErrorMessage = err.Error()
		s.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &msg})
		s.End(&msg)
		return s
	}
	return provider.StreamSimple(model, conversation, options)
}

func CompleteSimple(model Model, conversation Context, options *SimpleStreamOptions) (AssistantMessage, error) {
	s := StreamSimple(model, conversation, options)
	ctx := context.Background()
	if options != nil && options.RequestContext != nil {
		ctx = options.RequestContext
	}
	return s.Result(ctx)
}
