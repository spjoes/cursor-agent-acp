package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/client"
	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type FilesystemProvider struct {
	cfg    config.Config
	logger *logging.Logger

	clientCapabilities map[string]any
	fsClient           client.FileSystemClient
}

func NewFilesystemProvider(cfg config.Config, logger *logging.Logger, clientCapabilities map[string]any, fsClient client.FileSystemClient) *FilesystemProvider {
	return &FilesystemProvider{cfg: cfg, logger: logger, clientCapabilities: clientCapabilities, fsClient: fsClient}
}

func (p *FilesystemProvider) Name() string {
	return "filesystem"
}

func (p *FilesystemProvider) Description() string {
	return "File system operations via ACP client methods (read/write text files)"
}

func (p *FilesystemProvider) GetTools() []Tool {
	if !p.cfg.Tools.Filesystem.Enabled {
		return nil
	}

	fsCaps, _ := p.clientCapabilities["fs"].(map[string]any)
	if fsCaps == nil {
		p.logger.Warn("Client capabilities not yet initialized - filesystem tools unavailable", nil)
		return nil
	}

	tools := make([]Tool, 0)
	if capabilityBool(fsCaps, "readTextFile") {
		tools = append(tools, Tool{
			Name:        "read_file",
			Description: "Read a text file from the client workspace (includes unsaved changes in editor). Can read full file or specific line ranges for efficiency.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "description": "Absolute path to the file to read (relative to client workspace)"},
					"line":  map[string]any{"type": "number", "description": "Optional: Start reading from this line number (1-based)."},
					"limit": map[string]any{"type": "number", "description": "Optional: Maximum number of lines to read."},
				},
				"required": []string{"path"},
			},
			Handler: p.readFile,
		})
	}
	if capabilityBool(fsCaps, "writeTextFile") {
		tools = append(tools, Tool{
			Name:        "write_file",
			Description: "Write content to a text file in the client workspace. Client handles directory creation and permissions.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Absolute path to the file to write (relative to client workspace)"},
					"content": map[string]any{"type": "string", "description": "Content to write to the file"},
				},
				"required": []string{"path", "content"},
			},
			Handler: p.writeFile,
		})
	}

	return tools
}

func (p *FilesystemProvider) Cleanup() error { return nil }

func (p *FilesystemProvider) readFile(params map[string]any) (acp.ToolResult, error) {
	maxRetries := 3
	retryDelay := 1 * time.Second

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(retryDelay * time.Duration(attempt))
		}
		result, err := p.readFileOnce(params)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if isValidationError(err) || isPermissionError(err) || isFileNotFound(err) {
			break
		}
	}
	return acp.ToolResult{Success: false, Error: lastErr.Error()}, nil
}

func (p *FilesystemProvider) readFileOnce(params map[string]any) (acp.ToolResult, error) {
	sessionID := getString(params, "_sessionId")
	if sessionID == "" {
		return acp.ToolResult{}, fmt.Errorf("Session ID is required for ACP file operations. This is an internal error - please report it.")
	}
	path, err := nonEmptyStringParam(params, "path")
	if err != nil {
		return acp.ToolResult{}, err
	}
	line, hasLine := intParam(params, "line")
	if hasLine && line < 1 {
		return acp.ToolResult{}, fmt.Errorf("Line number must be a positive integer (1-based)")
	}
	limit, hasLimit := intParam(params, "limit")
	if hasLimit && limit < 1 {
		return acp.ToolResult{}, fmt.Errorf("Limit must be a positive integer")
	}

	content, err := p.fsClient.ReadTextFile(client.ReadFileOptions{
		SessionID: sessionID,
		Path:      path,
		Line:      line,
		Limit:     limit,
	})
	if err != nil {
		return acp.ToolResult{}, err
	}

	meta := map[string]any{
		"contentLength":          len(content),
		"lineCount":              lineCount(content),
		"source":                 "acp-client",
		"includesUnsavedChanges": true,
		"acpMethod":              "fs/read_text_file",
		"sessionId":              sessionID,
	}
	if hasLine {
		meta["startLine"] = line
	}
	if hasLimit {
		meta["maxLines"] = limit
	}

	return acp.ToolResult{
		Success: true,
		Result: map[string]any{
			"path":    path,
			"content": content,
			"_meta":   meta,
		},
	}, nil
}

func (p *FilesystemProvider) writeFile(params map[string]any) (acp.ToolResult, error) {
	maxRetries := 3
	retryDelay := 1 * time.Second

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(retryDelay * time.Duration(attempt))
		}
		result, err := p.writeFileOnce(params)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if isValidationError(err) || isPermissionError(err) {
			break
		}
	}
	return acp.ToolResult{Success: false, Error: lastErr.Error()}, nil
}

func (p *FilesystemProvider) writeFileOnce(params map[string]any) (acp.ToolResult, error) {
	sessionID := getString(params, "_sessionId")
	if sessionID == "" {
		return acp.ToolResult{}, fmt.Errorf("Session ID is required for ACP file operations. This is an internal error - please report it.")
	}
	path, err := nonEmptyStringParam(params, "path")
	if err != nil {
		return acp.ToolResult{}, err
	}
	content, err := contentStringParam(params, "content")
	if err != nil {
		return acp.ToolResult{}, err
	}

	if err := p.fsClient.WriteTextFile(client.WriteFileOptions{SessionID: sessionID, Path: path, Content: content}); err != nil {
		return acp.ToolResult{}, err
	}

	return acp.ToolResult{
		Success: true,
		Result: map[string]any{
			"path":    path,
			"written": true,
			"_meta": map[string]any{
				"contentLength": len(content),
				"lineCount":     lineCount(content),
				"source":        "acp-client",
				"acpMethod":     "fs/write_text_file",
				"sessionId":     sessionID,
			},
		},
	}, nil
}

func capabilityBool(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func intParam(params map[string]any, key string) (int, bool) {
	v, ok := params[key]
	if !ok || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case float32:
		return int(x), true
	default:
		return 0, false
	}
}

func lineCount(s string) int {
	n := 1
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}

func nonEmptyStringParam(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", fmt.Errorf("Valid file path is required. Path must be a non-empty string.")
	}
	str, ok := v.(string)
	if !ok || str == "" {
		return "", fmt.Errorf("Valid file path is required. Path must be a non-empty string.")
	}
	return str, nil
}

func contentStringParam(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return "", fmt.Errorf("Content is required. To create an empty file, pass an empty string.")
	}
	if str, ok := v.(string); ok {
		return str, nil
	}
	return fmt.Sprint(v), nil
}

func isValidationError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "required") || strings.Contains(msg, "invalid") || strings.Contains(msg, "must")
}

func isPermissionError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "permission") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "access denied") || strings.Contains(msg, "not allowed")
}

func isFileNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "enoent")
}
