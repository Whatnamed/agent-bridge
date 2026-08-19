package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Chat tool_call IDs are the only stable field that a normal OpenAI client
// reliably echoes back on the next turn. Antigravity also requires the
// thought signature from the assistant function call, so preserve it in a
// reversible, URL-safe transport ID instead of dropping it at the Chat
// compatibility boundary.
const thoughtSignatureToolCallIDPrefix = "agytc_"

type thoughtSignatureToolCallID struct {
	CallID    string `json:"call_id"`
	Signature string `json:"signature"`
}

func encodeThoughtSignatureToolCallID(callID, signature string) string {
	callID = strings.TrimSpace(callID)
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return callID
	}
	payload, err := json.Marshal(thoughtSignatureToolCallID{CallID: callID, Signature: signature})
	if err != nil {
		return callID
	}
	return thoughtSignatureToolCallIDPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

func decodeThoughtSignatureToolCallID(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", fmt.Errorf("tool call id is required")
	}
	if !strings.HasPrefix(value, thoughtSignatureToolCallIDPrefix) {
		return value, "", nil
	}
	encoded := strings.TrimPrefix(value, thoughtSignatureToolCallIDPrefix)
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", fmt.Errorf("invalid Antigravity tool call transport id")
	}
	var transport thoughtSignatureToolCallID
	if err := json.Unmarshal(payload, &transport); err != nil || strings.TrimSpace(transport.CallID) == "" || strings.TrimSpace(transport.Signature) == "" {
		return "", "", fmt.Errorf("invalid Antigravity tool call transport id")
	}
	return strings.TrimSpace(transport.CallID), strings.TrimSpace(transport.Signature), nil
}

func functionCallTransportID(item map[string]any) string {
	if item == nil {
		return ""
	}
	callID := stringValue(valueOr(item["call_id"], item["id"]))
	signature := firstMapString(item, "thought_signature", "thoughtSignature")
	return encodeThoughtSignatureToolCallID(callID, signature)
}

func responseFunctionCallNames(input []any) (map[string]string, error) {
	names := make(map[string]string)
	for _, raw := range input {
		item := mapAny(raw)
		if item == nil || stringValue(item["type"]) != "function_call" {
			continue
		}
		callID := strings.TrimSpace(stringValue(valueOr(item["call_id"], item["id"])))
		name := strings.TrimSpace(stringValue(item["name"]))
		if callID == "" || name == "" {
			return nil, fmt.Errorf("function_call must include call_id and name")
		}
		if previous, ok := names[callID]; ok && previous != name {
			return nil, fmt.Errorf("function_call reused call_id with a different name")
		}
		names[callID] = name
	}
	return names, nil
}

func resolveResponseFunctionOutputName(item map[string]any, names map[string]string) (string, error) {
	if item == nil {
		return "", fmt.Errorf("function_call_output must be an object")
	}
	callID := strings.TrimSpace(stringValue(item["call_id"]))
	if callID == "" {
		return "", fmt.Errorf("function_call_output must include call_id")
	}
	explicit := strings.TrimSpace(stringValue(item["name"]))
	mapped := strings.TrimSpace(names[callID])
	if explicit != "" && mapped != "" && explicit != mapped {
		return "", fmt.Errorf("function_call_output name does not match its function_call")
	}
	if explicit == "" {
		explicit = mapped
	}
	if explicit == "" {
		return "", fmt.Errorf("function_call_output function name could not be resolved")
	}
	return explicit, nil
}
