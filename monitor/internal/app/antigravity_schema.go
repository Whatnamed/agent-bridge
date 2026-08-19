package app

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/whatnamed/agent-bridge/shared/antigravity/cloudcode"
)

const maxAntigravitySchemaDepth = 32

var antigravityResponseSchemaKeys = map[string]struct{}{
	"$ref": {},
	"type": {}, "title": {}, "description": {}, "properties": {}, "required": {}, "enum": {},
	"format": {}, "minimum": {}, "maximum": {}, "items": {}, "prefixItems": {},
	"minItems": {}, "maxItems": {}, "anyOf": {}, "additionalProperties": {},
}

var antigravityToolSchemaKeys = map[string]struct{}{
	"type": {}, "description": {}, "properties": {}, "items": {}, "required": {}, "enum": {},
}

func validateAntigravityResponseSchema(value any) error {
	if list, ok := value.([]any); ok {
		if len(list) == 0 {
			return errorsForAntigravitySchema("schema array must not be empty")
		}
		for index, item := range list {
			if err := validateAntigravityResponseSchemaNode(item, fmt.Sprintf("schema[%d]", index), 0); err != nil {
				return err
			}
		}
		return nil
	}
	if _, ok := value.(map[string]any); !ok {
		return errorsForAntigravitySchema("schema must be an object or array")
	}
	return validateAntigravityResponseSchemaNode(value, "schema", 0)
}

func validateAntigravityResponseSchemaNode(value any, path string, depth int) error {
	if depth > maxAntigravitySchemaDepth {
		return errorsForAntigravitySchema("schema nesting is too deep")
	}
	m := mapAny(value)
	if m == nil {
		return errorsForAntigravitySchema(fmt.Sprintf("%s must be an object", path))
	}
	if len(m) == 0 {
		return errorsForAntigravitySchema(fmt.Sprintf("%s must not be empty", path))
	}
	for key := range m {
		if _, ok := antigravityResponseSchemaKeys[key]; !ok {
			return errorsForAntigravitySchema(fmt.Sprintf("unsupported schema keyword %q", key))
		}
	}
	if raw, ok := m["$ref"]; ok {
		ref, isString := raw.(string)
		if !isString || strings.TrimSpace(ref) == "" {
			return errorsForAntigravitySchema(fmt.Sprintf("%s.$ref must be a non-empty string", path))
		}
	}
	if raw, ok := m["type"]; ok {
		if err := validateAntigravitySchemaTypes(raw, path+".type"); err != nil {
			return err
		}
	}
	for _, key := range []string{"title", "description", "format"} {
		if raw, ok := m[key]; ok {
			if _, isString := raw.(string); !isString || (key == "format" && !supportedAntigravitySchemaFormat(stringValue(raw))) {
				return errorsForAntigravitySchema(fmt.Sprintf("%s.%s is unsupported", path, key))
			}
		}
	}
	if raw, ok := m["properties"]; ok {
		properties := mapAny(raw)
		if properties == nil {
			return errorsForAntigravitySchema(fmt.Sprintf("%s.properties must be an object", path))
		}
		for name, child := range properties {
			if err := validateAntigravityResponseSchemaNode(child, path+".properties."+name, depth+1); err != nil {
				return err
			}
		}
	}
	if raw, ok := m["required"]; ok {
		required, err := antigravitySchemaStringSlice(raw, path+".required")
		if err != nil {
			return err
		}
		if properties := mapAny(m["properties"]); properties != nil {
			for _, name := range required {
				if _, exists := properties[name]; !exists {
					return errorsForAntigravitySchema(fmt.Sprintf("%s.required contains unknown property %q", path, name))
				}
			}
		}
	}
	if raw, ok := m["enum"]; ok {
		if err := validateAntigravitySchemaEnum(raw, path+".enum"); err != nil {
			return err
		}
	}
	for _, key := range []string{"minimum", "maximum"} {
		if raw, ok := m[key]; ok {
			if _, valid := antigravitySchemaNumber(raw); !valid {
				return errorsForAntigravitySchema(fmt.Sprintf("%s.%s must be a number", path, key))
			}
		}
	}
	if minimum, ok := antigravitySchemaNumber(m["minimum"]); ok {
		if maximum, ok := antigravitySchemaNumber(m["maximum"]); ok && minimum > maximum {
			return errorsForAntigravitySchema(fmt.Sprintf("%s minimum exceeds maximum", path))
		}
	}
	if raw, ok := m["items"]; ok {
		if err := validateAntigravityResponseSchemaNode(raw, path+".items", depth+1); err != nil {
			return err
		}
	}
	if raw, ok := m["prefixItems"]; ok {
		items, ok := raw.([]any)
		if !ok || len(items) == 0 {
			return errorsForAntigravitySchema(fmt.Sprintf("%s.prefixItems must be a non-empty array", path))
		}
		for index, item := range items {
			if err := validateAntigravityResponseSchemaNode(item, fmt.Sprintf("%s.prefixItems[%d]", path, index), depth+1); err != nil {
				return err
			}
		}
	}
	var minItems, maxItems *int
	for _, entry := range []struct {
		key  string
		into **int
	}{{"minItems", &minItems}, {"maxItems", &maxItems}} {
		if raw, ok := m[entry.key]; ok {
			value, err := antigravitySchemaInteger(raw, path+"."+entry.key)
			if err != nil {
				return err
			}
			*entry.into = &value
		}
	}
	if minItems != nil && maxItems != nil && *minItems > *maxItems {
		return errorsForAntigravitySchema(fmt.Sprintf("%s minItems exceeds maxItems", path))
	}
	if raw, ok := m["anyOf"]; ok {
		variants, ok := raw.([]any)
		if !ok || len(variants) == 0 {
			return errorsForAntigravitySchema(fmt.Sprintf("%s.anyOf must be a non-empty array", path))
		}
		for index, variant := range variants {
			if err := validateAntigravityResponseSchemaNode(variant, fmt.Sprintf("%s.anyOf[%d]", path, index), depth+1); err != nil {
				return err
			}
		}
	}
	if raw, ok := m["additionalProperties"]; ok {
		switch value := raw.(type) {
		case bool:
		case map[string]any:
			if err := validateAntigravityResponseSchemaNode(value, path+".additionalProperties", depth+1); err != nil {
				return err
			}
		default:
			return errorsForAntigravitySchema(fmt.Sprintf("%s.additionalProperties must be a boolean or schema", path))
		}
	}
	return nil
}

func validateAntigravitySchemaTypes(value any, path string) error {
	types, err := antigravitySchemaTypeSlice(value, path)
	if err != nil {
		return err
	}
	if len(types) == 0 {
		return errorsForAntigravitySchema(fmt.Sprintf("%s must not be empty", path))
	}
	seen := map[string]struct{}{}
	nonNull := 0
	for _, typ := range types {
		typ = strings.ToLower(strings.TrimSpace(typ))
		if _, exists := seen[typ]; exists {
			return errorsForAntigravitySchema(fmt.Sprintf("%s contains a duplicate type", path))
		}
		seen[typ] = struct{}{}
		switch typ {
		case "string", "number", "integer", "boolean", "object", "array":
			nonNull++
		case "null":
		default:
			return errorsForAntigravitySchema(fmt.Sprintf("%s contains unsupported type %q", path, typ))
		}
	}
	if len(types) > 1 && (len(types) != 2 || nonNull != 1) {
		return errorsForAntigravitySchema(fmt.Sprintf("%s may combine only one type with null", path))
	}
	return nil
}

func antigravitySchemaTypeSlice(value any, path string) ([]string, error) {
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) == "" {
			return nil, errorsForAntigravitySchema(fmt.Sprintf("%s must be a non-empty string or array", path))
		}
		return []string{text}, nil
	}
	return antigravitySchemaStringSlice(value, path)
}

func supportedAntigravitySchemaFormat(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "date-time", "date", "time":
		return true
	default:
		return false
	}
}

func validateAntigravitySchemaEnum(value any, path string) error {
	items, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			items = make([]any, len(strings))
			for index, item := range strings {
				items[index] = item
			}
		} else {
			return errorsForAntigravitySchema(fmt.Sprintf("%s must be an array", path))
		}
	}
	if len(items) == 0 {
		return errorsForAntigravitySchema(fmt.Sprintf("%s must not be empty", path))
	}
	for _, item := range items {
		switch item.(type) {
		case nil, string, bool, json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		default:
			return errorsForAntigravitySchema(fmt.Sprintf("%s contains a non-scalar value", path))
		}
	}
	return nil
}

func antigravitySchemaStringSlice(value any, path string) ([]string, error) {
	var raw []any
	switch value := value.(type) {
	case []any:
		raw = value
	case []string:
		result := make([]string, len(value))
		copy(result, value)
		return result, nil
	default:
		return nil, errorsForAntigravitySchema(fmt.Sprintf("%s must be an array of strings", path))
	}
	result := make([]string, len(raw))
	for index, item := range raw {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, errorsForAntigravitySchema(fmt.Sprintf("%s[%d] must be a non-empty string", path, index))
		}
		result[index] = text
	}
	return result, nil
}

func antigravitySchemaNumber(value any) (float64, bool) {
	switch value := value.(type) {
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	case float64:
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int8:
		return float64(value), true
	case int16:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint8:
		return float64(value), true
	case uint16:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	default:
		return 0, false
	}
}

func antigravitySchemaInteger(value any, path string) (int, error) {
	parsed, ok := antigravitySchemaNumber(value)
	if !ok || math.Trunc(parsed) != parsed || parsed < 0 || parsed > float64(int(^uint(0)>>1)) {
		return 0, errorsForAntigravitySchema(fmt.Sprintf("%s must be a non-negative integer", path))
	}
	return int(parsed), nil
}

func errorsForAntigravitySchema(message string) error {
	return fmt.Errorf("Antigravity structured schema %s", message)
}

func antigravitySchema(value any) (*cloudcode.ParameterSchema, error) {
	if value == nil {
		return nil, nil
	}
	m := mapAny(value)
	if m == nil {
		return nil, fmt.Errorf("Antigravity function parameters must be a schema object")
	}
	for key := range m {
		if _, ok := antigravityToolSchemaKeys[key]; !ok {
			return nil, fmt.Errorf("Antigravity function schema keyword %q is not supported", key)
		}
	}
	typeName := strings.ToUpper(strings.TrimSpace(stringValue(m["type"])))
	if typeName != "" {
		if _, ok := m["type"].(string); !ok {
			return nil, fmt.Errorf("Antigravity function schema type must be a string")
		}
		switch typeName {
		case "STRING", "NUMBER", "INTEGER", "BOOLEAN", "OBJECT", "ARRAY":
		default:
			return nil, fmt.Errorf("Antigravity function schema type %q is not supported", typeName)
		}
	}
	description := ""
	if raw, ok := m["description"]; ok {
		var isString bool
		description, isString = raw.(string)
		if !isString {
			return nil, fmt.Errorf("Antigravity function schema description must be a string")
		}
	}
	result := &cloudcode.ParameterSchema{Type: typeName, Description: description}
	if raw, ok := m["required"]; ok {
		required, err := antigravitySchemaStringSlice(raw, "function schema required")
		if err != nil {
			return nil, err
		}
		result.Required = required
	}
	if raw, ok := m["enum"]; ok {
		items, ok := raw.([]any)
		if !ok {
			if strings, isStringSlice := raw.([]string); isStringSlice {
				result.Enum = append([]string(nil), strings...)
			} else {
				return nil, fmt.Errorf("Antigravity function schema enum must be an array of strings")
			}
		} else {
			if len(items) == 0 {
				return nil, fmt.Errorf("Antigravity function schema enum must not be empty")
			}
			for index, item := range items {
				text, isString := item.(string)
				if !isString || strings.TrimSpace(text) == "" {
					return nil, fmt.Errorf("Antigravity function schema enum[%d] must be a non-empty string", index)
				}
				result.Enum = append(result.Enum, text)
			}
		}
	}
	if raw, ok := m["properties"]; ok {
		properties := mapAny(raw)
		if properties == nil {
			return nil, fmt.Errorf("Antigravity function schema properties must be an object")
		}
		result.Properties = map[string]*cloudcode.ParameterSchema{}
		for name, child := range properties {
			parsed, err := antigravitySchema(child)
			if err != nil {
				return nil, fmt.Errorf("Antigravity function schema property %q: %w", name, err)
			}
			result.Properties[name] = parsed
		}
	}
	if raw, ok := m["items"]; ok {
		items, err := antigravitySchema(raw)
		if err != nil {
			return nil, err
		}
		result.Items = items
	}
	return result, nil
}
