package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	data, _ := json.Marshal(value)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
func cloneSlice(value []any) []any {
	data, _ := json.Marshal(value)
	var out []any
	_ = json.Unmarshal(data, &out)
	return out
}
func mapAny(value any) map[string]any { result, _ := value.(map[string]any); return result }
func sliceAny(value any) []any {
	if result, ok := value.([]any); ok {
		return result
	}
	return []any{}
}
func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if v, ok := value.(string); ok {
		return v
	}
	return fmt.Sprint(value)
}
func boolValue(value any) bool { v, _ := value.(bool); return v }
func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int8:
		return int(v)
	case int16:
		return int(v)
	case int32:
		return int(v)
	case int64:
		return int(v)
	case uint:
		return int(v)
	case uint8:
		return int(v)
	case uint16:
		return int(v)
	case uint32:
		return int(v)
	case uint64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
		f, _ := v.Float64()
		return int(f)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}
func valueOr(value, fallback any) any {
	if value == nil || value == "" {
		return fallback
	}
	return value
}
func setDefault(m map[string]any, key string, value any) {
	if m[key] == nil {
		m[key] = value
	}
}
func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }
func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

func normalizeResponseInput(value any) []any {
	switch v := value.(type) {
	case nil:
		return []any{}
	case string:
		return []any{map[string]any{"role": "user", "content": v}}
	case []any:
		result := make([]any, 0, len(v))
		for _, item := range v {
			if normalized := normalizeInputItem(item); normalized != nil {
				result = append(result, normalized)
			}
		}
		return result
	case map[string]any:
		if item := normalizeInputItem(v); item != nil {
			return []any{item}
		}
		return []any{}
	default:
		return []any{map[string]any{"role": "user", "content": fmt.Sprint(value)}}
	}
}

func normalizeInputItem(value any) any {
	item := mapAny(value)
	if item == nil {
		return value
	}
	switch item["type"] {
	case "reasoning":
		return map[string]any{"type": "reasoning", "encrypted_content": item["encrypted_content"], "summary": sliceAny(item["summary"])}
	case "message":
		if item["role"] == "assistant" {
			content := item["content"]
			switch value := content.(type) {
			case map[string]any:
				content = cloneMap(value)
			case []any:
				content = cloneSlice(value)
			}
			result := map[string]any{"role": "assistant", "content": content}
			if item["phase"] != nil {
				result["phase"] = item["phase"]
			}
			return result
		}
	case "function_call":
		result := map[string]any{"type": "function_call", "call_id": valueOr(item["call_id"], item["id"]), "name": item["name"], "arguments": valueOr(item["arguments"], "{}")}
		if signature := stringValue(valueOr(item["thought_signature"], item["thoughtSignature"])); signature != "" {
			result["thought_signature"] = signature
		}
		return result
	case "function_call_output":
		result := map[string]any{"type": "function_call_output", "output": item["output"]}
		if item["call_id"] != nil {
			result["call_id"] = item["call_id"]
		}
		if name := stringValue(item["name"]); name != "" {
			result["name"] = name
		}
		return result
	}
	return cloneMap(item)
}

func prepareResponse(body map[string]any, model string) map[string]any {
	result := cloneMap(body)
	if stringValue(result["model"]) == "" {
		result["model"] = model
	}
	result["input"] = normalizeResponseInput(result["input"])
	setDefault(result, "instructions", "You are a helpful assistant.")
	setDefault(result, "store", false)
	return result
}

func chatToResponse(body map[string]any, model string) (map[string]any, error) {
	result := map[string]any{"model": valueOr(body["model"], model), "input": []any{}, "store": false}
	var instructions []string
	var input []any
	functionNames := make(map[string]string)
	for _, raw := range sliceAny(body["messages"]) {
		msg := mapAny(raw)
		if msg == nil {
			continue
		}
		role := stringValue(msg["role"])
		switch role {
		case "system", "developer":
			if t := contentText(msg["content"]); t != "" {
				instructions = append(instructions, t)
			}
		case "tool", "function":
			rawCallID := stringValue(msg["tool_call_id"])
			if role == "function" && rawCallID == "" {
				rawCallID = stringValue(msg["name"])
			}
			decoded, err := decodeFunctionCallTransportID(rawCallID)
			if err != nil {
				return nil, err
			}
			callID := decoded.CallID
			name := strings.TrimSpace(stringValue(msg["name"]))
			if mapped := firstNonEmpty(functionNames[rawCallID], functionNames[callID]); mapped != "" {
				if name != "" && name != mapped {
					return nil, fmt.Errorf("tool output name does not match its assistant tool call")
				}
				name = mapped
			}
			if name == "" {
				return nil, fmt.Errorf("tool output function name could not be resolved")
			}
			output := map[string]any{"type": "function_call_output", "call_id": rawCallID, "name": name, "output": contentText(msg["content"])}
			input = append(input, output)
		case "assistant":
			if fc := mapAny(msg["function_call"]); fc != nil {
				name := strings.TrimSpace(stringValue(fc["name"]))
				if name == "" {
					return nil, fmt.Errorf("assistant function_call must include name")
				}
				rawCallID := stringValue(valueOr(fc["id"], fc["name"]))
				decoded, err := decodeFunctionCallTransportID(rawCallID)
				if err != nil {
					return nil, err
				}
				callID, signature := decoded.CallID, decoded.Signature
				if explicit := chatToolCallThoughtSignature(nil, fc); explicit != "" {
					if signature != "" && signature != explicit {
						return nil, fmt.Errorf("assistant function_call thought signature does not match its transport id")
					}
					signature = explicit
				}
				functionNames[callID] = name
				functionNames[rawCallID] = name
				entry := map[string]any{"type": "function_call", "call_id": rawCallID, "name": name, "arguments": valueOr(fc["arguments"], "{}")}
				if signature != "" {
					entry["thought_signature"] = signature
				}
				input = append(input, entry)
			}
			for _, tcRaw := range sliceAny(msg["tool_calls"]) {
				tc := mapAny(tcRaw)
				fn := mapAny(tc["function"])
				if fn == nil {
					return nil, fmt.Errorf("assistant tool_call must include function details")
				}
				name := strings.TrimSpace(stringValue(fn["name"]))
				if name == "" {
					return nil, fmt.Errorf("assistant tool_call must include function name")
				}
				rawCallID := stringValue(tc["id"])
				decoded, err := decodeFunctionCallTransportID(rawCallID)
				if err != nil {
					return nil, err
				}
				callID, signature := decoded.CallID, decoded.Signature
				if explicit := chatToolCallThoughtSignature(tc, fn); explicit != "" {
					if signature != "" && signature != explicit {
						return nil, fmt.Errorf("assistant tool_call thought signature does not match its transport id")
					}
					signature = explicit
				}
				functionNames[callID] = name
				functionNames[rawCallID] = name
				entry := map[string]any{"type": "function_call", "call_id": rawCallID, "name": name, "arguments": valueOr(fn["arguments"], "{}")}
				if signature != "" {
					entry["thought_signature"] = signature
				}
				input = append(input, entry)
			}
			if text := contentText(msg["content"]); text != "" || len(sliceAny(msg["tool_calls"])) == 0 {
				input = append(input, map[string]any{"role": "assistant", "content": text})
			}
		case "user":
			input = append(input, map[string]any{"role": "user", "content": chatContent(msg["content"])})
		}
	}
	result["instructions"] = strings.Join(instructions, "\n\n")
	if result["instructions"] == "" {
		result["instructions"] = "You are a helpful assistant."
	}
	result["input"] = input
	for _, key := range []string{"parallel_tool_calls", "service_tier", "temperature", "top_p", "user"} {
		if body[key] != nil {
			result[key] = body[key]
		}
	}
	if body["max_completion_tokens"] != nil {
		result["max_output_tokens"] = body["max_completion_tokens"]
	} else if body["max_tokens"] != nil {
		result["max_output_tokens"] = body["max_tokens"]
	}
	if body["reasoning"] != nil {
		result["reasoning"] = body["reasoning"]
	} else if body["reasoning_effort"] != nil {
		result["reasoning"] = map[string]any{"effort": body["reasoning_effort"]}
	}
	if body["verbosity"] != nil {
		result["text"] = map[string]any{"verbosity": body["verbosity"]}
	}
	var tools []any
	for _, raw := range sliceAny(body["tools"]) {
		tool := mapAny(raw)
		if tool == nil {
			continue
		}
		if tool["type"] == "function" {
			fn := mapAny(tool["function"])
			if fn != nil {
				copied := cloneMap(fn)
				copied["type"] = "function"
				tools = append(tools, copied)
			}
		} else {
			tools = append(tools, cloneMap(tool))
		}
	}
	for _, raw := range sliceAny(body["functions"]) {
		fn := mapAny(raw)
		if fn != nil {
			copied := cloneMap(fn)
			copied["type"] = "function"
			tools = append(tools, copied)
		}
	}
	if len(tools) > 0 {
		result["tools"] = tools
	}
	if choice := body["tool_choice"]; choice != nil {
		result["tool_choice"] = chatToolChoice(choice)
	} else if choice = body["function_call"]; choice != nil {
		result["tool_choice"] = chatFunctionChoice(choice)
	}
	if format := mapAny(body["response_format"]); format != nil {
		switch format["type"] {
		case "json_object":
			result["text"] = mergeMap(mapAny(result["text"]), map[string]any{"format": map[string]any{"type": "json_object"}})
		case "json_schema":
			schema := mapAny(format["json_schema"])
			f := map[string]any{"type": "json_schema"}
			for _, k := range []string{"name", "schema", "strict", "description"} {
				if schema[k] != nil {
					f[k] = schema[k]
				}
			}
			result["text"] = mergeMap(mapAny(result["text"]), map[string]any{"format": f})
		default:
			// Preserve an unknown response format so a provider-specific
			// validator can reject it instead of silently changing the
			// requested output semantics.
			result["text"] = mergeMap(mapAny(result["text"]), map[string]any{"format": cloneMap(format)})
		}
	} else if body["response_format"] != nil {
		result["text"] = mergeMap(mapAny(result["text"]), map[string]any{"format": map[string]any{"type": "__invalid_response_format"}})
	}
	return result, nil
}

func chatToolChoice(value any) any {
	m := mapAny(value)
	if m == nil {
		return value
	}
	if m["type"] == "function" {
		fn := mapAny(m["function"])
		if fn == nil {
			return value
		}
		return map[string]any{"type": "function", "name": fn["name"]}
	}
	return value
}

func chatToolCallThoughtSignature(toolCall, function map[string]any) string {
	for _, source := range []map[string]any{toolCall, function} {
		if source == nil {
			continue
		}
		if signature := firstMapString(source, "thought_signature", "thoughtSignature"); signature != "" {
			return signature
		}
		if extra := mapAny(source["extra_content"]); extra != nil {
			if google := mapAny(extra["google"]); google != nil {
				if signature := firstMapString(google, "thought_signature", "thoughtSignature"); signature != "" {
					return signature
				}
			}
		}
	}
	return ""
}
func chatFunctionChoice(value any) any {
	m := mapAny(value)
	if m == nil {
		return value
	}
	return map[string]any{"type": "function", "name": m["name"]}
}
func mergeMap(a, b map[string]any) map[string]any {
	if a == nil {
		a = map[string]any{}
	}
	for k, v := range b {
		a[k] = v
	}
	return a
}

func chatContent(value any) any {
	if s, ok := value.(string); ok {
		return s
	}
	var parts []any
	for _, raw := range sliceAny(value) {
		p := mapAny(raw)
		switch p["type"] {
		case "text", "input_text":
			parts = append(parts, map[string]any{"type": "input_text", "text": stringValue(p["text"])})
		case "image_url":
			u := p["image_url"]
			if m := mapAny(u); m != nil {
				u = m["url"]
			}
			parts = append(parts, map[string]any{"type": "input_image", "image_url": u})
		case "input_image":
			parts = append(parts, cloneMap(p))
		case "output_text":
			parts = append(parts, map[string]any{"type": "input_text", "text": stringValue(p["text"])})
		default:
			// Preserve the marker so an Antigravity request can reject it
			// explicitly. Do not silently turn an unknown semantic part into a
			// successful text-only request.
			if p == nil {
				parts = append(parts, map[string]any{"type": "__invalid_content_part"})
			} else {
				parts = append(parts, cloneMap(p))
			}
		}
	}
	return parts
}
func contentText(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	var b strings.Builder
	for _, raw := range sliceAny(value) {
		p := mapAny(raw)
		switch p["type"] {
		case "text", "input_text", "output_text":
			b.WriteString(stringValue(p["text"]))
		}
	}
	return b.String()
}
func messageText(item map[string]any) string {
	var b strings.Builder
	for _, raw := range sliceAny(item["content"]) {
		p := mapAny(raw)
		if p["type"] == "output_text" {
			b.WriteString(stringValue(p["text"]))
		}
	}
	return b.String()
}
func outputMessage(text string) map[string]any {
	return map[string]any{"id": newID("msg"), "type": "message", "role": "assistant", "status": "completed", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func ensureResponse(response, request map[string]any) map[string]any {
	result := cloneMap(response)
	setDefault(result, "id", newID("resp"))
	setDefault(result, "object", "response")
	setDefault(result, "created_at", nowFloat())
	setDefault(result, "status", "completed")
	setDefault(result, "model", valueOr(request["model"], defaultModel))
	setDefault(result, "output", []any{})
	setDefault(result, "parallel_tool_calls", true)
	setDefault(result, "tool_choice", valueOr(request["tool_choice"], "auto"))
	setDefault(result, "tools", sliceAny(request["tools"]))
	if _, ok := result["previous_response_id"]; !ok {
		result["previous_response_id"] = request["previous_response_id"]
	}
	if _, ok := result["usage"]; !ok {
		result["usage"] = nil
	}
	return result
}
func responseText(response map[string]any) string {
	if s, ok := response["output_text"].(string); ok {
		return s
	}
	var b strings.Builder
	for _, raw := range sliceAny(response["output"]) {
		item := mapAny(raw)
		if item["type"] == "message" {
			b.WriteString(messageText(item))
		}
	}
	return b.String()
}

func responseToChat(response map[string]any, fallback string, legacy bool, n int) map[string]any {
	usage := mapAny(response["usage"])
	if usage == nil {
		usage = map[string]any{}
	}
	prompt := intValue(usage["input_tokens"])
	completion := intValue(usage["output_tokens"])
	total := intValue(usage["total_tokens"])
	if total == 0 {
		total = prompt + completion
	}
	var toolCalls []any
	var legacyCall map[string]any
	for _, raw := range sliceAny(response["output"]) {
		item := mapAny(raw)
		if item["type"] != "function_call" {
			continue
		}
		fn := map[string]any{"name": stringValue(item["name"]), "arguments": stringValue(item["arguments"])}
		if legacy {
			if signature := firstMapString(item, "thought_signature", "thoughtSignature"); signature != "" {
				fn["thought_signature"] = signature
			}
			if legacyCall == nil {
				legacyCall = fn
			}
		} else {
			toolCall := map[string]any{"id": functionCallTransportID(item), "type": "function", "function": fn}
			if signature := firstMapString(item, "thought_signature", "thoughtSignature"); signature != "" {
				toolCall["extra_content"] = map[string]any{"google": map[string]any{"thought_signature": signature}}
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}
	message := map[string]any{"role": "assistant", "content": responseText(response)}
	if legacyCall != nil {
		message["function_call"] = legacyCall
	} else if len(toolCalls) > 0 {
		message["content"] = nil
		message["tool_calls"] = toolCalls
	}
	finish := "stop"
	if len(toolCalls) > 0 || legacyCall != nil {
		finish = "tool_calls"
	}
	if response["status"] == "incomplete" {
		finish = "length"
	}
	if n < 1 {
		n = 1
	}
	choices := make([]any, n)
	for i := 0; i < n; i++ {
		choices[i] = map[string]any{"index": i, "message": cloneMap(message), "finish_reason": finish, "logprobs": nil}
	}
	id := stringValue(response["id"])
	if strings.HasPrefix(id, "resp_") {
		id = strings.Replace(id, "resp_", "chatcmpl_", 1)
	} else {
		id = "chatcmpl_" + id
	}
	created := intValue(response["created_at"])
	if created == 0 {
		created = int(time.Now().Unix())
	}
	chatUsage := map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": total}
	if details := mapAny(usage["input_tokens_details"]); intValue(details["cached_tokens"]) != 0 {
		chatUsage["prompt_tokens_details"] = map[string]any{"cached_tokens": intValue(details["cached_tokens"])}
	}
	if details := mapAny(usage["output_tokens_details"]); intValue(details["reasoning_tokens"]) != 0 {
		chatUsage["completion_tokens_details"] = map[string]any{"reasoning_tokens": intValue(details["reasoning_tokens"])}
	}
	return map[string]any{"id": id, "object": "chat.completion", "created": created, "model": stringValue(valueOr(response["model"], fallback)), "choices": choices, "usage": chatUsage}
}

func responseContext(response map[string]any) []any {
	var result []any
	for _, raw := range sliceAny(response["output"]) {
		item := mapAny(raw)
		switch item["type"] {
		case "function_call":
			entry := map[string]any{"type": "function_call", "call_id": valueOr(item["call_id"], item["id"]), "name": item["name"], "arguments": valueOr(item["arguments"], "{}")}
			if signature := stringValue(valueOr(item["thought_signature"], item["thoughtSignature"])); signature != "" {
				entry["thought_signature"] = signature
			}
			result = append(result, entry)
		case "message":
			if text := messageText(item); text != "" {
				result = append(result, map[string]any{"role": "assistant", "content": text})
			}
		}
	}
	if len(result) == 0 {
		if text := responseText(response); text != "" {
			result = append(result, map[string]any{"role": "assistant", "content": text})
		}
	}
	return result
}
