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
