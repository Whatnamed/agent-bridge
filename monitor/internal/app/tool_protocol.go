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
const functionCallTransportV2Prefix = "agytc2_"

type thoughtSignatureToolCallID struct {
	CallID    string `json:"call_id"`
	Signature string `json:"signature"`
}

// functionCallTransportV2 is deliberately carried by the opaque call ID.
// Clients are allowed to drop provider-specific JSON extensions, but they
// reliably echo tool-call IDs. The step and positional fields are therefore
// part of the durable request history rather than an in-memory server map.
type functionCallTransportV2 struct {
	Version   int    `json:"version"`
	CallID    string `json:"call_id"`
	Signature string `json:"signature,omitempty"`
	StepID    string `json:"step_id"`
	PartIndex int    `json:"part_index"`
	GroupSize int    `json:"group_size"`
}

type decodedFunctionCallTransport struct {
	CallID    string
	Signature string
	StepID    string
	PartIndex int
	GroupSize int
	Version   int
	Encoded   bool
}

func encodeFunctionCallTransportID(callID, signature, stepID string, partIndex, groupSize int) string {
	callID = strings.TrimSpace(callID)
	signature = strings.TrimSpace(signature)
	stepID = strings.TrimSpace(stepID)
	if callID == "" {
		return ""
	}
	// Preserve an already valid envelope. This makes Responses -> Chat ->
	// Responses conversions idempotent and prevents recursive wrapping.
	if decoded, err := decodeFunctionCallTransportID(callID); err == nil && decoded.Encoded {
		return callID
	}
	if groupSize <= 1 && stepID == "" {
		if signature == "" {
			return callID
		}
		payload, err := json.Marshal(thoughtSignatureToolCallID{CallID: callID, Signature: signature})
		if err != nil {
			return callID
		}
		return thoughtSignatureToolCallIDPrefix + base64.RawURLEncoding.EncodeToString(payload)
	}
	if stepID == "" || groupSize < 1 || partIndex < 0 || partIndex >= groupSize {
		// The caller cannot safely invent grouping metadata. Returning the raw
		// ID makes the malformed state visible to the history validator instead
		// of manufacturing an envelope that could be misinterpreted.
		return callID
	}
	payload, err := json.Marshal(functionCallTransportV2{
		Version: 2, CallID: callID, Signature: signature, StepID: stepID,
		PartIndex: partIndex, GroupSize: groupSize,
	})
	if err != nil {
		return callID
	}
	return functionCallTransportV2Prefix + base64.RawURLEncoding.EncodeToString(payload)
}

func encodeThoughtSignatureToolCallID(callID, signature string) string {
	callID = strings.TrimSpace(callID)
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return callID
	}
	return encodeFunctionCallTransportID(callID, signature, "", 0, 1)
}

func decodeFunctionCallTransportID(value string) (decodedFunctionCallTransport, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return decodedFunctionCallTransport{}, fmt.Errorf("tool call id is required")
	}
	if strings.HasPrefix(value, functionCallTransportV2Prefix) {
		encoded := strings.TrimPrefix(value, functionCallTransportV2Prefix)
		payload, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return decodedFunctionCallTransport{}, fmt.Errorf("invalid Antigravity tool call transport id")
		}
		var transport functionCallTransportV2
		decoder := json.NewDecoder(strings.NewReader(string(payload)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&transport); err != nil || transport.Version != 2 || strings.TrimSpace(transport.CallID) == "" || strings.TrimSpace(transport.StepID) == "" || transport.GroupSize < 1 || transport.PartIndex < 0 || transport.PartIndex >= transport.GroupSize {
			return decodedFunctionCallTransport{}, fmt.Errorf("invalid Antigravity tool call transport id")
		}
		return decodedFunctionCallTransport{
			CallID: strings.TrimSpace(transport.CallID), Signature: strings.TrimSpace(transport.Signature),
			StepID: strings.TrimSpace(transport.StepID), PartIndex: transport.PartIndex,
			GroupSize: transport.GroupSize, Version: 2, Encoded: true,
		}, nil
	}
	if strings.HasPrefix(value, thoughtSignatureToolCallIDPrefix) {
		encoded := strings.TrimPrefix(value, thoughtSignatureToolCallIDPrefix)
		payload, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return decodedFunctionCallTransport{}, fmt.Errorf("invalid Antigravity tool call transport id")
		}
		var transport thoughtSignatureToolCallID
		decoder := json.NewDecoder(strings.NewReader(string(payload)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&transport); err != nil || strings.TrimSpace(transport.CallID) == "" || strings.TrimSpace(transport.Signature) == "" {
			return decodedFunctionCallTransport{}, fmt.Errorf("invalid Antigravity tool call transport id")
		}
		return decodedFunctionCallTransport{
			CallID: strings.TrimSpace(transport.CallID), Signature: strings.TrimSpace(transport.Signature),
			GroupSize: 1, Version: 1, Encoded: true,
		}, nil
	}
	if strings.HasPrefix(value, "agytc") {
		return decodedFunctionCallTransport{}, fmt.Errorf("invalid Antigravity tool call transport id")
	}
	return decodedFunctionCallTransport{CallID: value}, nil
}

func decodeThoughtSignatureToolCallID(value string) (string, string, error) {
	decoded, err := decodeFunctionCallTransportID(value)
	if err != nil {
		return "", "", err
	}
	return decoded.CallID, decoded.Signature, nil
}

func functionCallTransportID(item map[string]any) string {
	if item == nil {
		return ""
	}
	callID := strings.TrimSpace(stringValue(valueOr(item["call_id"], item["id"])))
	if decoded, err := decodeFunctionCallTransportID(callID); err == nil && decoded.Encoded {
		return callID
	}
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
		transportID := strings.TrimSpace(stringValue(valueOr(item["call_id"], item["id"])))
		name := strings.TrimSpace(stringValue(item["name"]))
		if transportID == "" || name == "" {
			return nil, fmt.Errorf("function_call must include call_id and name")
		}
		decoded, err := decodeFunctionCallTransportID(transportID)
		if err != nil {
			return nil, err
		}
		for _, key := range []string{transportID, decoded.CallID} {
			if previous, ok := names[key]; ok && previous != name {
				return nil, fmt.Errorf("function_call reused call_id with a different name")
			}
			names[key] = name
		}
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
