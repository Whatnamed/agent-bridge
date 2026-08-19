package app

import (
	"strings"
	"testing"
)

func TestAntigravityParallelFunctionHistoryRebuildsOneOrderedStep(t *testing.T) {
	first := encodeFunctionCallTransportID("call-1", "sig-1", "step-1", 0, 2)
	second := encodeFunctionCallTransportID("call-2", "", "step-1", 1, 2)
	request, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{
			map[string]any{"role": "user", "content": "run both"},
			map[string]any{"type": "function_call", "call_id": first, "name": "lookup", "arguments": `{"a":1}`},
			map[string]any{"type": "function_call", "call_id": second, "name": "search", "arguments": `{"b":2}`},
			map[string]any{"type": "function_call_output", "call_id": first, "output": `{"value":"one"}`},
			map[string]any{"type": "function_call_output", "call_id": second, "output": `{"value":"two"}`},
		},
	}, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Request.Contents) != 3 || len(request.Request.Contents[1].Parts) != 2 || len(request.Request.Contents[2].Parts) != 2 {
		t.Fatalf("contents = %#v", request.Request.Contents)
	}
	model := request.Request.Contents[1]
	if model.Role != "model" || model.Parts[0].FunctionCall == nil || model.Parts[1].FunctionCall == nil {
		t.Fatalf("model content = %#v", model)
	}
	if model.Parts[0].FunctionCall.Name != "lookup" || model.Parts[1].FunctionCall.Name != "search" || model.Parts[0].ThoughtSignature != "sig-1" || model.Parts[1].ThoughtSignature != "" {
		t.Fatalf("parallel call parts = %#v", model.Parts)
	}
	responses := request.Request.Contents[2]
	if responses.Role != "user" || responses.Parts[0].FunctionResponse == nil || responses.Parts[1].FunctionResponse == nil || responses.Parts[0].FunctionResponse.ID != "call-1" || responses.Parts[1].FunctionResponse.ID != "call-2" {
		t.Fatalf("response content = %#v", responses)
	}
}

func TestAntigravitySameNameParallelHistoryDoesNotCopySignature(t *testing.T) {
	first := encodeFunctionCallTransportID("bash-1", "sig-bash", "step-bash", 0, 3)
	second := encodeFunctionCallTransportID("bash-2", "", "step-bash", 1, 3)
	third := encodeFunctionCallTransportID("bash-3", "", "step-bash", 2, 3)
	request, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{
			map[string]any{"type": "function_call", "call_id": first, "name": "Bash", "arguments": `{}`},
			map[string]any{"type": "function_call", "call_id": second, "name": "Bash", "arguments": `{}`},
			map[string]any{"type": "function_call", "call_id": third, "name": "Bash", "arguments": `{}`},
			map[string]any{"type": "function_call_output", "call_id": first, "output": "one"},
			map[string]any{"type": "function_call_output", "call_id": second, "output": "two"},
			map[string]any{"type": "function_call_output", "call_id": third, "output": "three"},
		},
	}, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	parts := request.Request.Contents[0].Parts
	if len(parts) != 3 || parts[0].ThoughtSignature != "sig-bash" || parts[1].ThoughtSignature != "" || parts[2].ThoughtSignature != "" {
		t.Fatalf("same-name signatures = %#v", parts)
	}
}

func TestAntigravitySequentialFunctionHistoryKeepsSeparateSteps(t *testing.T) {
	first := encodeFunctionCallTransportID("call-1", "sig-1", "step-1", 0, 1)
	second := encodeFunctionCallTransportID("call-2", "sig-2", "step-2", 0, 1)
	request, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{
			map[string]any{"role": "user", "content": "continue"},
			map[string]any{"type": "function_call", "call_id": first, "name": "one", "arguments": `{}`},
			map[string]any{"type": "function_call_output", "call_id": first, "output": "one"},
			map[string]any{"type": "function_call", "call_id": second, "name": "two", "arguments": `{}`},
			map[string]any{"type": "function_call_output", "call_id": second, "output": "two"},
		},
	}, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Request.Contents) != 5 || request.Request.Contents[1].Role != "model" || request.Request.Contents[2].Role != "user" || request.Request.Contents[3].Role != "model" || request.Request.Contents[4].Role != "user" {
		t.Fatalf("sequential contents = %#v", request.Request.Contents)
	}
	if request.Request.Contents[1].Parts[0].ThoughtSignature != "sig-1" || request.Request.Contents[3].Parts[0].ThoughtSignature != "sig-2" {
		t.Fatalf("sequential signatures = %#v", request.Request.Contents)
	}
}

func TestAntigravityOldOpaqueIDHistoryRemainsCompatible(t *testing.T) {
	first := encodeThoughtSignatureToolCallID("old-1", "old-sig")
	second := encodeThoughtSignatureToolCallID("old-2", "old-sig-2")
	request, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{
			map[string]any{"type": "function_call", "call_id": first, "name": "one", "arguments": `{}`},
			map[string]any{"type": "function_call", "call_id": second, "name": "two", "arguments": `{}`},
			map[string]any{"type": "function_call_output", "call_id": first, "output": "one"},
			map[string]any{"type": "function_call_output", "call_id": second, "output": "two"},
		},
	}, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Request.Contents) != 2 || len(request.Request.Contents[0].Parts) != 2 || len(request.Request.Contents[1].Parts) != 2 || request.Request.Contents[0].Parts[0].ThoughtSignature != "old-sig" {
		t.Fatalf("old opaque history = %#v", request.Request.Contents)
	}
}

func TestFunctionCallTransportV2RejectsMalformedOrMismatchedEnvelope(t *testing.T) {
	if _, _, err := decodeThoughtSignatureToolCallID("agytc2_not-base64"); err == nil {
		t.Fatal("malformed v2 transport id was accepted")
	}
	first := encodeFunctionCallTransportID("call-1", "sig-1", "step-1", 0, 2)
	wrong := encodeFunctionCallTransportID("call-2", "", "step-2", 1, 2)
	_, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{
			map[string]any{"type": "function_call", "call_id": first, "name": "one", "arguments": `{}`},
			map[string]any{"type": "function_call", "call_id": wrong, "name": "two", "arguments": `{}`},
		},
	}, stableAntigravityModel, "projects/test-project")
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("mismatched group was accepted: %v", err)
	}
}

func TestAntigravityFunctionCallWithoutRecoverableSignatureFailsClosed(t *testing.T) {
	_, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{map[string]any{"type": "function_call", "call_id": "raw-call", "name": "lookup", "arguments": `{}`}},
	}, stableAntigravityModel, "projects/test-project")
	if err == nil || !strings.Contains(err.Error(), "thought signature could not be recovered") {
		t.Fatalf("signature-less function call was accepted: %v", err)
	}
}

func TestResponsesChatResponsesPreservesV2TransportIDAndExtension(t *testing.T) {
	transportID := encodeFunctionCallTransportID("call-1", "sig-1", "step-1", 0, 2)
	response := map[string]any{
		"id": "resp_tool", "model": stableAntigravityModel,
		"output": []any{
			map[string]any{"type": "function_call", "call_id": transportID, "id": transportID, "name": "one", "arguments": `{}`, "thought_signature": "sig-1"},
			map[string]any{"type": "function_call", "call_id": encodeFunctionCallTransportID("call-2", "", "step-1", 1, 2), "id": encodeFunctionCallTransportID("call-2", "", "step-1", 1, 2), "name": "two", "arguments": `{}`},
		},
	}
	completion := responseToChat(response, "fallback", false, 1)
	message := mapAny(mapAny(sliceAny(completion["choices"])[0])["message"])
	toolCalls := sliceAny(message["tool_calls"])
	toolCall := mapAny(toolCalls[0])
	secondToolCall := mapAny(toolCalls[1])
	if got := stringValue(toolCall["id"]); got != transportID {
		t.Fatalf("transport id changed: got %q want %q", got, transportID)
	}
	if extra := mapAny(toolCall["extra_content"]); stringValue(mapAny(extra["google"])["thought_signature"]) != "sig-1" {
		t.Fatalf("Chat extension missing: %#v", toolCall)
	}
	if mapAny(toolCall["function"])["thought_signature"] != nil {
		t.Fatalf("modern Chat function contained nonstandard signature: %#v", toolCall)
	}
	converted, err := chatToResponse(map[string]any{
		"model": stableAntigravityModel,
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{toolCall, secondToolCall}},
			map[string]any{"role": "tool", "tool_call_id": transportID, "content": "ok"},
			map[string]any{"role": "tool", "tool_call_id": stringValue(secondToolCall["id"]), "content": "ok2"},
		},
	}, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if got := stringValue(mapAny(sliceAny(converted["input"])[0])["call_id"]); got != transportID {
		t.Fatalf("converted call id changed: got %q want %q", got, transportID)
	}
	if got := stringValue(mapAny(sliceAny(converted["input"])[2])["call_id"]); got != transportID {
		t.Fatalf("converted output id changed: got %q want %q", got, transportID)
	}
	request, err := buildAntigravityRequest(converted, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Request.Contents) != 2 || request.Request.Contents[0].Parts[0].ThoughtSignature != "sig-1" {
		t.Fatalf("round-tripped request = %#v", request.Request.Contents)
	}
}
