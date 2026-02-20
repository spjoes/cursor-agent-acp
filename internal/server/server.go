package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/client"
	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/cursor"
	"github.com/spjoes/cursor-agent-acp/internal/errorfmt"
	"github.com/spjoes/cursor-agent-acp/internal/extensions"
	"github.com/spjoes/cursor-agent-acp/internal/jsonrpc"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
	"github.com/spjoes/cursor-agent-acp/internal/permissions"
	"github.com/spjoes/cursor-agent-acp/internal/prompt"
	"github.com/spjoes/cursor-agent-acp/internal/session"
	"github.com/spjoes/cursor-agent-acp/internal/slash"
	"github.com/spjoes/cursor-agent-acp/internal/toolcall"
	"github.com/spjoes/cursor-agent-acp/internal/tools"
)

const (
	AdapterName    = "cursor-agent-acp"
	AdapterTitle   = "Cursor Agent ACP Adapter"
	AdapterVersion = "0.7.1-go"
)

type Status struct {
	Running    bool            `json:"running"`
	UptimeMs   int64           `json:"uptimeMs,omitempty"`
	Components map[string]bool `json:"components"`
}

type clientRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpc.Error  `json:"error,omitempty"`
}

type Server struct {
	cfg    config.Config
	logger *logging.Logger

	sessions    *session.Manager
	cursor      *cursor.Bridge
	extensions  *extensions.Registry
	slash       *slash.Registry
	permissions *permissions.Handler
	toolCalls   *toolcall.Manager
	fsClient    *client.ACPFileSystemClient
	tools       *tools.Registry
	prompt      *prompt.Handler

	stdoutMu sync.Mutex
	stdout   io.Writer

	startTime time.Time
	running   bool

	clientCapabilities map[string]any

	pendingMu        sync.Mutex
	pendingClientRPC map[string]chan clientRPCResponse
	clientRPCSeq     uint64
}

var (
	nodeVersionOnce sync.Once
	nodeVersion     string
)

func New(cfg config.Config, logger *logging.Logger) *Server {
	s := &Server{
		cfg:              cfg,
		logger:           logger,
		stdout:           os.Stdout,
		pendingClientRPC: map[string]chan clientRPCResponse{},
	}
	s.sessions = session.NewManager(cfg, logger)
	s.cursor = cursor.NewBridge(cfg, logger)
	s.extensions = extensions.NewRegistry(logger)
	s.slash = slash.NewRegistry(logger)
	s.permissions = permissions.NewHandler(logger)
	s.toolCalls = toolcall.NewManager(
		logger,
		func(notification map[string]any) { s.writeMessage(notification) },
		func(params permissions.RequestPermissionParams) permissions.PermissionOutcome {
			return defaultPermissionOutcome(params.Options)
		},
	)
	s.tools = tools.NewRegistry(cfg, logger, s.cursor)
	s.tools.SetToolCallManager(s.toolCalls)
	s.fsClient = client.NewACPFileSystemClient(s, logger)
	s.prompt = prompt.NewHandler(s.sessions, s.cursor, logger, s.sendNotification, s.slash)

	s.registerDefaultCommands()
	s.slash.OnChange(func(_ []slash.AvailableCommand) {
		sessions, _, _, err := s.sessions.ListSessions(1000, 0, nil)
		if err != nil {
			s.logger.Warn("failed to list sessions for slash update", map[string]any{"error": err.Error()})
			return
		}
		for _, sess := range sessions {
			s.sendAvailableCommandsUpdate(sess.ID)
		}
	})

	return s
}

func (s *Server) Initialize() error {
	if err := config.EnsureSessionDir(s.cfg); err != nil {
		return err
	}

	s.sessions.LoadModelsFromProvider(s.cursor)
	s.refreshModelCommand()
	if version, err := s.cursor.GetVersion(); err != nil {
		s.logger.Warn("cursor-agent CLI not available", map[string]any{"error": err.Error()})
	} else {
		s.logger.Info("cursor-agent CLI detected", map[string]any{"version": version})
		status := s.cursor.CheckAuthentication()
		if !status.Authenticated {
			s.logger.Warn("cursor-agent not authenticated", map[string]any{"error": status.Error})
		}
	}

	s.running = true
	s.startTime = time.Now().UTC()
	return nil
}

func (s *Server) Close() {
	s.running = false
	if s.prompt != nil {
		s.prompt.Close()
	}
	if s.toolCalls != nil {
		s.toolCalls.Cleanup()
	}
	if s.permissions != nil {
		s.permissions.Cleanup()
	}
	if s.tools != nil {
		_ = s.tools.Cleanup()
	}
	if s.cursor != nil {
		_ = s.cursor.Close()
	}
	if s.extensions != nil {
		s.extensions.Clear()
	}
	if s.slash != nil {
		s.slash.Clear()
	}
	if s.sessions != nil {
		s.sessions.Close()
	}
}

func (s *Server) Status() Status {
	status := Status{
		Running: s.running,
		Components: map[string]bool{
			"sessionManager":    s.sessions != nil,
			"cursorBridge":      s.cursor != nil,
			"extensionRegistry": s.extensions != nil,
			"slashRegistry":     s.slash != nil,
			"permissions":       s.permissions != nil,
			"toolCallManager":   s.toolCalls != nil,
			"toolRegistry":      s.tools != nil,
			"promptHandler":     s.prompt != nil,
		},
	}
	if !s.startTime.IsZero() {
		status.UptimeMs = time.Since(s.startTime).Milliseconds()
	}
	return status
}

func (s *Server) StartStdio(ctx context.Context) error {
	s.logger.Info("Starting ACP adapter with stdio transport", nil)

	scanner := bufio.NewScanner(os.Stdin)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	var inflight sync.WaitGroup

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			inflight.Wait()
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			resp := jsonrpc.Failure(nil, jsonrpc.ParseError, "Parse error", map[string]any{"error": err.Error()})
			s.writeMessage(resp)
			continue
		}

		if _, ok := envelope["method"]; ok {
			var req jsonrpc.Request
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				resp := jsonrpc.Failure(nil, jsonrpc.InvalidRequest, "Invalid request", map[string]any{"error": err.Error()})
				s.writeMessage(resp)
				continue
			}
			inflight.Add(1)
			go func(request jsonrpc.Request) {
				defer inflight.Done()
				resp, postResponse := s.processRequest(ctx, request)
				if request.IsNotification() {
					return
				}
				s.writeMessage(resp)
				if postResponse != nil {
					postResponse()
				}
			}(req)
			continue
		}

		if _, ok := envelope["id"]; ok {
			var resp clientRPCResponse
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				s.logger.Warn("Failed to decode client RPC response", map[string]any{"error": err.Error()})
				continue
			}
			s.handleClientRPCResponse(resp)
			continue
		}

		s.logger.Warn("Ignoring JSON-RPC message without method or id", map[string]any{"line": line})
	}

	if err := scanner.Err(); err != nil {
		inflight.Wait()
		return err
	}
	inflight.Wait()
	return nil
}

func (s *Server) ProcessRequest(ctx context.Context, req jsonrpc.Request) jsonrpc.Response {
	resp, postResponse := s.processRequest(ctx, req)
	if postResponse != nil && !req.IsNotification() {
		go postResponse()
	}
	return resp
}

func (s *Server) processRequest(ctx context.Context, req jsonrpc.Request) (jsonrpc.Response, func()) {
	if req.JSONRPC != jsonrpc.Version {
		return jsonrpc.Failure(req.ID, jsonrpc.InvalidRequest, "Invalid JSON-RPC version", nil), nil
	}
	if strings.TrimSpace(req.Method) == "" {
		return jsonrpc.Failure(req.ID, jsonrpc.InvalidRequest, "Method is required", nil), nil
	}

	var result any
	var err error
	var postResponse func()

	s.logger.Debug("Processing request", map[string]any{"method": req.Method, "id": req.ID})

	switch req.Method {
	case "initialize":
		result, err = s.handleInitialize(req.Params)
	case "session/new":
		var newResponse acp.NewSessionResponse
		newResponse, err = s.handleSessionNew(ctx, req.Params)
		result = newResponse
		if err == nil {
			sessionID := strings.TrimSpace(newResponse.SessionID)
			if sessionID != "" {
				postResponse = func() { s.sendAvailableCommandsUpdate(sessionID) }
			}
		}
	case "session/load":
		var loadResponse acp.LoadSessionResponse
		loadResponse, err = s.handleSessionLoad(ctx, req.Params)
		result = loadResponse
		if err == nil {
			params, derr := decodeParams[acp.LoadSessionRequest](req.Params)
			if derr == nil {
				sessionID := strings.TrimSpace(params.SessionID)
				if sessionID != "" {
					postResponse = func() { s.sendAvailableCommandsUpdate(sessionID) }
				}
			}
		}
	case "session/list":
		result, err = s.handleSessionList(req.Params)
	case "session/update":
		result, err = s.handleSessionUpdate(req.Params)
	case "session/delete":
		result, err = s.handleSessionDelete(req.Params)
	case "session/set_mode":
		result, err = s.handleSetSessionMode(req.Params)
	case "session/set_model":
		result, err = s.handleSetSessionModel(req.Params)
	case "session/prompt":
		result, err = s.handleSessionPrompt(ctx, req)
	case "session/cancel":
		result, err = s.handleSessionCancel(req, req.Params)
	case "session/request_permission":
		result, err = s.handleRequestPermission(req)
	case "tools/list":
		result, err = s.handleToolsList()
	case "tools/call":
		result, err = s.handleToolCall(ctx, req.ID, req.Params)
	default:
		if strings.HasPrefix(req.Method, "_") {
			params, derr := decodeObjectParams(req.Params)
			if derr != nil {
				return jsonrpc.Failure(req.ID, jsonrpc.InvalidParams, derr.Error(), nil), nil
			}
			if req.IsNotification() {
				s.extensions.SendNotification(req.Method, params)
				return jsonrpc.Success(req.ID, nil), nil
			}
			if !s.extensions.HasMethod(req.Method) {
				return jsonrpc.Failure(req.ID, jsonrpc.MethodNotFound, "Method not found", nil), nil
			}
			result, err = s.extensions.CallMethod(req.Method, params)
			break
		}
		return jsonrpc.Failure(req.ID, jsonrpc.MethodNotFound, "Unknown method: "+req.Method, nil), nil
	}

	if err != nil {
		formatted := errorfmt.Format(err, "internal error", map[string]any{"name": fmt.Sprintf("%T", err)})
		return jsonrpc.Failure(req.ID, formatted.Code, formatted.Message, formatted.Data), nil
	}
	return jsonrpc.Success(req.ID, result), postResponse
}

func (s *Server) handleInitialize(raw json.RawMessage) (acp.InitializeResponse, error) {
	initializeStart := time.Now().UTC()
	params, err := decodeParams[acp.InitializeRequest](raw)
	if err != nil {
		return acp.InitializeResponse{}, err
	}
	if params.ProtocolVersion == 0 {
		return acp.InitializeResponse{}, fmt.Errorf("Protocol version is required in initialize request. This agent supports versions: 1. Please specify \"protocolVersion\" in your request.")
	}

	agreed := 1
	if params.ProtocolVersion == 1 {
		agreed = 1
	}

	s.clientCapabilities = params.ClientCapabilities
	s.tools.ConfigureFilesystemProvider(s.clientCapabilities, s.fsClient)

	connectivitySuccess := false
	cursorVersion := any(nil)
	cursorAuthenticated := false
	cursorError := ""
	if version, err := s.cursor.GetVersion(); err == nil {
		status := s.cursor.CheckAuthentication()
		connectivitySuccess = true
		cursorVersion = version
		cursorAuthenticated = status.Authenticated
		cursorError = status.Error
	}
	cursorAvailable := connectivitySuccess && cursorAuthenticated

	capabilities := map[string]any{
		"loadSession": true,
		"promptCapabilities": map[string]any{
			"image":           cursorAvailable,
			"audio":           false,
			"embeddedContext": cursorAvailable,
		},
		"mcpCapabilities": map[string]any{
			"http": false,
			"sse":  false,
		},
		"sessionCapabilities": map[string]any{
			"_meta": map[string]any{
				"supportsSessionModes": true,
				"supportsSetMode":      true,
				"supportsSetModel":     true,
			},
		},
		"_meta": map[string]any{
			"streaming":       cursorAvailable,
			"toolCalling":     cursorAvailable,
			"fileSystem":      s.cfg.Tools.Filesystem.Enabled,
			"terminal":        s.cfg.Tools.Terminal.Enabled,
			"cursorAvailable": cursorAvailable,
			"cursorVersion":   cursorVersion,
			"description":     "Production-ready ACP adapter for Cursor CLI",
			"implementation":  "cursor-agent-acp",
			"repositoryUrl":   "https://github.com/spjoes/cursor-agent-acp",
		},
	}
	metaCaps, _ := capabilities["_meta"].(map[string]any)
	if metaCaps == nil {
		metaCaps = map[string]any{}
		capabilities["_meta"] = metaCaps
	}
	for k, v := range s.buildExtensionCapabilities() {
		metaCaps[k] = v
	}

	cursorCLIStatus := "unavailable"
	if connectivitySuccess {
		cursorCLIStatus = "available"
	}

	meta := map[string]any{
		"initializationTime":       initializeStart.Format(time.RFC3339),
		"initializationDurationMs": time.Since(initializeStart).Milliseconds(),
		"cursorCliStatus":          cursorCLIStatus,
		"cursorVersion":            cursorVersion,
		"cursorAuthenticated":      cursorAuthenticated,
		"nodeVersion":              resolvedNodeVersion(),
		"platform":                 runtime.GOOS,
		"arch":                     runtime.GOARCH,
		"toolsEnabled": map[string]any{
			"filesystem": s.cfg.Tools.Filesystem.Enabled,
			"terminal":   s.cfg.Tools.Terminal.Enabled,
		},
		"versionNegotiation": map[string]any{
			"clientRequested": params.ProtocolVersion,
			"agentResponded":  agreed,
			"agentSupports":   []int{1},
		},
		"implementation": "cursor-agent-acp",
	}

	if !connectivitySuccess {
		guidance := map[string]any{
			"issue": "cursor-agent CLI not available",
		}
		if strings.TrimSpace(cursorError) != "" {
			guidance["issue"] = cursorError
		}
		lower := strings.ToLower(cursorError)
		if strings.Contains(lower, "not installed") || strings.Contains(lower, "not found") || strings.Contains(lower, "enoent") || strings.Contains(lower, "command not found") {
			guidance["resolution"] = "Install cursor-agent CLI: https://cursor.sh/docs/agent"
		}
		meta["cursorCliGuidance"] = guidance
	} else if !cursorAuthenticated {
		issue := "User not authenticated"
		if strings.TrimSpace(cursorError) != "" {
			issue = cursorError
		}
		meta["cursorCliGuidance"] = map[string]any{
			"issue":      issue,
			"resolution": "Run: cursor-agent login",
		}
	}

	resp := acp.InitializeResponse{
		ProtocolVersion:   agreed,
		AgentCapabilities: capabilities,
		AgentInfo: acp.Implementation{
			Name:    AdapterName,
			Title:   AdapterTitle,
			Version: AdapterVersion,
		},
		AuthMethods: []map[string]any{},
		Meta:        meta,
	}
	return resp, nil
}

func resolvedNodeVersion() string {
	nodeVersionOnce.Do(func() {
		nodeVersion = runtime.Version()

		out, err := exec.Command("node", "--version").Output()
		if err != nil {
			return
		}

		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" {
			nodeVersion = trimmed
		}
	})
	return nodeVersion
}

func (s *Server) handleSessionNew(ctx context.Context, raw json.RawMessage) (acp.NewSessionResponse, error) {
	params, err := decodeParams[acp.NewSessionRequest](raw)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	if strings.TrimSpace(params.Cwd) == "" {
		return acp.NewSessionResponse{}, fmt.Errorf("cwd (working directory) is required and must be a non-empty string")
	}
	if !isAbsPath(params.Cwd) {
		return acp.NewSessionResponse{}, fmt.Errorf("cwd must be an absolute path (per ACP spec)")
	}
	if params.McpServers == nil {
		return acp.NewSessionResponse{}, fmt.Errorf("mcpServers is required and must be an array (can be empty)")
	}

	metadata := cloneMap(params.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["cwd"] = params.Cwd
	metadata["mcpServers"] = params.McpServers

	if chatID, err := s.cursor.CreateChat(ctx); err == nil && chatID != "" {
		metadata["cursorChatId"] = chatID
	}

	sessionData, err := s.sessions.CreateSession(metadata)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}

	meta := map[string]any{
		"createdAt":      sessionData.CreatedAt.Format(time.RFC3339),
		"cwd":            params.Cwd,
		"mcpServerCount": len(params.McpServers),
	}
	if len(params.McpServers) > 0 {
		servers := make([]map[string]any, 0, len(params.McpServers))
		for _, rawServer := range params.McpServers {
			name := strings.TrimSpace(fmt.Sprint(rawServer["name"]))
			if name == "" || name == "<nil>" {
				name = "unnamed"
			}
			serverType := strings.TrimSpace(fmt.Sprint(rawServer["type"]))
			if serverType == "" || serverType == "<nil>" {
				serverType = "unknown"
			}
			servers = append(servers, map[string]any{
				"name":   name,
				"type":   serverType,
				"status": "pending-implementation",
			})
		}
		meta["mcpStatus"] = "not-implemented"
		meta["mcpServers"] = servers
	}
	resp := acp.NewSessionResponse{
		SessionID: sessionData.ID,
		Modes:     s.sessions.GetSessionModeState(sessionData.ID),
		Models:    s.sessions.GetSessionModelState(sessionData.ID),
		Meta:      meta,
	}
	return resp, nil
}

func (s *Server) handleSessionLoad(ctx context.Context, raw json.RawMessage) (acp.LoadSessionResponse, error) {
	params, err := decodeParams[acp.LoadSessionRequest](raw)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if strings.TrimSpace(params.SessionID) == "" {
		return acp.LoadSessionResponse{}, fmt.Errorf("sessionId is required")
	}
	if strings.TrimSpace(params.Cwd) == "" {
		return acp.LoadSessionResponse{}, fmt.Errorf("cwd (working directory) is required and must be a non-empty string")
	}
	if !isAbsPath(params.Cwd) {
		return acp.LoadSessionResponse{}, fmt.Errorf("cwd must be an absolute path (per ACP spec)")
	}
	if params.McpServers == nil {
		return acp.LoadSessionResponse{}, fmt.Errorf("mcpServers is required and must be an array (can be empty)")
	}

	sessionData, err := s.sessions.LoadSession(params.SessionID)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	_, err = s.sessions.UpdateSession(params.SessionID, mergeMaps(params.Metadata, map[string]any{"cwd": params.Cwd, "mcpServers": params.McpServers}))
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}

	for _, msg := range sessionData.Conversation {
		updateType := ""
		if msg.Role == "user" {
			updateType = "user_message_chunk"
		} else if msg.Role == "assistant" || msg.Role == "system" {
			updateType = "agent_message_chunk"
		} else {
			continue
		}
		for _, block := range msg.Content {
			s.sendNotification("session/update", map[string]any{
				"sessionId": params.SessionID,
				"update": map[string]any{
					"sessionUpdate": updateType,
					"content":       block,
				},
			})
		}
	}

	resp := acp.LoadSessionResponse{
		Modes:  s.sessions.GetSessionModeState(params.SessionID),
		Models: s.sessions.GetSessionModelState(params.SessionID),
		Meta: map[string]any{
			"sessionId":      sessionData.ID,
			"loadedAt":       time.Now().UTC().Format(time.RFC3339),
			"messageCount":   sessionData.State.MessageCount,
			"lastActivity":   sessionData.State.LastActivity.Format(time.RFC3339),
			"cwd":            params.Cwd,
			"mcpServerCount": len(params.McpServers),
		},
	}
	return resp, nil
}

func (s *Server) handleSetSessionMode(raw json.RawMessage) (acp.SetSessionModeResponse, error) {
	params, err := decodeParams[acp.SetSessionModeRequest](raw)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	if params.SessionID == "" || params.ModeID == "" {
		if params.SessionID == "" {
			return acp.SetSessionModeResponse{}, fmt.Errorf("sessionId is required")
		}
		return acp.SetSessionModeResponse{}, fmt.Errorf("modeId is required")
	}
	prev, err := s.sessions.SetSessionMode(params.SessionID, params.ModeID)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}

	return acp.SetSessionModeResponse{Meta: map[string]any{
		"previousMode": prev,
		"newMode":      params.ModeID,
		"changedAt":    time.Now().UTC().Format(time.RFC3339),
	}}, nil
}

func (s *Server) handleSetSessionModel(raw json.RawMessage) (acp.SetSessionModelResponse, error) {
	params, err := decodeParams[acp.SetSessionModelRequest](raw)
	if err != nil {
		return acp.SetSessionModelResponse{}, err
	}
	if params.SessionID == "" || params.ModelID == "" {
		if params.SessionID == "" {
			return acp.SetSessionModelResponse{}, fmt.Errorf("sessionId is required")
		}
		return acp.SetSessionModelResponse{}, fmt.Errorf("modelId is required")
	}
	prev, err := s.sessions.SetSessionModel(params.SessionID, params.ModelID)
	if err != nil {
		return acp.SetSessionModelResponse{}, err
	}
	return acp.SetSessionModelResponse{Meta: map[string]any{
		"previousModel": prev,
		"newModel":      params.ModelID,
		"changedAt":     time.Now().UTC().Format(time.RFC3339),
	}}, nil
}

func (s *Server) handleSessionList(raw json.RawMessage) (acp.ListSessionsResponse, error) {
	params, err := decodeParams[acp.ListSessionsRequest](raw)
	if err != nil {
		return acp.ListSessionsResponse{}, err
	}
	items, total, hasMore, err := s.sessions.ListSessions(params.Limit, params.Offset, params.Filter)
	if err != nil {
		return acp.ListSessionsResponse{}, err
	}
	return acp.ListSessionsResponse{Sessions: items, Total: total, HasMore: hasMore}, nil
}

func (s *Server) handleSessionUpdate(raw json.RawMessage) (map[string]any, error) {
	params, err := decodeParams[acp.UpdateSessionRequest](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	_, err = s.sessions.UpdateSession(params.SessionID, params.Metadata)
	if err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": params.SessionID, "updated": true}, nil
}

func (s *Server) handleSessionDelete(raw json.RawMessage) (map[string]any, error) {
	params, err := decodeParams[acp.DeleteSessionRequest](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	if err := s.sessions.DeleteSession(params.SessionID); err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": params.SessionID, "deleted": true}, nil
}

func (s *Server) handleSessionPrompt(ctx context.Context, req jsonrpc.Request) (acp.PromptResponse, error) {
	params, err := decodeParams[acp.PromptRequest](req.Params)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	requestID := ""
	if req.ID != nil {
		requestID = fmt.Sprint(req.ID)
	}
	return s.prompt.ProcessWithRequestID(ctx, params, requestID)
}

func (s *Server) handleSessionCancel(req jsonrpc.Request, raw json.RawMessage) (any, error) {
	params, err := decodeParams[acp.CancelNotification](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required and must be a string")
	}
	if strings.TrimSpace(params.RequestID) != "" {
		s.prompt.CancelStream(params.RequestID)
	}
	s.prompt.CancelSession(params.SessionID)
	s.toolCalls.CancelSessionToolCalls(params.SessionID)
	s.permissions.CancelSessionPermissionRequests(params.SessionID)

	if req.IsNotification() {
		return nil, nil
	}
	return nil, nil
}

func (s *Server) handleToolsList() (acp.ToolsListResponse, error) {
	return acp.ToolsListResponse{Tools: s.tools.ToolDescriptors()}, nil
}

func (s *Server) handleToolCall(_ context.Context, reqID any, raw json.RawMessage) (any, error) {
	params, err := decodeParams[acp.ToolCallRequest](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.Name) == "" {
		return nil, fmt.Errorf("tool name is required")
	}
	sessionID := extractSessionID(params.Parameters)
	result, err := s.tools.ExecuteToolWithSession(
		tools.ToolCall{
			ID:         fmt.Sprint(reqID),
			Name:       params.Name,
			Parameters: params.Parameters,
		},
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return nil, errors.New(result.Error)
	}
	return result, nil
}

func (s *Server) handleRequestPermission(req jsonrpc.Request) (any, error) {
	resp, err := s.permissions.HandlePermissionRequest(req)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (s *Server) sendAvailableCommandsUpdate(sessionID string) {
	if !s.sessions.HasSession(sessionID) {
		return
	}
	commands := s.slash.GetCommands()
	if len(commands) == 0 {
		return
	}

	s.sendNotification("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate":     "available_commands_update",
			"availableCommands": commands,
		},
		"_meta": map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339)},
	})
}

func (s *Server) sendNotification(method string, params any) {
	message := map[string]any{
		"jsonrpc": jsonrpc.Version,
		"method":  method,
		"params":  params,
	}
	s.writeMessage(message)
}

func (s *Server) registerDefaultCommands() {
	_ = s.slash.RegisterCommand("plan", "Create a detailed implementation plan", "description of what to plan")
	s.refreshModelCommand()
}

func (s *Server) refreshModelCommand() {
	models := s.sessions.GetAvailableModels()
	modelNames := make([]string, 0, len(models))
	for _, model := range models {
		modelNames = append(modelNames, model.ID)
	}
	description := "Switch to a different model. Available: " + strings.Join(modelNames, ", ")
	if existing := s.slash.GetCommand("model"); existing != nil {
		if existing.Description == description && existing.Input != nil && existing.Input.Hint == "model-id" {
			return
		}
	}
	_ = s.slash.RegisterCommand("model", description, "model-id")
}

func (s *Server) buildExtensionCapabilities() map[string]any {
	if s.extensions == nil {
		return map[string]any{}
	}
	methods := s.extensions.RegisteredMethods()
	notifications := s.extensions.RegisteredNotifications()
	if len(methods) == 0 && len(notifications) == 0 {
		return map[string]any{}
	}

	namespaces := map[string]map[string][]string{}
	for _, method := range methods {
		ns := extensionNamespace(method)
		entry := namespaces[ns]
		if entry == nil {
			entry = map[string][]string{}
			namespaces[ns] = entry
		}
		entry["methods"] = append(entry["methods"], method)
	}
	for _, notification := range notifications {
		ns := extensionNamespace(notification)
		entry := namespaces[ns]
		if entry == nil {
			entry = map[string][]string{}
			namespaces[ns] = entry
		}
		entry["notifications"] = append(entry["notifications"], notification)
	}

	out := map[string]any{}
	for ns, entry := range namespaces {
		cap := map[string]any{}
		if len(entry["methods"]) > 0 {
			cap["methods"] = entry["methods"]
		}
		if len(entry["notifications"]) > 0 {
			cap["notifications"] = entry["notifications"]
		}
		out[ns] = cap
	}
	return out
}

func extensionNamespace(name string) string {
	name = strings.TrimPrefix(name, "_")
	if name == "" {
		return "default"
	}
	parts := strings.SplitN(name, "/", 2)
	return parts[0]
}

func (s *Server) ReadTextFile(params client.ReadTextFileRequest) (client.ReadTextFileResponse, error) {
	if strings.TrimSpace(params.SessionID) == "" {
		return client.ReadTextFileResponse{}, fmt.Errorf("sessionId is required and must be a string")
	}
	if strings.TrimSpace(params.Path) == "" {
		return client.ReadTextFileResponse{}, fmt.Errorf("path is required and must be a string")
	}
	if params.Line < 0 {
		return client.ReadTextFileResponse{}, fmt.Errorf("line must be a positive integer (1-based)")
	}
	if params.Limit < 0 {
		return client.ReadTextFileResponse{}, fmt.Errorf("limit must be a positive integer")
	}

	result, err := s.callClient(context.Background(), "fs/read_text_file", params)
	if err != nil {
		return client.ReadTextFileResponse{}, err
	}
	var response client.ReadTextFileResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return client.ReadTextFileResponse{}, fmt.Errorf("invalid fs/read_text_file response: %w", err)
	}
	if response.Content == "" && string(result) != `{"content":""}` && string(result) != "{}" {
		// For ACP compatibility, only enforce type shape.
		var shape map[string]any
		if err := json.Unmarshal(result, &shape); err != nil {
			return client.ReadTextFileResponse{}, fmt.Errorf("invalid fs/read_text_file response: %w", err)
		}
		if _, ok := shape["content"]; !ok {
			return client.ReadTextFileResponse{}, fmt.Errorf("invalid fs/read_text_file response: content is required")
		}
	}
	return response, nil
}

func (s *Server) WriteTextFile(params client.WriteTextFileRequest) (client.WriteTextFileResponse, error) {
	if strings.TrimSpace(params.SessionID) == "" {
		return client.WriteTextFileResponse{}, fmt.Errorf("sessionId is required and must be a string")
	}
	if strings.TrimSpace(params.Path) == "" {
		return client.WriteTextFileResponse{}, fmt.Errorf("path is required and must be a string")
	}

	result, err := s.callClient(context.Background(), "fs/write_text_file", params)
	if err != nil {
		return client.WriteTextFileResponse{}, err
	}
	if len(result) == 0 || string(result) == "null" {
		return client.WriteTextFileResponse{}, nil
	}

	var response client.WriteTextFileResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return client.WriteTextFileResponse{}, fmt.Errorf("invalid fs/write_text_file response: %w", err)
	}
	return response, nil
}

func (s *Server) CreateTerminal(params client.CreateTerminalRequest) (client.CreateTerminalResponse, error) {
	if strings.TrimSpace(params.SessionID) == "" {
		return client.CreateTerminalResponse{}, fmt.Errorf("sessionId is required and must be a string")
	}
	if strings.TrimSpace(params.Command) == "" {
		return client.CreateTerminalResponse{}, fmt.Errorf("command is required and must be a string")
	}

	result, err := s.callClient(context.Background(), "terminal/create", params)
	if err != nil {
		return client.CreateTerminalResponse{}, err
	}
	var response client.CreateTerminalResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return client.CreateTerminalResponse{}, fmt.Errorf("invalid terminal/create response: %w", err)
	}
	if strings.TrimSpace(response.TerminalID) == "" {
		var shape map[string]any
		if err := json.Unmarshal(result, &shape); err == nil {
			if id, ok := shape["id"].(string); ok {
				response.TerminalID = id
			}
		}
	}
	if strings.TrimSpace(response.TerminalID) == "" {
		return client.CreateTerminalResponse{}, fmt.Errorf("invalid terminal/create response: terminalId is required")
	}
	return response, nil
}

func (s *Server) GetTerminalOutput(params client.TerminalOutputRequest) (client.TerminalOutputResponse, error) {
	if strings.TrimSpace(params.TerminalID) == "" {
		return client.TerminalOutputResponse{}, fmt.Errorf("terminalId is required and must be a string")
	}

	result, err := s.callClient(context.Background(), "terminal/output", params)
	if err != nil {
		return client.TerminalOutputResponse{}, err
	}
	var response client.TerminalOutputResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return client.TerminalOutputResponse{}, fmt.Errorf("invalid terminal/output response: %w", err)
	}
	return response, nil
}

func (s *Server) WaitForTerminalExit(params client.WaitForTerminalExitRequest) (client.WaitForTerminalExitResponse, error) {
	if strings.TrimSpace(params.TerminalID) == "" {
		return client.WaitForTerminalExitResponse{}, fmt.Errorf("terminalId is required and must be a string")
	}

	result, err := s.callClient(context.Background(), "terminal/wait_for_exit", params)
	if err != nil {
		return client.WaitForTerminalExitResponse{}, err
	}
	var response client.WaitForTerminalExitResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return client.WaitForTerminalExitResponse{}, fmt.Errorf("invalid terminal/wait_for_exit response: %w", err)
	}
	return response, nil
}

func (s *Server) KillTerminal(params client.KillTerminalRequest) error {
	if strings.TrimSpace(params.TerminalID) == "" {
		return fmt.Errorf("terminalId is required and must be a string")
	}
	_, err := s.callClient(context.Background(), "terminal/kill", params)
	return err
}

func (s *Server) ReleaseTerminal(params client.ReleaseTerminalRequest) error {
	if strings.TrimSpace(params.TerminalID) == "" {
		return fmt.Errorf("terminalId is required and must be a string")
	}
	_, err := s.callClient(context.Background(), "terminal/release", params)
	return err
}

func (s *Server) callClient(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if strings.TrimSpace(method) == "" {
		return nil, fmt.Errorf("client method is required")
	}

	requestID := fmt.Sprintf("client_%d", atomic.AddUint64(&s.clientRPCSeq, 1))
	waiter := make(chan clientRPCResponse, 1)
	s.pendingMu.Lock()
	s.pendingClientRPC[requestID] = waiter
	s.pendingMu.Unlock()

	s.writeMessage(map[string]any{
		"jsonrpc": jsonrpc.Version,
		"id":      requestID,
		"method":  method,
		"params":  params,
	})

	waitCtx := ctx
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	if _, hasDeadline := waitCtx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(waitCtx, 90*time.Second)
		defer cancel()
	}

	select {
	case resp := <-waiter:
		if resp.Error != nil {
			if resp.Error.Data != nil {
				return nil, fmt.Errorf("client %s failed: %s (code=%d, data=%v)", method, resp.Error.Message, resp.Error.Code, resp.Error.Data)
			}
			return nil, fmt.Errorf("client %s failed: %s (code=%d)", method, resp.Error.Message, resp.Error.Code)
		}
		if len(resp.Result) == 0 {
			return json.RawMessage(`null`), nil
		}
		return resp.Result, nil
	case <-waitCtx.Done():
		s.pendingMu.Lock()
		delete(s.pendingClientRPC, requestID)
		s.pendingMu.Unlock()
		return nil, fmt.Errorf("client %s timed out: %w", method, waitCtx.Err())
	}
}

func (s *Server) handleClientRPCResponse(resp clientRPCResponse) {
	responseID := fmt.Sprint(resp.ID)
	s.pendingMu.Lock()
	waiter, ok := s.pendingClientRPC[responseID]
	if ok {
		delete(s.pendingClientRPC, responseID)
	}
	s.pendingMu.Unlock()
	if !ok {
		s.logger.Debug("No pending client RPC for response", map[string]any{"id": responseID})
		return
	}

	select {
	case waiter <- resp:
	default:
	}
}

func (s *Server) writeMessage(v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		s.logger.Error("failed to serialize message", map[string]any{"error": err.Error()})
		return
	}
	s.stdoutMu.Lock()
	defer s.stdoutMu.Unlock()
	_, _ = s.stdout.Write(append(buf, '\n'))
}

func decodeParams[T any](raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("invalid params: %w", err)
	}
	return out, nil
}

func decodeObjectParams(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("params must be an object")
	}
	return obj, nil
}

func isAbsPath(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	// Windows absolute path support on non-Windows hosts.
	winAbs := regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	return winAbs.MatchString(p)
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func extractSessionID(parameters map[string]any) string {
	if parameters == nil {
		return ""
	}
	for _, key := range []string{"sessionId", "session_id", "_sessionId"} {
		if v, ok := parameters[key]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func defaultPermissionOutcome(options []permissions.PermissionOption) permissions.PermissionOutcome {
	for _, option := range options {
		if option.Kind == "allow_once" {
			return permissions.PermissionOutcome{Outcome: "selected", OptionID: option.OptionID}
		}
	}
	if len(options) > 0 {
		return permissions.PermissionOutcome{Outcome: "selected", OptionID: options[0].OptionID}
	}
	return permissions.PermissionOutcome{Outcome: "selected", OptionID: "reject-once"}
}

func mergeMaps(parts ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, m := range parts {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
