package context

type LaneDiagnostics struct {
	UsedTokens int `json:"used_tokens"`
	CapTokens  int `json:"cap_tokens"`
}

type ContextDiagnostics struct {
	ModelContextWindow   int `json:"model_context_window"`
	ReserveTokens        int `json:"reserve_tokens"`
	EstimatedInputTokens int `json:"estimated_input_tokens"`
	OverflowRetries      int `json:"overflow_retries"`

	SystemLane            LaneDiagnostics `json:"system_lane"`
	BootstrapLane         LaneDiagnostics `json:"bootstrap_lane"`
	WorkingMemoryLane     LaneDiagnostics `json:"working_memory_lane"`
	RecentMessagesLane    LaneDiagnostics `json:"recent_messages_lane"`
	RetrievedMemoryLane   LaneDiagnostics `json:"retrieved_memory_lane"`
	CompactionSummaryLane LaneDiagnostics `json:"compaction_summary_lane"`

	SelectedMemoryIDs   []string `json:"selected_memory_ids,omitempty"`
	SelectedMemoryTypes []string `json:"selected_memory_types,omitempty"`
	PruneActions        []string `json:"prune_actions,omitempty"`
	CompactionActions   []string `json:"compaction_actions,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
}

func ComputeReserveTokens(maxTokens int) int {
	if maxTokens <= 0 {
		return 512
	}
	reserve := (maxTokens * 15) / 100
	if reserve < 512 {
		reserve = 512
	}
	if reserve > 4096 {
		reserve = 4096
	}
	half := maxTokens / 2
	if half > 0 && reserve > half {
		reserve = half
	}
	if reserve < 0 {
		reserve = 0
	}
	return reserve
}
