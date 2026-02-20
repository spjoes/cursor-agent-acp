package tools

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/client"
	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/cursor"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
	"github.com/spjoes/cursor-agent-acp/internal/toolcall"
)

type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Handler     func(params map[string]any) (acp.ToolResult, error)
}

type ToolProvider interface {
	Name() string
	Description() string
	GetTools() []Tool
	Cleanup() error
}

type ToolCall struct {
	ID         string
	Name       string
	Parameters map[string]any
}

type Registry struct {
	cfg    config.Config
	logger *logging.Logger

	providers map[string]ToolProvider
	tools     map[string]Tool

	cursorBridge *cursor.Bridge
	toolCalls    *toolcall.Manager
}

func NewRegistry(cfg config.Config, logger *logging.Logger, cursorBridge *cursor.Bridge) *Registry {
	r := &Registry{
		cfg:          cfg,
		logger:       logger,
		providers:    map[string]ToolProvider{},
		tools:        map[string]Tool{},
		cursorBridge: cursorBridge,
	}
	r.initializeProviders()
	return r
}

func (r *Registry) SetToolCallManager(manager *toolcall.Manager) {
	r.toolCalls = manager
	r.logger.Debug("ToolCallManager registered with ToolRegistry", nil)
}

func (r *Registry) RegisterProvider(provider ToolProvider) {
	r.logger.Debug("Registering tool provider", map[string]any{"provider": provider.Name()})
	r.providers[provider.Name()] = provider
	for _, t := range provider.GetTools() {
		r.tools[t.Name] = t
		r.logger.Debug("Registered tool", map[string]any{"tool": t.Name})
	}
}

func (r *Registry) UnregisterProvider(providerName string) {
	provider, ok := r.providers[providerName]
	if !ok {
		r.logger.Warn("Tool provider not found", map[string]any{"provider": providerName})
		return
	}
	for _, t := range provider.GetTools() {
		delete(r.tools, t.Name)
	}
	delete(r.providers, providerName)
}

func (r *Registry) ConfigureFilesystemProvider(clientCapabilities map[string]any, fsClient client.FileSystemClient) {
	r.UnregisterProvider("filesystem")
	if !r.cfg.Tools.Filesystem.Enabled {
		return
	}
	provider := NewFilesystemProvider(r.cfg, r.logger, clientCapabilities, fsClient)
	r.RegisterProvider(provider)
}

func (r *Registry) GetTools() []Tool {
	tools := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		tools = append(tools, t)
	}
	return tools
}

func (r *Registry) ToolDescriptors() []acp.ToolDescriptor {
	descriptors := make([]acp.ToolDescriptor, 0, len(r.tools))
	for _, t := range r.tools {
		descriptors = append(descriptors, acp.ToolDescriptor{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].Name < descriptors[j].Name })
	return descriptors
}

func (r *Registry) GetTool(name string) *Tool {
	tool, ok := r.tools[name]
	if !ok {
		return nil
	}
	copy := tool
	return &copy
}

func (r *Registry) GetProviders() []ToolProvider {
	providers := make([]ToolProvider, 0, len(r.providers))
	for _, p := range r.providers {
		providers = append(providers, p)
	}
	return providers
}

func (r *Registry) HasTool(name string) bool {
	_, ok := r.tools[name]
	return ok
}

func (r *Registry) ExecuteTool(toolCall ToolCall) (acp.ToolResult, error) {
	return r.ExecuteToolWithSession(toolCall, "")
}

func (r *Registry) ExecuteToolWithSession(toolCall ToolCall, sessionID string) (acp.ToolResult, error) {
	start := time.Now()
	tool, ok := r.tools[toolCall.Name]
	if !ok {
		return acp.ToolResult{Success: false, Error: "Tool not found: " + toolCall.Name, Metadata: map[string]any{"toolName": toolCall.Name, "duration": 0, "executedAt": time.Now().UTC()}}, nil
	}

	if toolCall.Parameters == nil {
		toolCall.Parameters = map[string]any{}
	}

	if err := validateToolParameters(tool, toolCall.Parameters); err != nil {
		return acp.ToolResult{Success: false, Error: fmt.Sprintf("Invalid parameters for %s: %s", toolCall.Name, err.Error()), Metadata: map[string]any{"toolName": toolCall.Name, "duration": 0, "executedAt": time.Now().UTC()}}, nil
	}

	var toolCallID string
	if sessionID != "" && r.toolCalls != nil {
		locations := extractLocations(toolCall.Parameters)
		report := map[string]any{
			"title":    toolTitle(toolCall.Name, toolCall.Parameters),
			"kind":     toolKind(toolCall.Name),
			"status":   "pending",
			"rawInput": toolCall.Parameters,
		}
		if len(locations) > 0 {
			report["locations"] = locations
		}
		toolCallID = r.toolCalls.ReportToolCall(sessionID, toolCall.Name, report)
		r.toolCalls.UpdateToolCall(sessionID, toolCallID, map[string]any{"status": "in_progress"})
	}

	params := cloneMap(toolCall.Parameters)
	if sessionID != "" {
		params["_sessionId"] = sessionID
	}

	result, err := tool.Handler(params)
	duration := time.Since(start).Milliseconds()
	if err != nil {
		if sessionID != "" && r.toolCalls != nil && toolCallID != "" {
			r.toolCalls.FailToolCall(sessionID, toolCallID, map[string]any{"error": err.Error()})
		}
		return acp.ToolResult{Success: false, Error: err.Error(), Metadata: map[string]any{"toolName": toolCall.Name, "duration": duration, "executedAt": time.Now().UTC(), "toolCallId": toolCallID}}, nil
	}

	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["toolName"] = toolCall.Name
	result.Metadata["duration"] = duration
	result.Metadata["executedAt"] = time.Now().UTC()
	if toolCallID != "" {
		result.Metadata["toolCallId"] = toolCallID
	}

	if sessionID != "" && r.toolCalls != nil && toolCallID != "" {
		if result.Success {
			complete := map[string]any{"rawOutput": result.Result}
			if diffs, ok := result.Metadata["diffs"].([]any); ok {
				complete["content"] = r.toolCalls.ConvertDiffContent(diffs)
			}
			r.toolCalls.CompleteToolCall(sessionID, toolCallID, complete)
		} else {
			r.toolCalls.FailToolCall(sessionID, toolCallID, map[string]any{"error": result.Error, "rawOutput": result.Result})
		}
	}

	return result, nil
}

func (r *Registry) GetCapabilities() map[string]any {
	toolNames := make([]string, 0, len(r.tools))
	for name := range r.tools {
		toolNames = append(toolNames, name)
	}
	providerNames := make([]string, 0, len(r.providers))
	for name := range r.providers {
		providerNames = append(providerNames, name)
	}
	cap := map[string]any{
		"tools":     toolNames,
		"providers": providerNames,
	}
	cap["filesystem"] = r.HasTool("read_file") || r.HasTool("write_file")
	cap["cursor"] = r.HasTool("search_codebase") || r.HasTool("analyze_code")
	return cap
}

func (r *Registry) Metrics() map[string]any {
	providers := make([]string, 0, len(r.providers))
	for name := range r.providers {
		providers = append(providers, name)
	}
	return map[string]any{"totalTools": len(r.tools), "totalProviders": len(r.providers), "enabledProviders": providers}
}

func (r *Registry) ValidateConfiguration() []string {
	errs := make([]string, 0)
	if r.cfg.Tools.Filesystem.Enabled {
		if _, ok := r.providers["filesystem"]; !ok {
			errs = append(errs, "Filesystem tools enabled but filesystem provider not registered")
		}
	}
	if r.cfg.Tools.Terminal.Enabled && r.cfg.Tools.Terminal.MaxProcesses <= 0 {
		errs = append(errs, "Terminal enabled but maxProcesses is invalid")
	}
	if r.cfg.Tools.Cursor.Enabled && !r.HasTool("search_codebase") && !r.HasTool("analyze_code") {
		errs = append(errs, "Cursor tools enabled but not properly registered")
	}
	return errs
}

func (r *Registry) Reload() error {
	for _, provider := range r.providers {
		_ = provider.Cleanup()
	}
	r.providers = map[string]ToolProvider{}
	r.tools = map[string]Tool{}
	r.initializeProviders()
	return nil
}

func (r *Registry) Cleanup() error {
	for _, provider := range r.providers {
		if err := provider.Cleanup(); err != nil {
			r.logger.Warn("Failed to cleanup provider", map[string]any{"provider": provider.Name(), "error": err.Error()})
		}
	}
	return nil
}

func (r *Registry) initializeProviders() {
	if r.cfg.Tools.Cursor.Enabled {
		r.RegisterProvider(NewCursorProvider(r.cfg, r.logger, r.cursorBridge))
	}
}

func validateToolParameters(tool Tool, params map[string]any) error {
	if params == nil {
		return fmt.Errorf("parameters are required and must be an object")
	}
	required, _ := tool.Parameters["required"].([]string)
	if len(required) == 0 {
		if raw, ok := tool.Parameters["required"].([]any); ok {
			required = make([]string, 0, len(raw))
			for _, v := range raw {
				required = append(required, fmt.Sprint(v))
			}
		}
	}
	for _, key := range required {
		value, ok := params[key]
		if !ok || value == nil {
			return fmt.Errorf("Missing required parameter: %s", key)
		}
	}
	return nil
}

func extractLocations(parameters map[string]any) []map[string]any {
	locations := make([]map[string]any, 0)
	if path, ok := parameters["path"]; ok {
		locations = append(locations, map[string]any{"path": path})
	} else if source, ok := parameters["sourcePath"]; ok {
		locations = append(locations, map[string]any{"path": source})
	}
	if dest, ok := parameters["destinationPath"]; ok {
		locations = append(locations, map[string]any{"path": dest})
	} else if dest, ok := parameters["destination"]; ok {
		locations = append(locations, map[string]any{"path": dest})
	}
	if files, ok := parameters["files"].([]any); ok {
		for _, file := range files {
			locations = append(locations, map[string]any{"path": file})
		}
	}
	return locations
}

func toolKind(name string) string {
	kindMap := map[string]string{
		"read_file": "read", "copy_file": "read", "list_directory": "read", "get_file_info": "read",
		"write_file": "edit", "append_file": "edit", "create_file": "edit", "patch_file": "edit", "apply_code_changes": "edit",
		"delete_file": "delete", "remove_file": "delete", "remove_directory": "delete",
		"move_file": "move", "rename_file": "move",
		"search_codebase": "search", "search_files": "search", "grep": "search", "find_files": "search", "find_references": "search", "find_definitions": "search",
		"run_tests": "execute", "run_command": "execute", "execute_command": "execute", "run_script": "execute", "shell": "execute",
		"fetch_url": "fetch", "http_request": "fetch", "download_file": "fetch", "api_request": "fetch", "web_search": "fetch",
		"think": "think", "reason": "think", "plan": "think", "analyze": "think", "explain_code": "think",
		"switch_mode": "switch_mode", "set_mode": "switch_mode", "change_mode": "switch_mode",
		"analyze_code": "read", "get_project_info": "read",
	}
	if kind, ok := kindMap[name]; ok {
		return kind
	}
	return "other"
}

func toolTitle(toolName string, parameters map[string]any) string {
	switch toolName {
	case "read_file":
		return "Reading file: " + str(parameters["path"], "unknown")
	case "write_file":
		return "Writing file: " + str(parameters["path"], "unknown")
	case "list_directory":
		return "Listing directory: " + str(parameters["path"], "unknown")
	case "delete_file", "remove_file":
		return "Deleting file: " + str(parameters["path"], "unknown")
	case "remove_directory":
		return "Removing directory: " + str(parameters["path"], "unknown")
	case "move_file", "rename_file":
		return "Moving file: " + str(parameters["source"], str(parameters["from"], "unknown")) + " -> " + str(parameters["destination"], str(parameters["to"], "unknown"))
	case "search_codebase":
		return "Searching codebase: " + str(parameters["query"], "unknown")
	case "run_tests":
		return "Running tests: " + str(parameters["test_pattern"], "all")
	case "run_command", "execute_command", "shell":
		return "Running: " + str(parameters["command"], "unknown")
	case "analyze", "analyze_code":
		return "Analyzing: " + str(parameters["file_path"], str(parameters["target"], "unknown"))
	case "get_project_info":
		return "Getting project information"
	case "explain_code":
		return "Explaining code: " + str(parameters["file_path"], "unknown")
	default:
		return "Executing tool: " + toolName
	}
}

func str(v any, def string) string {
	if v == nil {
		return def
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" {
		return def
	}
	return s
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
