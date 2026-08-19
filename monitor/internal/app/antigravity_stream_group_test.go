package app

import (
	"context"
	"testing"
)

func TestAntigravityStreamAssignsPositionalV2EnvelopeToParallelCalls(t *testing.T) {
	provider, _, closeServer := newTestAntigravityProvider(t, []string{`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"bash-1","name":"Bash","args":{"command":"one"}},"thoughtSignature":"sig-bash"},{"functionCall":{"id":"bash-2","name":"Bash","args":{"command":"two"}}},{"functionCall":{"id":"bash-3","name":"Bash","args":{"command":"three"}}}]},"finishReason":"STOP"}]}}`})
	defer closeServer()
	var events []map[string]any
	err := provider.stream(context.Background(), map[string]any{
		"model": stableAntigravityModel,
		"input": []any{map[string]any{"role": "user", "content": "run three commands"}},
	}, func(event map[string]any) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var output []any
	for _, event := range events {
		if event["type"] == "response.completed" {
			output = sliceAny(mapAny(event["response"])["output"])
		}
	}
	if len(output) != 3 {
		t.Fatalf("output = %#v", output)
	}
	for index, raw := range output {
		item := mapAny(raw)
		if item["type"] != "function_call" {
			t.Fatalf("output[%d] = %#v", index, item)
		}
		transportID := stringValue(item["call_id"])
		decoded, err := decodeFunctionCallTransportID(transportID)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Version != 2 || decoded.PartIndex != index || decoded.GroupSize != 3 || decoded.StepID == "" {
			t.Fatalf("output[%d] transport = %#v id=%q", index, decoded, transportID)
		}
		wantSignature := ""
		if index == 0 {
			wantSignature = "sig-bash"
		}
		if stringValue(item["thought_signature"]) != wantSignature || decoded.Signature != wantSignature {
			t.Fatalf("output[%d] signature = %#v decoded=%#v", index, item, decoded)
		}
	}
}
