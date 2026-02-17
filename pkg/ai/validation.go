package ai

import (
	"fmt"
	"strconv"
)

func ValidateToolCall(tools []Tool, toolCall ContentBlock) (map[string]any, error) {
	for _, tool := range tools {
		if tool.Name == toolCall.Name {
			return ValidateToolArguments(tool, toolCall)
		}
	}
	return nil, fmt.Errorf("tool %q not found", toolCall.Name)
}

func ValidateToolArguments(tool Tool, toolCall ContentBlock) (map[string]any, error) {
	if toolCall.Type != ContentTypeToolCall {
		return nil, fmt.Errorf("content block %q is not a toolCall", toolCall.Type)
	}
	if tool.Parameters == nil {
		return CloneMap(toolCall.Arguments), nil
	}

	validated, err := validateSchemaValue(tool.Parameters, toolCall.Arguments, "root")
	if err != nil {
		return nil, fmt.Errorf("validation failed for tool %q: %w", toolCall.Name, err)
	}
	out, ok := validated.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("validated arguments are not an object")
	}
	return out, nil
}

func validateSchemaValue(schema map[string]any, value any, path string) (any, error) {
	typeName, _ := schema["type"].(string)
	if enumValues, ok := schema["enum"].([]any); ok && len(enumValues) > 0 {
		matched := false
		for _, candidate := range enumValues {
			if fmt.Sprint(candidate) == fmt.Sprint(value) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("%s: value %v is not one of the allowed enum values", path, value)
		}
	}

	switch typeName {
	case "", "object":
		input, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: expected object", path)
		}
		out := CloneMap(input)
		props, _ := schema["properties"].(map[string]any)
		requiredRaw, _ := schema["required"].([]any)

		required := map[string]struct{}{}
		for _, r := range requiredRaw {
			required[fmt.Sprint(r)] = struct{}{}
		}

		for name := range required {
			if _, ok := out[name]; !ok {
				return nil, fmt.Errorf("%s.%s: required field missing", path, name)
			}
		}

		for key, propSchemaRaw := range props {
			propSchema, ok := propSchemaRaw.(map[string]any)
			if !ok {
				continue
			}
			v, exists := out[key]
			if !exists {
				continue
			}
			validated, err := validateSchemaValue(propSchema, v, path+"."+key)
			if err != nil {
				return nil, err
			}
			out[key] = validated
		}
		return out, nil
	case "string":
		switch t := value.(type) {
		case string:
			return t, nil
		case fmt.Stringer:
			return t.String(), nil
		default:
			return fmt.Sprint(value), nil
		}
	case "number":
		switch t := value.(type) {
		case float64:
			return t, nil
		case float32:
			return float64(t), nil
		case int:
			return float64(t), nil
		case int64:
			return float64(t), nil
		case jsonNumberLike:
			f, err := strconv.ParseFloat(string(t), 64)
			if err != nil {
				return nil, fmt.Errorf("%s: expected number", path)
			}
			return f, nil
		case string:
			f, err := strconv.ParseFloat(t, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: expected number", path)
			}
			return f, nil
		default:
			return nil, fmt.Errorf("%s: expected number", path)
		}
	case "integer":
		switch t := value.(type) {
		case int:
			return t, nil
		case int64:
			return int(t), nil
		case float64:
			return int(t), nil
		case string:
			i, err := strconv.Atoi(t)
			if err != nil {
				return nil, fmt.Errorf("%s: expected integer", path)
			}
			return i, nil
		default:
			return nil, fmt.Errorf("%s: expected integer", path)
		}
	case "boolean":
		switch t := value.(type) {
		case bool:
			return t, nil
		case string:
			b, err := strconv.ParseBool(t)
			if err != nil {
				return nil, fmt.Errorf("%s: expected boolean", path)
			}
			return b, nil
		default:
			return nil, fmt.Errorf("%s: expected boolean", path)
		}
	case "array":
		itemsSchema, _ := schema["items"].(map[string]any)
		arr, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("%s: expected array", path)
		}
		if itemsSchema == nil {
			return arr, nil
		}
		out := make([]any, len(arr))
		for i, item := range arr {
			v, err := validateSchemaValue(itemsSchema, item, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	default:
		return value, nil
	}
}

type jsonNumberLike string
