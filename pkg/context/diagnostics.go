package context

type LaneDiagnostics struct {
	UsedTokens int `json:"used_tokens"`
	CapTokens  int `json:"cap_tokens"`
}

type ContextDiagnostics struct {
	ModelContextWindow   int    `json:"model_context_window"`
	ReserveTokens        int    `json:"reserve_tokens"`
	ReserveFloorTokens   int    `json:"reserve_floor_tokens"`
	EstimatedInputTokens int    `json:"estimated_input_tokens"`
	OverflowRetries      int    `json:"overflow_retries"`
	ToolResultTruncation int    `json:"tool_result_truncation_count"`
	OverflowStage        string `json:"overflow_stage,omitempty"`
	SummaryStrategy      string `json:"summary_strategy,omitempty"`

	SystemLane            LaneDiagnostics `json:"system_lane"`
	BootstrapLane         LaneDiagnostics `json:"bootstrap_lane"`
	WorkingMemoryLane     LaneDiagnostics `json:"working_memory_lane"`
	RecentMessagesLane    LaneDiagnostics `json:"recent_messages_lane"`
	RetrievedMemoryLane   LaneDiagnostics `json:"retrieved_memory_lane"`
	CompactionSummaryLane LaneDiagnostics `json:"compaction_summary_lane"`

	SelectedMemoryIDs       []string `json:"selected_memory_ids,omitempty"`
	SelectedMemoryTypes     []string `json:"selected_memory_types,omitempty"`
	MemorySearchMode        string   `json:"memory_search_mode,omitempty"`
	MemoryProvider          string   `json:"memory_provider,omitempty"`
	MemoryFallbackReason    string   `json:"memory_fallback_reason,omitempty"`
	MemoryUnavailableReason string   `json:"memory_unavailable_reason,omitempty"`
	PruneActions            []string `json:"prune_actions,omitempty"`
	CompactionActions       []string `json:"compaction_actions,omitempty"`
	PairRepairActions       []string `json:"pair_repair_actions,omitempty"`
	Warnings                []string `json:"warnings,omitempty"`
}

func ComputeReserveTokens(maxTokens int, reserveFloor int) int {
	if maxTokens <= 0 {
		return 512
	}
	if reserveFloor < 0 {
		reserveFloor = 0
	}
	reserve := (maxTokens * 15) / 100
	if reserve < reserveFloor {
		reserve = reserveFloor
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
