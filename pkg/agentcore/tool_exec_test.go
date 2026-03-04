package agentcore

import (
	"context"
	"testing"
)

func TestExecToolOpencodeFailureAddsTroubleshooting(t *testing.T) {
	tool := &execTool{}
	output, err := tool.Run(context.Background(), ToolInput{
		Session: &Session{ID: "session-opencode-failure"},
		Args: map[string]any{
			"command": "opencode run --format json --definitely-invalid-flag \"hello\"",
			"timeout": 5,
		},
	})
	if err == nil {
		t.Fatalf("expected exec error for invalid opencode command")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("expected error status, got %q", output.Status)
	}

	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected structured result map")
	}
	if _, ok := result["opencode_troubleshooting"].(map[string]any); !ok {
		t.Fatalf("expected opencode troubleshooting hints in result, got %#v", result)
	}
}

func TestExecToolNonOpencodeFailureDoesNotAddTroubleshooting(t *testing.T) {
	tool := &execTool{}
	output, err := tool.Run(context.Background(), ToolInput{
		Session: &Session{ID: "session-non-opencode-failure"},
		Args: map[string]any{
			"command": "false",
			"timeout": 5,
		},
	})
	if err == nil {
		t.Fatalf("expected exec error for false command")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("expected error status, got %q", output.Status)
	}

	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected structured result map")
	}
	if _, exists := result["opencode_troubleshooting"]; exists {
		t.Fatalf("did not expect opencode troubleshooting for non-opencode command")
	}
}

func TestExecToolDefersGatewayRestartCommands(t *testing.T) {
	tool := &execTool{}
	output, err := tool.Run(context.Background(), ToolInput{
		Session: &Session{ID: "session-restart-defer"},
		Args: map[string]any{
			"command": "/home/exedev/.local/bin/gopher restart",
		},
	})
	if err != nil {
		t.Fatalf("expected deferred restart to succeed, got error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}
	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected structured result map")
	}
	if deferred, _ := result["deferred"].(bool); !deferred {
		t.Fatalf("expected deferred=true in result, got %#v", result)
	}
}

func TestExecToolAllowsNonRestartGopherCommands(t *testing.T) {
	tool := &execTool{}
	output, err := tool.Run(context.Background(), ToolInput{
		Session: &Session{ID: "session-gopher-help"},
		Args: map[string]any{
			"command": "gopher --help | head -n 1",
			"timeout": 5,
		},
	})
	if err != nil {
		t.Fatalf("expected gopher help command to run, got error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}
}
