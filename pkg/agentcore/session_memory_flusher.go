package agentcore

import (
	"context"
	"fmt"
	"strings"

	"github.com/bstncartwright/gopher/pkg/memory"
	"github.com/bstncartwright/gopher/pkg/memory/ingest"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type sessionEventLister interface {
	List(ctx context.Context, sessionID sessionrt.SessionID) ([]sessionrt.Event, error)
}

type StoreBackedSessionMemoryFlusher struct {
	store     sessionEventLister
	manager   memory.MemoryManager
	extractor *ingest.Extractor
	agentID   string
}

func NewStoreBackedSessionMemoryFlusher(store sessionEventLister, manager memory.MemoryManager, agentID string) *StoreBackedSessionMemoryFlusher {
	if store == nil || manager == nil {
		return nil
	}
	return &StoreBackedSessionMemoryFlusher{
		store:     store,
		manager:   manager,
		extractor: ingest.NewExtractor(ingest.ExtractorOptions{}),
		agentID:   strings.TrimSpace(agentID),
	}
}

func (f *StoreBackedSessionMemoryFlusher) FlushSession(ctx context.Context, sessionID string) error {
	if f == nil || f.store == nil || f.manager == nil {
		return nil
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return nil
	}
	events, err := f.store.List(ctx, sessionrt.SessionID(trimmedSessionID))
	if err != nil {
		return fmt.Errorf("load session events for memory flush: %w", err)
	}
	if len(events) == 0 {
		return nil
	}
	records := f.extractor.ExtractSession(trimmedSessionID, f.agentID, events)
	var firstErr error
	for _, record := range records {
		if err := f.manager.Store(ctx, record); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return fmt.Errorf("store flushed session memories: %w", firstErr)
	}
	return nil
}
