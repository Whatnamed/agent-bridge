package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/whatnamed/agent-bridge/agy/internal/cloudcode"
)

const DefaultModel = "gemini-3.7-flash-high"

type Config struct {
	Mode           cloudcode.Mode
	RequestedModel string
	Project        string
	Prompt         string
	ToolTest       bool
}

type Result struct {
	Status            string `json:"status"`
	Endpoint          string `json:"endpoint"`
	RequestedModel    string `json:"requested_model"`
	ResolvedModel     string `json:"resolved_model"`
	ModelResolution   string `json:"model_resolution"`
	TTFTMS            int64  `json:"ttft_ms,omitempty"`
	FirstByteMS       int64  `json:"first_sse_event_ms,omitempty"`
	FirstTextMS       int64  `json:"first_text_ms,omitempty"`
	FirstReasoningMS  int64  `json:"first_reasoning_ms,omitempty"`
	CompletionMS      int64  `json:"completion_ms,omitempty"`
	TotalDurationMS   int64  `json:"total_duration_ms,omitempty"`
	InputTokens       int64  `json:"input_tokens,omitempty"`
	OutputTokens      int64  `json:"output_tokens,omitempty"`
	ThinkingTokens    int64  `json:"thinking_tokens,omitempty"`
	CachedTokens      int64  `json:"cached_tokens,omitempty"`
	TotalTokens       int64  `json:"total_tokens,omitempty"`
	FinishReason      string `json:"finish_reason,omitempty"`
	ResponseText      string `json:"response_text,omitempty"`
	ReasoningText     string `json:"reasoning_text,omitempty"`
	ToolTestRequested bool   `json:"tool_test_requested,omitempty"`
	ToolCallName      string `json:"tool_call_name,omitempty"`
	ToolRoundTrip     bool   `json:"tool_round_trip,omitempty"`
	Cancelled         bool   `json:"cancelled,omitempty"`
}

func Run(ctx context.Context, client *cloudcode.Client, config Config) (Result, error) {
	if client == nil {
		return Result{Status: "error"}, errors.New("probe CloudCode client is nil")
	}
	if config.Mode == "" {
		config.Mode = cloudcode.ModeCompat
	}
	if config.RequestedModel == "" {
		config.RequestedModel = DefaultModel
	}
	if config.Prompt == "" {
		config.Prompt = "只回复：OAUTH_OK"
	}

	result := Result{
		Status:            "starting",
		Endpoint:          client.Endpoint,
		RequestedModel:    config.RequestedModel,
		ToolTestRequested: config.ToolTest,
	}
	start := time.Now()
	loadResponse, err := client.LoadCodeAssist(ctx)
	if err != nil {
		result.Status = statusForError(ctx, err)
		result.Cancelled = result.Status == "cancelled"
		return result, fmt.Errorf("loadCodeAssist: %w", err)
	}
	project := strings.TrimSpace(config.Project)
	if project == "" {
		project = strings.TrimSpace(loadResponse.CloudAICompanionProject)
	}
	if project == "" {
		if loadResponse.GCPManaged {
			return resultWithError(ctx, result, errors.New("loadCodeAssist requires GCP-managed project onboarding; this isolated POC does not perform onboarding"))
		}
		return resultWithError(ctx, result, errors.New("loadCodeAssist did not return a Cloud AI Companion project; pass --project only when independently verified"))
	}

	catalog, err := client.FetchAvailableModels(ctx)
	if err != nil {
		return resultWithError(ctx, result, fmt.Errorf("fetchAvailableModels: %w", err))
	}
	resolved, resolution := cloudcode.ResolveModel(config.RequestedModel, catalog)
	result.ResolvedModel = resolved
	result.ModelResolution = resolution

	request, err := cloudcode.NewRequest(config.Mode, resolved, project, config.Prompt, config.ToolTest)
	if err != nil {
		return resultWithError(ctx, result, err)
	}
	first, err := consumeStream(ctx, client, request, start, &result)
	if err != nil {
		return resultWithError(ctx, result, err)
	}
	result.Status = "ok"
	result.TotalDurationMS = elapsedMilliseconds(start)
	result.InputTokens = first.usage.InputTokens
	result.OutputTokens = first.usage.OutputTokens
	result.ThinkingTokens = first.usage.ThinkingTokens
	result.CachedTokens = first.usage.CachedTokens
	result.TotalTokens = first.usage.TotalTokens
	result.FinishReason = first.finishReason

	if !config.ToolTest {
		return result, nil
	}
	if len(first.functionCalls) == 0 {
		return resultWithError(ctx, result, errors.New("tool test requested but the model did not emit a functionCall"))
	}
	call := first.functionCalls[0]
	if call.Name != "get_test_value" {
		return resultWithError(ctx, result, fmt.Errorf("tool test received unsupported function %q; refusing to execute it", call.Name))
	}
	name, ok := call.Args["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return resultWithError(ctx, result, errors.New("get_test_value call did not contain a non-empty name"))
	}
	result.ToolCallName = call.Name

	assistantContent := first.assistantToolContent()
	if len(assistantContent.Parts) == 0 {
		return resultWithError(ctx, result, errors.New("tool call response did not contain assistant content for continuation"))
	}
	continuation := request
	continuation.RequestID, err = cloudcode.NewRequestID()
	if err != nil {
		return resultWithError(ctx, result, err)
	}
	continuation.Request.Contents = append(append([]cloudcode.Content(nil), request.Request.Contents...), assistantContent)
	continuation.Request.Contents = append(continuation.Request.Contents, cloudcode.Content{
		Role: "user",
		Parts: []cloudcode.ContentPart{{FunctionResponse: &cloudcode.FunctionResponse{
			ID:   call.ID,
			Name: call.Name,
			Response: map[string]any{
				"name":  name,
				"value": "AGY_POC_OK",
			},
		}}},
	})
	second, err := consumeStream(ctx, client, continuation, start, &result)
	if err != nil {
		return resultWithError(ctx, result, err)
	}
	result.ToolRoundTrip = true
	result.ThinkingTokens += second.usage.ThinkingTokens
	result.InputTokens += second.usage.InputTokens
	result.OutputTokens += second.usage.OutputTokens
	result.CachedTokens += second.usage.CachedTokens
	result.TotalTokens += second.usage.TotalTokens
	if second.finishReason != "" {
		result.FinishReason = second.finishReason
	}
	result.TotalDurationMS = elapsedMilliseconds(start)
	return result, nil
}

type streamSummary struct {
	text          string
	reasoning     string
	finishReason  string
	usage         cloudcode.Usage
	functionCalls []cloudcode.FunctionCall
	candidates    []cloudcode.Candidate
}

func consumeStream(ctx context.Context, client *cloudcode.Client, request cloudcode.GenerateRequest, start time.Time, result *Result) (streamSummary, error) {
	stream, err := client.StreamGenerateContent(ctx, request)
	if err != nil {
		result.TotalDurationMS = elapsedMilliseconds(start)
		return streamSummary{}, err
	}
	defer stream.Close()
	summary := streamSummary{}
	firstEvent := true
	for {
		event, err := stream.Next()
		if err != nil {
			result.TotalDurationMS = elapsedMilliseconds(start)
			return summary, err
		}
		now := elapsedMilliseconds(start)
		if firstEvent {
			result.FirstByteMS = now
			firstEvent = false
		}
		if event.Done {
			result.CompletionMS = now
			break
		}
		if event.Text != "" {
			if result.FirstTextMS == 0 {
				result.FirstTextMS = now
				result.TTFTMS = now
			}
			summary.text += event.Text
		}
		if event.Reasoning != "" {
			if result.FirstReasoningMS == 0 {
				result.FirstReasoningMS = now
			}
			summary.reasoning += event.Reasoning
		}
		if event.FinishReason != "" {
			summary.finishReason = event.FinishReason
		}
		summary.usage.Merge(event.Usage)
		summary.functionCalls = append(summary.functionCalls, event.FunctionCalls...)
		summary.candidates = append(summary.candidates, event.Candidates...)
	}
	result.ResponseText += summary.text
	result.ReasoningText += summary.reasoning
	return summary, nil
}

func (s streamSummary) assistantToolContent() cloudcode.Content {
	var parts []cloudcode.ContentPart
	for _, candidate := range s.candidates {
		for _, part := range candidate.Parts {
			if part.FunctionCall != nil || part.ThoughtSignature != "" {
				parts = append(parts, part)
			}
		}
	}
	return cloudcode.Content{Role: "model", Parts: parts}
}

func resultWithError(ctx context.Context, result Result, err error) (Result, error) {
	result.Status = statusForError(ctx, err)
	result.Cancelled = result.Status == "cancelled"
	return result, err
}

func statusForError(ctx context.Context, err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return "cancelled"
	}
	return "error"
}

func elapsedMilliseconds(start time.Time) int64 {
	value := time.Since(start).Milliseconds()
	if value < 1 {
		return 1
	}
	return value
}
