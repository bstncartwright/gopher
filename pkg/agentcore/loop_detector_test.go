package agentcore

import "testing"

func testConfig() LoopDetectionConfig {
	return LoopDetectionConfig{
		Enabled:                       true,
		WarningThreshold:              3,
		CriticalThreshold:             5,
		GlobalCircuitBreakerThreshold: 8,
		HistorySize:                   10,
	}
}

func TestLoopDetectorDisabledReturnsNone(t *testing.T) {
	ld := NewLoopDetector(LoopDetectionConfig{Enabled: false})

	for i := 0; i < 20; i++ {
		ld.Record("read", map[string]any{"path": "/tmp/a"}, "content")
	}
	result := ld.Check("read", map[string]any{"path": "/tmp/a"})
	if result.Level != LoopLevelNone {
		t.Fatalf("expected LoopLevelNone, got %d", result.Level)
	}
}

func TestLoopDetectorGenericRepeat(t *testing.T) {
	cfg := testConfig()
	args := map[string]any{"path": "/tmp/a"}

	tests := []struct {
		name     string
		records  int
		wantLvl  LoopDetectLevel
		wantPat  string
	}{
		{"below warning", 1, LoopLevelNone, ""},
		{"at warning", 2, LoopLevelWarning, "generic_repeat"},
		{"at critical", 4, LoopLevelCritical, "generic_repeat"},
		{"at circuit breaker", 7, LoopLevelCircuitBreaker, "generic_repeat"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ld := NewLoopDetector(cfg)
			for i := 0; i < tt.records; i++ {
				ld.Record("read", args, "output")
			}
			result := ld.Check("read", args)
			if result.Level != tt.wantLvl {
				t.Fatalf("expected level %d, got %d (message: %s)", tt.wantLvl, result.Level, result.Message)
			}
			if result.Pattern != tt.wantPat {
				t.Fatalf("expected pattern %q, got %q", tt.wantPat, result.Pattern)
			}
		})
	}
}

func TestLoopDetectorPollNoProgress(t *testing.T) {
	cfg := testConfig()
	output := "still running"

	// Use 3 rotating pids so genericRepeat doesn't fire (consecutive
	// identical args) and pingPong doesn't fire (only detects 2-call
	// alternation). pollNoProgress only checks that consecutive entries
	// are process+poll with the same output.
	pollArgs := func(i int) map[string]any {
		return map[string]any{"action": "poll", "pid": float64(i % 3)}
	}

	tests := []struct {
		name    string
		records int
		wantLvl LoopDetectLevel
	}{
		{"below warning", 1, LoopLevelNone},
		{"at warning", 2, LoopLevelWarning},
		{"at critical", 4, LoopLevelCritical},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ld := NewLoopDetector(cfg)
			for i := 0; i < tt.records; i++ {
				ld.Record("process", pollArgs(i), output)
			}
			result := ld.Check("process", pollArgs(tt.records))
			if result.Level != tt.wantLvl {
				t.Fatalf("expected level %d, got %d (message: %s)", tt.wantLvl, result.Level, result.Message)
			}
			if tt.wantLvl != LoopLevelNone && result.Pattern != "poll_no_progress" {
				t.Fatalf("expected pattern poll_no_progress, got %q", result.Pattern)
			}
		})
	}

	t.Run("different output resets", func(t *testing.T) {
		ld := NewLoopDetector(cfg)
		for i := 0; i < 3; i++ {
			ld.Record("process", pollArgs(i), output)
		}
		ld.Record("process", pollArgs(3), "different output")
		result := ld.Check("process", pollArgs(4))
		if result.Level != LoopLevelNone {
			t.Fatalf("expected LoopLevelNone after output change, got %d", result.Level)
		}
	})
}

func TestLoopDetectorPingPong(t *testing.T) {
	cfg := testConfig()
	argsA := map[string]any{"path": "/a"}
	argsB := map[string]any{"path": "/b"}

	t.Run("detected after 4 alternations", func(t *testing.T) {
		ld := NewLoopDetector(cfg)
		// Record A, B, A (3 entries). Check B -> count=4 (B,A,B,A).
		ld.Record("read", argsA, "out")
		ld.Record("read", argsB, "out")
		ld.Record("read", argsA, "out")
		result := ld.Check("read", argsB)
		if result.Level == LoopLevelNone {
			t.Fatalf("expected ping_pong detection, got LoopLevelNone")
		}
		if result.Pattern != "ping_pong" {
			t.Fatalf("expected pattern ping_pong, got %q", result.Pattern)
		}
	})

	t.Run("not detected with fewer than 4", func(t *testing.T) {
		ld := NewLoopDetector(cfg)
		// Record A, B (2 entries). Check A -> count=3, but pingPong requires >=4.
		ld.Record("read", argsA, "out")
		ld.Record("read", argsB, "out")
		result := ld.Check("read", argsA)
		if result.Pattern == "ping_pong" {
			t.Fatalf("ping_pong should not trigger with only 3 alternations")
		}
	})

	t.Run("reaches warning threshold", func(t *testing.T) {
		ld := NewLoopDetector(cfg)
		// Build alternating: A B A B A -> 5 entries. Check B -> count=6.
		for i := 0; i < 5; i++ {
			if i%2 == 0 {
				ld.Record("read", argsA, "out")
			} else {
				ld.Record("read", argsB, "out")
			}
		}
		result := ld.Check("read", argsB)
		if result.Level < LoopLevelCritical {
			t.Fatalf("expected at least critical with count 6, got level %d", result.Level)
		}
	})
}

func TestLoopDetectorHistorySizeTrimming(t *testing.T) {
	cfg := testConfig() // HistorySize=10
	ld := NewLoopDetector(cfg)

	repeatArgs := map[string]any{"path": "/repeat"}

	// Record 2 identical calls (one short of warning with the Check).
	ld.Record("read", repeatArgs, "out")
	ld.Record("read", repeatArgs, "out")

	// Now record 10 unique calls to push the repeated ones out of history.
	for i := 0; i < 10; i++ {
		ld.Record("write", map[string]any{"idx": i}, "ok")
	}

	// The 2 earlier "read" records should be trimmed.
	// A Check for "read" should see no consecutive matches -> None.
	result := ld.Check("read", repeatArgs)
	if result.Level != LoopLevelNone {
		t.Fatalf("expected LoopLevelNone after trimming, got %d (message: %s)", result.Level, result.Message)
	}

	// Verify that new consecutive records still work after trimming.
	ld2 := NewLoopDetector(cfg)
	for i := 0; i < 15; i++ {
		ld2.Record("read", repeatArgs, "out")
	}
	// Despite 15 records, only last 10 remain. Check adds 1 -> count=11 -> circuit breaker (>=8).
	result = ld2.Check("read", repeatArgs)
	if result.Level != LoopLevelCircuitBreaker {
		t.Fatalf("expected circuit breaker after 15+1 records (10 in history), got %d", result.Level)
	}
}
