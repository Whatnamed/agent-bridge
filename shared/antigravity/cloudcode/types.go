package cloudcode

import "encoding/json"

type Mode string

const (
	ModeCompat  Mode = "compat"
	ModeMinimal Mode = "minimal"
)

type Metadata struct {
	IdeType    string `json:"ideType,omitempty"`
	Platform   string `json:"platform,omitempty"`
	PluginType string `json:"pluginType,omitempty"`
}

type LoadCodeAssistRequest struct {
	Metadata Metadata `json:"metadata"`
}

type LoadCodeAssistResponse struct {
	CloudAICompanionProject string          `json:"cloudaicompanionProject"`
	GCPManaged              bool            `json:"gcpManaged"`
	CurrentTier             Tier            `json:"currentTier"`
	AllowedTiers            []Tier          `json:"allowedTiers"`
	ManageSubscriptionURI   string          `json:"manageSubscriptionUri"`
	UpgradeSubscriptionURI  string          `json:"upgradeSubscriptionUri"`
	Unknown                 json.RawMessage `json:"-"`
}

type Tier struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

type AvailableModel struct {
	DisplayName string `json:"displayName"`
}

type ModelsResponse struct {
	Models              map[string]AvailableModel `json:"models"`
	DefaultAgentModelID string                    `json:"defaultAgentModelId,omitempty"`
}

type ContentPart struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	EncryptedContent string            `json:"encryptedContent,omitempty"`
	InlineData       *InlineData       `json:"inlineData,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

type InlineData struct {
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

type Content struct {
	Role  string        `json:"role,omitempty"`
	Parts []ContentPart `json:"parts,omitempty"`
}

type SystemInstruction struct {
	Role  string        `json:"role,omitempty"`
	Parts []ContentPart `json:"parts,omitempty"`
}

type FunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name,omitempty"`
	Args map[string]any `json:"args,omitempty"`

	// ThoughtSignature and PartIndex are parser metadata. They identify the
	// exact candidate part that produced this call; they are not fields of the
	// nested functionCall object sent back to CloudCode.
	ThoughtSignature string `json:"-"`
	PartIndex        int    `json:"-"`
}

type FunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Response map[string]any `json:"response,omitempty"`
}

type ParameterSchema struct {
	Type        string                      `json:"type,omitempty"`
	Description string                      `json:"description,omitempty"`
	Properties  map[string]*ParameterSchema `json:"properties,omitempty"`
	Items       *ParameterSchema            `json:"items,omitempty"`
	Required    []string                    `json:"required,omitempty"`
	Enum        []string                    `json:"enum,omitempty"`
}

type FunctionDeclaration struct {
	Name        string           `json:"name,omitempty"`
	Description string           `json:"description,omitempty"`
	Parameters  *ParameterSchema `json:"parameters,omitempty"`
}

type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type ToolConfig struct {
	FunctionCallingConfig *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type ThinkingConfig struct {
	IncludeThoughts *bool  `json:"includeThoughts,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
}

type GenerationConfig struct {
	Temperature     float64         `json:"temperature,omitempty"`
	TopP            float64         `json:"topP,omitempty"`
	ThinkingConfig  *ThinkingConfig `json:"thinkingConfig,omitempty"`
	MaxOutputTokens int             `json:"maxOutputTokens,omitempty"`
}

type InternalRequest struct {
	Contents          []Content          `json:"contents,omitempty"`
	SystemInstruction *SystemInstruction `json:"systemInstruction,omitempty"`
	Tools             []Tool             `json:"tools,omitempty"`
	ToolConfig        *ToolConfig        `json:"toolConfig,omitempty"`
	GenerationConfig  *GenerationConfig  `json:"generationConfig,omitempty"`
	SessionID         string             `json:"sessionId,omitempty"`
}

type GenerateRequest struct {
	Model        string          `json:"model,omitempty"`
	Project      string          `json:"project,omitempty"`
	UserPromptID string          `json:"user_prompt_id,omitempty"`
	Request      InternalRequest `json:"request,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	UserAgent    string          `json:"userAgent,omitempty"`
	RequestType  string          `json:"requestType,omitempty"`
	RequestID    string          `json:"requestId,omitempty"`
}

type Usage struct {
	InputTokens int64 `json:"input_tokens,omitempty"`
	// OutputTokens is the visible candidate output count from CloudCode;
	// thinking tokens are kept separately and combined only at the public
	// Responses usage boundary.
	OutputTokens   int64 `json:"output_tokens,omitempty"`
	ThinkingTokens int64 `json:"thinking_tokens,omitempty"`
	CachedTokens   int64 `json:"cached_tokens,omitempty"`
	TotalTokens    int64 `json:"total_tokens,omitempty"`
}

func (u Usage) HasData() bool {
	return u.InputTokens != 0 || u.OutputTokens != 0 || u.ThinkingTokens != 0 || u.CachedTokens != 0 || u.TotalTokens != 0
}

func (u *Usage) Merge(other Usage) {
	if u == nil {
		return
	}
	if other.InputTokens != 0 {
		u.InputTokens = other.InputTokens
	}
	if other.OutputTokens != 0 {
		u.OutputTokens = other.OutputTokens
	}
	if other.ThinkingTokens != 0 {
		u.ThinkingTokens = other.ThinkingTokens
	}
	if other.CachedTokens != 0 {
		u.CachedTokens = other.CachedTokens
	}
	if other.TotalTokens != 0 {
		u.TotalTokens = other.TotalTokens
	}
}

func (u *Usage) Add(other Usage) {
	if u == nil {
		return
	}
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.ThinkingTokens += other.ThinkingTokens
	u.CachedTokens += other.CachedTokens
	u.TotalTokens += other.TotalTokens
}

type Candidate struct {
	Role         string
	FinishReason string
	Parts        []ContentPart
}

type Event struct {
	Done              bool
	CleanEOF          bool
	Text              string
	Reasoning         string
	FinishReason      string
	Usage             Usage
	FunctionCalls     []FunctionCall
	Candidates        []Candidate
	ThoughtSignatures []string
}
