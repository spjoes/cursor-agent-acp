package acp

import "time"

type InitializeRequest struct {
	ProtocolVersion    int             `json:"protocolVersion"`
	ClientInfo         *Implementation `json:"clientInfo,omitempty"`
	ClientCapabilities map[string]any  `json:"clientCapabilities,omitempty"`
}

type InitializeResponse struct {
	ProtocolVersion   int              `json:"protocolVersion"`
	AgentCapabilities map[string]any   `json:"agentCapabilities"`
	AgentInfo         Implementation   `json:"agentInfo"`
	AuthMethods       []map[string]any `json:"authMethods"`
	Meta              map[string]any   `json:"_meta,omitempty"`
}

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type NewSessionRequest struct {
	Cwd        string           `json:"cwd"`
	McpServers []map[string]any `json:"mcpServers"`
	Metadata   map[string]any   `json:"metadata,omitempty"`
}

type NewSessionResponse struct {
	SessionID string             `json:"sessionId"`
	Modes     *SessionModeState  `json:"modes,omitempty"`
	Models    *SessionModelState `json:"models,omitempty"`
	Meta      map[string]any     `json:"_meta,omitempty"`
}

type LoadSessionRequest struct {
	SessionID  string           `json:"sessionId"`
	Cwd        string           `json:"cwd"`
	McpServers []map[string]any `json:"mcpServers"`
	Metadata   map[string]any   `json:"metadata,omitempty"`
}

type LoadSessionResponse struct {
	Modes  *SessionModeState  `json:"modes,omitempty"`
	Models *SessionModelState `json:"models,omitempty"`
	Meta   map[string]any     `json:"_meta,omitempty"`
}

type SetSessionModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type SetSessionModeResponse struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

type SetSessionModelRequest struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

type SetSessionModelResponse struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

type ListSessionsRequest struct {
	Limit  int            `json:"limit,omitempty"`
	Offset int            `json:"offset,omitempty"`
	Filter map[string]any `json:"filter,omitempty"`
}

type ListSessionsResponse struct {
	Sessions []SessionInfo `json:"sessions"`
	Total    int           `json:"total"`
	HasMore  bool          `json:"hasMore"`
}

type UpdateSessionRequest struct {
	SessionID string         `json:"sessionId"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type DeleteSessionRequest struct {
	SessionID string `json:"sessionId"`
}

type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt,omitempty"`
	Content   []ContentBlock `json:"content,omitempty"`
	Stream    bool           `json:"stream,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type PromptResponse struct {
	StopReason string         `json:"stopReason"`
	Meta       map[string]any `json:"_meta,omitempty"`
}

type CancelNotification struct {
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId,omitempty"`
}

type ToolCallRequest struct {
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolsListResponse struct {
	Tools []ToolDescriptor `json:"tools"`
}

type ToolResult struct {
	Success  bool           `json:"success"`
	Result   any            `json:"result,omitempty"`
	Error    string         `json:"error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Content blocks follow ACP schema.
type ContentBlock struct {
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
	Data        string            `json:"data,omitempty"`
	MimeType    string            `json:"mimeType,omitempty"`
	URI         string            `json:"uri,omitempty"`
	Name        string            `json:"name,omitempty"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Size        any               `json:"size,omitempty"`
	Resource    *EmbeddedResource `json:"resource,omitempty"`
	Annotations map[string]any    `json:"annotations,omitempty"`
}

type EmbeddedResource struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

type SessionModel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
}

type SessionModelEntry struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name"`
}

type SessionModelState struct {
	AvailableModels []SessionModelEntry `json:"availableModels"`
	CurrentModelID  string              `json:"currentModelId"`
}

type ConversationMessage struct {
	ID        string         `json:"id"`
	Role      string         `json:"role"`
	Content   []ContentBlock `json:"content"`
	Timestamp time.Time      `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type SessionState struct {
	LastActivity time.Time `json:"lastActivity"`
	MessageCount int       `json:"messageCount"`
	TokenCount   int       `json:"tokenCount,omitempty"`
	Status       string    `json:"status"`
	CurrentMode  string    `json:"currentMode,omitempty"`
	CurrentModel string    `json:"currentModel,omitempty"`
}

type SessionData struct {
	ID           string                `json:"id"`
	Metadata     map[string]any        `json:"metadata"`
	Conversation []ConversationMessage `json:"conversation"`
	State        SessionState          `json:"state"`
	CreatedAt    time.Time             `json:"createdAt"`
	UpdatedAt    time.Time             `json:"updatedAt"`
}

type SessionInfo struct {
	ID        string         `json:"id"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	Status    string         `json:"status"`
}
