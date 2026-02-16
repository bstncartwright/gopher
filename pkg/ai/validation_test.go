package ai

import "testing"

func TestValidateToolArgumentsCoerceTypes(t *testing.T) {
	tool := Tool{
		Name:        "sum",
		Description: "sum values",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a":  map[string]any{"type": "integer"},
				"b":  map[string]any{"type": "number"},
				"ok": map[string]any{"type": "boolean"},
			},
			"required": []any{"a", "b", "ok"},
		},
	}
	toolCall := ContentBlock{
		Type: ContentTypeToolCall,
		Name: "sum",
		Arguments: map[string]any{
			"a":  "1",
			"b":  "2.5",
			"ok": "true",
		},
	}
	validated, err := ValidateToolArguments(tool, toolCall)
	if err != nil {
		t.Fatalf("ValidateToolArguments() error: %v", err)
	}
	if _, ok := validated["a"].(int); !ok {
		t.Fatalf("expected coerced int for a, got %#v", validated["a"])
	}
	if _, ok := validated["b"].(float64); !ok {
		t.Fatalf("expected coerced float64 for b, got %#v", validated["b"])
	}
	if v, ok := validated["ok"].(bool); !ok || !v {
		t.Fatalf("expected coerced true bool for ok, got %#v", validated["ok"])
	}
}

func TestValidateToolCallMissingRequired(t *testing.T) {
	tools := []Tool{{
		Name: "sum",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{"type": "integer"},
			},
			"required": []any{"a"},
		},
	}}
	_, err := ValidateToolCall(tools, ContentBlock{Type: ContentTypeToolCall, Name: "sum", Arguments: map[string]any{}})
	if err == nil {
		t.Fatalf("expected validation error for missing required property")
	}
}
