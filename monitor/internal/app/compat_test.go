package app

import (
	"encoding/json"
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
	got := chatToResponse(body, defaultModel)
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
	if len(input) != 1 || mapAny(input[0])["encrypted_content"] != "cipher" {
		t.Fatalf("input = %#v", input)
	}
}
