package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/whatnamed/agent-bridge/shared/antigravity/cloudcode"
)

type antigravityFunctionCallRef struct {
	decoded decodedFunctionCallTransport
	name    string
}

type antigravityFunctionCallGroup struct {
	parts     []cloudcode.ContentPart
	calls     []antigravityFunctionCallRef
	explicit  bool
	stepID    string
	groupSize int
}

// antigravityContents reconstructs GenerateContent history from the durable
// canonical function-call IDs. A parallel step is one model Content with
// ordered functionCall parts followed by one user Content with ordered
// functionResponse parts. Sequential steps are separated by their IDs and by
// the intervening function output, so they remain independent Content pairs.
func antigravityContents(input []any) ([]cloudcode.Content, []cloudcode.ContentPart, error) {
	contents := make([]cloudcode.Content, 0, len(input))
	systemParts := make([]cloudcode.ContentPart, 0)
	functionNames, err := responseFunctionCallNames(input)
	if err != nil {
		return nil, nil, err
	}

	var pendingCalls *antigravityFunctionCallGroup
	var pendingOutputs *antigravityFunctionCallGroup
	var activeCalls *antigravityFunctionCallGroup

	flushCalls := func() error {
		if pendingCalls == nil {
			return nil
		}
		if err := validateAntigravityFunctionCallGroup(pendingCalls); err != nil {
			return err
		}
		contents = append(contents, cloudcode.Content{Role: "model", Parts: append([]cloudcode.ContentPart(nil), pendingCalls.parts...)})
		activeCalls = pendingCalls
		pendingCalls = nil
		return nil
	}
	flushOutputs := func() error {
		if pendingOutputs == nil {
			return nil
		}
		if activeCalls == nil {
			return errors.New("function_call_output has no immediately preceding function_call group")
		}
		if err := validateAntigravityFunctionResponseGroup(activeCalls, pendingOutputs); err != nil {
			return err
		}
		contents = append(contents, cloudcode.Content{Role: "user", Parts: append([]cloudcode.ContentPart(nil), pendingOutputs.parts...)})
		pendingOutputs = nil
		activeCalls = nil
		return nil
	}
	flushPending := func() error {
		if err := flushCalls(); err != nil {
			return err
		}
		return flushOutputs()
	}

	for _, raw := range input {
		item := mapAny(raw)
		if item == nil {
			if text := strings.TrimSpace(stringValue(raw)); text != "" {
				if err := flushPending(); err != nil {
					return nil, nil, err
				}
				contents = append(contents, cloudcode.Content{Role: "user", Parts: []cloudcode.ContentPart{{Text: text}}})
			} else if raw != nil {
				return nil, nil, errors.New("Antigravity input supports text and known message/tool items only")
			}
			continue
		}

		typ := strings.TrimSpace(stringValue(item["type"]))
		switch typ {
		case "function_call":
			if pendingOutputs != nil {
				if err := flushOutputs(); err != nil {
					return nil, nil, err
				}
			}
			transportID := strings.TrimSpace(stringValue(valueOr(item["call_id"], item["id"])))
			name := strings.TrimSpace(stringValue(item["name"]))
			if transportID == "" || name == "" {
				return nil, nil, errors.New("function_call must include call_id and name")
			}
			decoded, err := decodeFunctionCallTransportID(transportID)
			if err != nil {
				return nil, nil, err
			}
			if pendingCalls == nil || !sameAntigravityFunctionCallGroup(pendingCalls, decoded) {
				if err := flushCalls(); err != nil {
					return nil, nil, err
				}
				pendingCalls = newAntigravityFunctionCallGroup(decoded)
			}
			if decoded.Version == 2 && decoded.PartIndex != len(pendingCalls.parts) {
				return nil, nil, errors.New("Antigravity parallel function calls are not in positional order")
			}
			for _, existing := range pendingCalls.calls {
				if existing.decoded.CallID == decoded.CallID {
					return nil, nil, errors.New("Antigravity function_call reused a call id in one step")
				}
			}
			signature := firstMapString(item, "thought_signature", "thoughtSignature")
			if decoded.Signature != "" {
				if signature != "" && signature != decoded.Signature {
					return nil, nil, errors.New("function_call thought signature does not match its transport id")
				}
				signature = decoded.Signature
			}
			if decoded.Version == 0 && signature == "" {
				return nil, nil, errors.New("function_call thought signature could not be recovered")
			}
			part := cloudcode.ContentPart{FunctionCall: &cloudcode.FunctionCall{ID: decoded.CallID, Name: name, Args: mapValueFromJSON(item["arguments"])}}
			part.ThoughtSignature = signature
			pendingCalls.parts = append(pendingCalls.parts, part)
			pendingCalls.calls = append(pendingCalls.calls, antigravityFunctionCallRef{decoded: decoded, name: name})

		case "function_call_output":
			if pendingCalls != nil {
				if err := flushCalls(); err != nil {
					return nil, nil, err
				}
			}
			if activeCalls == nil {
				return nil, nil, errors.New("function_call_output function name could not be resolved: no matching function_call")
			}
			transportID := strings.TrimSpace(stringValue(item["call_id"]))
			if transportID == "" {
				return nil, nil, errors.New("function_call_output must include call_id")
			}
			decoded, err := decodeFunctionCallTransportID(transportID)
			if err != nil {
				return nil, nil, err
			}
			if pendingOutputs == nil {
				pendingOutputs = &antigravityFunctionCallGroup{parts: make([]cloudcode.ContentPart, 0, len(activeCalls.calls)), calls: make([]antigravityFunctionCallRef, 0, len(activeCalls.calls)), explicit: activeCalls.explicit, stepID: activeCalls.stepID, groupSize: activeCalls.groupSize}
			}
			if !sameAntigravityFunctionCallGroup(pendingOutputs, decoded) {
				return nil, nil, errors.New("function_call_output does not belong to the active function-call group")
			}
			next := len(pendingOutputs.parts)
			if next >= len(activeCalls.calls) {
				return nil, nil, errors.New("too many function_call_output items for a function-call group")
			}
			expected := activeCalls.calls[next]
			if decoded.Version == 2 && decoded.PartIndex != next {
				return nil, nil, errors.New("function_call_output position does not match its function-call group")
			}
			if decoded.CallID != expected.decoded.CallID {
				return nil, nil, errors.New("function_call_output order does not match its function-call group")
			}
			name, err := resolveResponseFunctionOutputName(item, functionNames)
			if err != nil {
				return nil, nil, err
			}
			if name != expected.name {
				return nil, nil, errors.New("function_call_output name does not match its function_call")
			}
			pendingOutputs.parts = append(pendingOutputs.parts, cloudcode.ContentPart{FunctionResponse: &cloudcode.FunctionResponse{ID: decoded.CallID, Name: name, Response: functionResponseValue(item["output"])}})
			pendingOutputs.calls = append(pendingOutputs.calls, antigravityFunctionCallRef{decoded: decoded, name: name})

		case "input_image":
			if err := flushPending(); err != nil {
				return nil, nil, err
			}
			part, err := antigravityImageContentPart(item)
			if err != nil {
				return nil, nil, err
			}
			contents = append(contents, cloudcode.Content{Role: "user", Parts: []cloudcode.ContentPart{part}})

		case "reasoning":
			if err := flushPending(); err != nil {
				return nil, nil, err
			}
			if encrypted := firstMapString(item, "encrypted_content", "encryptedContent"); encrypted != "" {
				contents = append(contents, cloudcode.Content{Role: "model", Parts: []cloudcode.ContentPart{{EncryptedContent: encrypted}}})
			} else {
				return nil, nil, errors.New("Antigravity reasoning input requires encrypted content")
			}

		case "reasoning_summary":
			// Public summaries are not provider-private Gemini state. They are
			// replay-safe metadata emitted by this bridge and must not become a
			// synthetic CloudCode part.
			continue

		case "input_text", "text", "output_text":
			if err := flushPending(); err != nil {
				return nil, nil, err
			}
			if _, ok := item["text"]; !ok {
				return nil, nil, fmt.Errorf("Antigravity %s input must include text", typ)
			}
			role := "user"
			if typ == "output_text" {
				role = "model"
			}
			contents = append(contents, cloudcode.Content{Role: role, Parts: []cloudcode.ContentPart{{Text: stringValue(item["text"])}}})

		case "message", "":
			role := stringValue(item["role"])
			if role == "system" || role == "developer" {
				if err := flushPending(); err != nil {
					return nil, nil, err
				}
				text, err := antigravityInstructionText(item["content"])
				if err != nil {
					return nil, nil, err
				}
				if text = strings.TrimSpace(text); text != "" {
					systemParts = append(systemParts, cloudcode.ContentPart{Text: text})
				}
				continue
			}
			if err := flushPending(); err != nil {
				return nil, nil, err
			}
			if role == "assistant" {
				role = "model"
			} else if role == "" || role == "user" {
				role = "user"
			} else {
				return nil, nil, fmt.Errorf("unsupported Responses input role %q", role)
			}
			parts, err := antigravityMessageParts(item["content"])
			if err != nil {
				return nil, nil, err
			}
			if len(parts) > 0 {
				contents = append(contents, cloudcode.Content{Role: role, Parts: parts})
			}

		default:
			return nil, nil, fmt.Errorf("Antigravity does not support input item type %q", typ)
		}
	}
	if err := flushPending(); err != nil {
		return nil, nil, err
	}
	if len(contents) == 0 {
		return nil, nil, errors.New("Antigravity request input is empty")
	}
	return contents, systemParts, nil
}

func newAntigravityFunctionCallGroup(decoded decodedFunctionCallTransport) *antigravityFunctionCallGroup {
	return &antigravityFunctionCallGroup{
		parts:     make([]cloudcode.ContentPart, 0, maxInt(decoded.GroupSize, 1)),
		calls:     make([]antigravityFunctionCallRef, 0, maxInt(decoded.GroupSize, 1)),
		explicit:  decoded.Version == 2,
		stepID:    decoded.StepID,
		groupSize: maxInt(decoded.GroupSize, 1),
	}
}

func sameAntigravityFunctionCallGroup(group *antigravityFunctionCallGroup, decoded decodedFunctionCallTransport) bool {
	if group == nil {
		return false
	}
	if group.explicit != (decoded.Version == 2) {
		return false
	}
	if !group.explicit {
		return true
	}
	return group.stepID == decoded.StepID && group.groupSize == decoded.GroupSize
}

func validateAntigravityFunctionCallGroup(group *antigravityFunctionCallGroup) error {
	if group == nil || len(group.parts) == 0 {
		return errors.New("Antigravity function-call group is empty")
	}
	if !group.explicit {
		return nil
	}
	if len(group.parts) != group.groupSize || len(group.calls) != group.groupSize {
		return errors.New("Antigravity parallel function-call group is incomplete")
	}
	for index, call := range group.calls {
		if call.decoded.PartIndex != index {
			return errors.New("Antigravity parallel function-call group has missing or duplicate positions")
		}
	}
	return nil
}

func validateAntigravityFunctionResponseGroup(calls, outputs *antigravityFunctionCallGroup) error {
	if calls == nil || outputs == nil || len(outputs.parts) != len(calls.calls) {
		return errors.New("Antigravity function-response group is incomplete")
	}
	if outputs.explicit && (outputs.groupSize != calls.groupSize || outputs.stepID != calls.stepID) {
		return errors.New("Antigravity function-response group metadata does not match its function-call group")
	}
	return nil
}

func maxInt(value, fallback int) int {
	if value > fallback {
		return value
	}
	return fallback
}
