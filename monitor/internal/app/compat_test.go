package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatToResponseTranslatesMessagesToolsAndStructuredOutput(t *testing.T) {
	body := map[string]any{
		"model": "gpt-test",
		"messages": []any{
			map[string]any{"role": "system", "content": "Be terse."},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "weather"}}},
		},
		"tools":           []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}},
		"tool_choice":     map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "answer", "strict": true, "schema": map[string]any{"type": "object"}}},
	}
	got, err := chatToResponse(body, defaultModel)
	if err != nil {
		t.Fatal(err)
	}
	if got["instructions"] != "Be terse." {
		t.Fatalf("instructions = %#v", got["instructions"])
	}
	input := sliceAny(got["input"])
	if len(input) != 1 || mapAny(input[0])["role"] != "user" {
		t.Fatalf("input = %#v", input)
	}
	tools := sliceAny(got["tools"])
	if len(tools) != 1 || mapAny(tools[0])["name"] != "lookup" {
		t.Fatalf("tools = %#v", tools)
	}
	choice := mapAny(got["tool_choice"])
	if choice["name"] != "lookup" {
		t.Fatalf("tool choice = %#v", choice)
	}
	format := mapAny(mapAny(got["text"])["format"])
	if format["type"] != "json_schema" || format["name"] != "answer" {
		t.Fatalf("format = %#v", format)
	}
}

func TestChatToolContinuationPreservesFunctionNameAndThoughtSignature(t *testing.T) {
	response := map[string]any{
		"id":    "resp_tool",
		"model": "gemini-3.7-flash-high",
		"output": []any{map[string]any{
			"type": "function_call", "call_id": "call-1", "name": "get_test_value", "arguments": `{"name":"smoke"}`,
			"thought_signature": "signature-1",
		}},
		"usage": map[string]any{"input_tokens": int64(3), "output_tokens": int64(8), "total_tokens": int64(11)},
	}
	completion := responseToChat(response, "fallback", false, 1)
	message := mapAny(mapAny(sliceAny(completion["choices"])[0])["message"])
	toolCalls := sliceAny(message["tool_calls"])
	if len(toolCalls) != 1 {
		t.Fatalf("tool calls = %#v", toolCalls)
	}
	toolCall := mapAny(toolCalls[0])
	transportID := stringValue(toolCall["id"])
	if !strings.HasPrefix(transportID, thoughtSignatureToolCallIDPrefix) {
		t.Fatalf("thought signature was not carried by tool call id: %q", transportID)
	}
	if fn := mapAny(toolCall["function"]); fn["thought_signature"] != nil {
		t.Fatalf("modern Chat function leaked thought signature: %#v", fn)
	}

	converted, err := chatToResponse(map[string]any{
		"model": "gemini-3.7-flash-high",
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": toolCalls},
			map[string]any{"role": "tool", "tool_call_id": transportID, "content": "AGY_POC_OK"},
		},
	}, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	input := sliceAny(converted["input"])
	if len(input) != 2 {
		t.Fatalf("converted input = %#v", input)
	}
	call := mapAny(input[0])
	if call["call_id"] != transportID || call["name"] != "get_test_value" || call["thought_signature"] != "signature-1" {
		t.Fatalf("converted function call = %#v", call)
	}
	output := mapAny(input[1])
	if output["call_id"] != transportID || output["name"] != "get_test_value" {
		t.Fatalf("converted function output = %#v", output)
	}
	legacyCompletion := responseToChat(response, "fallback", true, 1)
	legacyCall := mapAny(mapAny(sliceAny(legacyCompletion["choices"])[0])["message"])["function_call"]
	if mapAny(legacyCall)["thought_signature"] != "signature-1" {
		t.Fatalf("legacy function_call lost thought signature: %#v", legacyCall)
	}
}

func TestModernChatToolDeltaUsesTransportIDWithoutNonstandardFunctionField(t *testing.T) {
	item := map[string]any{
		"id": "call-1", "call_id": "call-1", "name": "get_test_value", "arguments": `{"name":"smoke"}`,
		"thought_signature": "signature-1",
	}
	delta := toolDelta(item, 0, item["arguments"].(string), true, false)
	toolCall := mapAny(sliceAny(delta["tool_calls"])[0])
	if !strings.HasPrefix(stringValue(toolCall["id"]), thoughtSignatureToolCallIDPrefix) {
		t.Fatalf("stream tool call did not use transport id: %#v", toolCall)
	}
	if fn := mapAny(toolCall["function"]); fn["thought_signature"] != nil {
		t.Fatalf("stream modern function leaked thought signature: %#v", fn)
	}
	if got := functionCallTransportID(map[string]any{
		"id": stringValue(toolCall["id"]), "call_id": stringValue(toolCall["id"]),
		"thought_signature": "signature-1",
	}); got != stringValue(toolCall["id"]) {
		t.Fatalf("already encoded tool id was wrapped again: got %q want %q", got, toolCall["id"])
	}
	legacy := toolDelta(item, 0, "", true, true)
	if mapAny(legacy["function_call"])["thought_signature"] != "signature-1" {
		t.Fatalf("legacy stream function_call lost thought signature: %#v", legacy)
	}
}

func TestChatToolOutputWithoutAssistantCallFailsClosed(t *testing.T) {
	_, err := chatToResponse(map[string]any{
		"messages": []any{map[string]any{"role": "tool", "tool_call_id": "call-missing", "content": "value"}},
	}, defaultModel)
	if err == nil || !strings.Contains(err.Error(), "function name") {
		t.Fatalf("orphan tool output was accepted: %v", err)
	}
}

func TestResponseAndChatStoresAreBoundedAndIsolated(t *testing.T) {
	responses := newResponseStore(1)
	responses.remember("one", []any{map[string]any{"role": "user", "content": "one"}}, map[string]any{"id": "one", "output": []any{outputMessage("answer")}})
	responses.remember("two", []any{}, map[string]any{"id": "two", "output": []any{}})
	if responses.get("one") != nil {
		t.Fatal("oldest response was not evicted")
	}
	got := responses.get("two")
	got.Response["status"] = "mutated"
	if responses.get("two").Response["status"] == "mutated" {
		t.Fatal("store leaked mutable state")
	}

	chats := newChatStore(1)
	completion := map[string]any{"id": "chatcmpl_one", "model": "gpt-test", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "hi"}}}}
	chats.remember("chatcmpl_one", completion, map[string]any{"key": "value"})
	stored := chats.get("chatcmpl_one")
	if len(stored.Messages) != 1 || stored.Messages[0]["id"] != "chatcmpl_one_msg_0" {
		t.Fatalf("messages = %#v", stored.Messages)
	}
	if _, err := json.Marshal(stored.Completion); err != nil {
		t.Fatal(err)
	}
}

func TestResponseStoreRefreshesExistingEntryEvictionOrder(t *testing.T) {
	responses := newResponseStore(2)
	responses.remember("one", []any{}, map[string]any{"id": "one", "output": []any{}})
	responses.remember("two", []any{}, map[string]any{"id": "two", "output": []any{}})
	responses.remember("one", []any{}, map[string]any{"id": "one", "output": []any{}})
	responses.remember("three", []any{}, map[string]any{"id": "three", "output": []any{}})
	if responses.get("one") == nil || responses.get("two") != nil || responses.get("three") == nil {
		t.Fatalf("unexpected eviction order: %#v", responses.order)
	}
}

func TestResponseStoreDerivesContextOnlyWhenRead(t *testing.T) {
	responses := newResponseStore(2)
	input := []any{map[string]any{"role": "user", "content": "question"}}
	responses.remember("one", input, map[string]any{
		"id": "one", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}}},
	})

	responses.mu.RLock()
	entry := responses.values["one"]
	responses.mu.RUnlock()
	if entry == nil || entry.Context != nil {
		t.Fatalf("store retained derived context: %#v", entry)
	}
	if len(entry.EffectiveInput) != 1 || len(entry.ResponseContext) != 1 {
		t.Fatalf("canonical store fields = %#v", entry)
	}
	got := responses.get("one")
	if len(got.Context) != 2 || len(got.EffectiveInput) != 1 {
		t.Fatalf("derived context = %#v", got)
	}
}

func BenchmarkResponseStoreRememberAndGet(b *testing.B) {
	input := make([]any, 256)
	for i := range input {
		input[i] = map[string]any{"role": "user", "content": "benchmark input item with enough text to exercise cloning"}
	}
	response := map[string]any{
		"id":     "benchmark",
		"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}}},
	}
	responses := newResponseStore(64)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		responses.remember("benchmark", input, response)
		_ = responses.get("benchmark")
	}
}

func TestPrepareResponseNormalizesReasoningAndDefaults(t *testing.T) {
	got := prepareResponse(map[string]any{"input": []any{
		map[string]any{"type": "reasoning", "summary": []any{}},
		map[string]any{"type": "reasoning", "encrypted_content": "cipher", "summary": []any{}},
	}}, "gpt-default")
	if got["model"] != "gpt-default" || got["store"] != false {
		t.Fatalf("defaults = %#v", got)
	}
	input := sliceAny(got["input"])
	if len(input) != 2 || mapAny(input[0])["type"] != "reasoning_summary" || mapAny(input[1])["encrypted_content"] != "cipher" {
		t.Fatalf("input = %#v", input)
	}
}
