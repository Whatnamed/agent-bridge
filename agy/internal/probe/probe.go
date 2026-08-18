package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/whatnamed/agent-bridge/agy/internal/cloudcode"
)

const (
	DefaultModel          = "gemini-3.7-flash-high"
	DefaultPrompt         = "只回复：OAUTH_OK"
	DefaultToolTestPrompt = `必须调用 get_test_value，参数 name="smoke"。获得工具结果后，只回复工具返回的 value。`
)

type Config struct {
	Mode            cloudcode.Mode
	RequestedModel  string
	Project         string
	Prompt          string
	PromptSpecified bool
	ToolTest        bool
}

type Result struct {
	Status              string `json:"status"`
	Endpoint            string `json:"endpoint"`
	RequestedModel      string `json:"requested_model"`
	ResolvedModel       string `json:"resolved_model,omitempty"`
	ActualUpstreamModel string `json:"actual_upstream_model,omitempty"`
	ModelResolution     string `json:"model_resolution"`
	LoadCodeAssistMS    int64  `json:"load_code_assist_ms,omitempty"`
	FetchModelsMS       int64  `json:"fetch_models_ms,omitempty"`
	RequestToHeadersMS  int64  `json:"request_to_headers_ms,omitempty"`
	FirstSSEEventMS     int64  `json:"first_sse_event_ms,omitempty"`
	FirstTextMS         int64  `json:"first_text_ms,omitempty"`
	FirstReasoningMS    int64  `json:"first_reasoning_ms,omitempty"`
	TTFTMS              int64  `json:"ttft_ms,omitempty"`
	CompletionMS        int64  `json:"completion_ms,omitempty"`
	GenerationTotalMS   int64  `json:"generation_total_ms,omitempty"`
	WholeProbeTotalMS   int64  `json:"whole_probe_total_ms,omitempty"`
	InputTokens         int64  `json:"input_tokens,omitempty"`
	OutputTokens        int64  `json:"output_tokens,omitempty"`
	ThinkingTokens      int64  `json:"thinking_tokens,omitempty"`
	CachedTokens        int64  `json:"cached_tokens,omitempty"`
	TotalTokens         int64  `json:"total_tokens,omitempty"`
	FinishReason        string `json:"finish_reason,omitempty"`
	ResponseText        string `json:"response_text,omitempty"`
	ReasoningText       string `json:"reasoning_text,omitempty"`
	ToolTestRequested   bool   `json:"tool_test_requested,omitempty"`
	ToolCallName        string `json:"tool_call_name,omitempty"`
	ToolRoundTrip       bool   `json:"tool_round_trip,omitempty"`
	Cancelled           bool   `json:"cancelled,omitempty"`
}

func Run(ctx context.Context, client *cloudcode.Client, config Config) (result Result, err error) {
	wholeProbeStart := time.Now()
	defer func() {
		result.WholeProbeTotalMS = elapsedMilliseconds(wholeProbeStart)
	}()

	if client == nil {
		return Result{Status: "error"}, errors.New("probe CloudCode client is nil")
	}
	if config.Mode == "" {
		config.Mode = cloudcode.ModeCompat
	}
	if strings.TrimSpace(config.RequestedModel) == "" {
		config.RequestedModel = DefaultModel
	}
	if !config.PromptSpecified && strings.TrimSpace(config.Prompt) == "" {
		if config.ToolTest {
			config.Prompt = DefaultToolTestPrompt
		} else {
			config.Prompt = DefaultPrompt
		}
	}

	result = Result{
		Status:            "starting",
		Endpoint:          client.Endpoint,
		RequestedModel:    config.RequestedModel,
		ToolTestRequested: config.ToolTest,
	}

	loadStart := time.Now()
	loadResponse, err := client.LoadCodeAssist(ctx)
	result.LoadCodeAssistMS = elapsedMilliseconds(loadStart)
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

	fetchStart := time.Now()
	catalog, err := client.FetchAvailableModels(ctx)
	result.FetchModelsMS = elapsedMilliseconds(fetchStart)
	if err != nil {
		return resultWithError(ctx, result, fmt.Errorf("fetchAvailableModels: %w", err))
	}
	modelResolution := cloudcode.ResolveModel(config.RequestedModel, catalog)
	result.RequestedModel = modelResolution.RequestedModel
	result.ResolvedModel = modelResolution.ActualUpstreamModel
	result.ActualUpstreamModel = modelResolution.ActualUpstreamModel
	result.ModelResolution = modelResolution.Status
	if !modelResolution.Verified() {
		result.Status = "model_not_found"
		return result, fmt.Errorf("model-not-found: requested model %q was not present in the current CloudCode catalog; status=%s; generation was not sent", modelResolution.RequestedModel, modelResolution.Status)
	}

	request, err := cloudcode.NewRequest(config.Mode, modelResolution.ActualUpstreamModel, project, config.Prompt, config.ToolTest)
	if err != nil {
		return resultWithError(ctx, result, err)
	}
	generationStart := time.Now()
	first, err := consumeStream(ctx, client, request, generationStart, &result)
	if err != nil {
		return resultWithError(ctx, result, err)
	}
	result.Status = "ok"
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
	second, err := consumeStream(ctx, client, continuation, generationStart, &result)
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

func consumeStream(ctx context.Context, client *cloudcode.Client, request cloudcode.GenerateRequest, generationStart time.Time, result *Result) (streamSummary, error) {
	stream, err := client.StreamGenerateContent(ctx, request)
	if err != nil {
		result.GenerationTotalMS = elapsedMilliseconds(generationStart)
		return streamSummary{}, err
	}
	defer stream.Close()
	if result.RequestToHeadersMS == 0 {
		receivedAt := stream.ResponseReceivedAt
		if receivedAt.IsZero() {
			receivedAt = time.Now()
		}
		result.RequestToHeadersMS = durationMilliseconds(generationStart, receivedAt)
	}
	summary := streamSummary{}
	for {
		event, err := stream.Next()
		if err != nil {
			result.GenerationTotalMS = elapsedMilliseconds(generationStart)
			return summary, err
		}
		if event.Done {
			result.CompletionMS = elapsedMilliseconds(generationStart)
			result.GenerationTotalMS = result.CompletionMS
			break
		}
		if result.FirstSSEEventMS == 0 {
			result.FirstSSEEventMS = elapsedMilliseconds(generationStart)
		}
		if event.Text != "" {
			if result.FirstTextMS == 0 {
				result.FirstTextMS = elapsedMilliseconds(generationStart)
				result.TTFTMS = result.FirstTextMS
			}
			summary.text += event.Text
		}
		if event.Reasoning != "" {
			if result.FirstReasoningMS == 0 {
				result.FirstReasoningMS = elapsedMilliseconds(generationStart)
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
	return durationMilliseconds(start, time.Now())
}

func durationMilliseconds(start, end time.Time) int64 {
	value := end.Sub(start).Milliseconds()
	if value < 1 {
		return 1
	}
	return value
}
