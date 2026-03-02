package agentcore

import (
	"encoding/json"
	"fmt"
	"sync"
)

type LoopDetectionConfig struct {
	Enabled                       bool `json:"enabled"`
	WarningThreshold              int  `json:"warning_threshold"`
	CriticalThreshold             int  `json:"critical_threshold"`
	GlobalCircuitBreakerThreshold int  `json:"global_circuit_breaker_threshold"`
	HistorySize                   int  `json:"history_size"`
}

type LoopDetectLevel int

const (
	LoopLevelNone LoopDetectLevel = iota
	LoopLevelWarning
	LoopLevelCritical
	LoopLevelCircuitBreaker
)

type LoopDetectResult struct {
	Level   LoopDetectLevel
	Message string
	Pattern string
}

type toolCallRecord struct {
	Name   string
	Args   string
	Output string
}

type LoopDetector struct {
	config  LoopDetectionConfig
	history []toolCallRecord
	mu      sync.Mutex
}

func NewLoopDetector(config LoopDetectionConfig) *LoopDetector {
	if config.Enabled {
		if config.WarningThreshold == 0 {
			config.WarningThreshold = 10
		}
		if config.CriticalThreshold == 0 {
			config.CriticalThreshold = 20
		}
		if config.GlobalCircuitBreakerThreshold == 0 {
			config.GlobalCircuitBreakerThreshold = 30
		}
		if config.HistorySize == 0 {
			config.HistorySize = 30
		}
	}
	return &LoopDetector{
		config:  config,
		history: make([]toolCallRecord, 0),
	}
}

func (ld *LoopDetector) Check(name string, args map[string]any) LoopDetectResult {
	if !ld.config.Enabled {
		return LoopDetectResult{Level: LoopLevelNone}
	}
	ld.mu.Lock()
	defer ld.mu.Unlock()

	results := []LoopDetectResult{
		ld.genericRepeat(name, args),
		ld.knownPollNoProgress(name, args),
		ld.pingPong(name, args),
	}

	best := LoopDetectResult{Level: LoopLevelNone}
	for _, r := range results {
		if r.Level > best.Level {
			best = r
		}
	}
	return best
}

func (ld *LoopDetector) Record(name string, args map[string]any, output any) {
	if !ld.config.Enabled {
		return
	}
	ld.mu.Lock()
	defer ld.mu.Unlock()

	argsStr := marshalForComparison(args)
	outputStr := marshalForComparison(output)
	if len(outputStr) > 512 {
		outputStr = outputStr[:512]
	}

	ld.history = append(ld.history, toolCallRecord{
		Name:   name,
		Args:   argsStr,
		Output: outputStr,
	})

	if len(ld.history) > ld.config.HistorySize {
		ld.history = ld.history[len(ld.history)-ld.config.HistorySize:]
	}
}

func (ld *LoopDetector) genericRepeat(name string, args map[string]any) LoopDetectResult {
	argsStr := marshalForComparison(args)
	count := 0
	for i := len(ld.history) - 1; i >= 0; i-- {
		rec := ld.history[i]
		if rec.Name == name && rec.Args == argsStr {
			count++
		} else {
			break
		}
	}
	count++ // include the current incoming call
	return ld.severity(count, "generic_repeat")
}

func (ld *LoopDetector) knownPollNoProgress(name string, args map[string]any) LoopDetectResult {
	if name != "process" {
		return LoopDetectResult{Level: LoopLevelNone}
	}
	action, _ := args["action"].(string)
	if action != "poll" {
		return LoopDetectResult{Level: LoopLevelNone}
	}

	if len(ld.history) == 0 {
		return LoopDetectResult{Level: LoopLevelNone}
	}

	last := ld.history[len(ld.history)-1]
	if last.Name != "process" || !isPollAction(last.Args) {
		return LoopDetectResult{Level: LoopLevelNone}
	}

	refOutput := last.Output
	count := 0
	for i := len(ld.history) - 1; i >= 0; i-- {
		rec := ld.history[i]
		if rec.Name == "process" && isPollAction(rec.Args) && rec.Output == refOutput {
			count++
		} else {
			break
		}
	}
	count++ // include the current incoming call
	return ld.severity(count, "poll_no_progress")
}

func (ld *LoopDetector) pingPong(name string, args map[string]any) LoopDetectResult {
	if len(ld.history) < 3 {
		return LoopDetectResult{Level: LoopLevelNone}
	}

	currentKey := recordKey(name, marshalForComparison(args))
	lastKey := recordKey(ld.history[len(ld.history)-1].Name, ld.history[len(ld.history)-1].Args)

	if currentKey == lastKey {
		return LoopDetectResult{Level: LoopLevelNone}
	}

	keyA := currentKey
	keyB := lastKey

	count := 1 // current call
	expect := keyB
	for i := len(ld.history) - 1; i >= 0; i-- {
		k := recordKey(ld.history[i].Name, ld.history[i].Args)
		if k != expect {
			break
		}
		count++
		if expect == keyA {
			expect = keyB
		} else {
			expect = keyA
		}
	}

	if count < 4 {
		return LoopDetectResult{Level: LoopLevelNone}
	}
	return ld.severity(count, "ping_pong")
}

func (ld *LoopDetector) severity(count int, pattern string) LoopDetectResult {
	switch {
	case count >= ld.config.GlobalCircuitBreakerThreshold:
		return LoopDetectResult{
			Level:   LoopLevelCircuitBreaker,
			Message: fmt.Sprintf("circuit breaker: %d repetitive tool calls detected (%s), ending turn", count, pattern),
			Pattern: pattern,
		}
	case count >= ld.config.CriticalThreshold:
		return LoopDetectResult{
			Level:   LoopLevelCritical,
			Message: fmt.Sprintf("loop detected: %d repetitive tool calls (%s), tool call blocked", count, pattern),
			Pattern: pattern,
		}
	case count >= ld.config.WarningThreshold:
		return LoopDetectResult{
			Level:   LoopLevelWarning,
			Message: fmt.Sprintf("warning: %d repetitive tool calls detected (%s), consider changing approach", count, pattern),
			Pattern: pattern,
		}
	default:
		return LoopDetectResult{Level: LoopLevelNone}
	}
}

func marshalForComparison(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func recordKey(name, args string) string {
	return name + "|" + args
}

func isPollAction(argsJSON string) bool {
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return false
	}
	action, _ := m["action"].(string)
	return action == "poll"
}
