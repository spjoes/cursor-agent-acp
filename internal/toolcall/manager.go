package toolcall

import (
	"fmt"
	"sync"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/logging"
	"github.com/spjoes/cursor-agent-acp/internal/permissions"
)

type PermissionRequester func(params permissions.RequestPermissionParams) permissions.PermissionOutcome

type SendNotification func(notification map[string]any)

type ToolCallInfo struct {
	ToolCallID       string
	SessionID        string
	ToolName         string
	Status           string
	StartTime        time.Time
	EndTime          *time.Time
	LastNotification map[string]any
	cleanupTimer     *time.Timer
}

type Manager struct {
	logger            *logging.Logger
	send              SendNotification
	requestPermission PermissionRequester

	mu              sync.Mutex
	activeToolCalls map[string]*ToolCallInfo
	toolCallCounter int64
	notificationSeq int64
}

func NewManager(logger *logging.Logger, send SendNotification, permission PermissionRequester) *Manager {
	return &Manager{
		logger:            logger,
		send:              send,
		requestPermission: permission,
		activeToolCalls:   map[string]*ToolCallInfo{},
	}
}

func (m *Manager) GenerateToolCallID(toolName string) string {
	m.mu.Lock()
	m.toolCallCounter++
	counter := m.toolCallCounter
	m.mu.Unlock()
	return fmt.Sprintf("tool_%s_%d_%d", toolName, time.Now().UnixMilli(), counter)
}

func (m *Manager) ReportToolCall(sessionID, toolName string, options map[string]any) string {
	toolCallID, _ := options["toolCallId"].(string)
	if toolCallID == "" {
		toolCallID = m.GenerateToolCallID(toolName)
	}
	status, _ := options["status"].(string)
	if status == "" {
		status = "pending"
	}

	now := time.Now().UTC()
	update := map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    toolCallID,
		"title":         options["title"],
		"status":        status,
		"_meta": mergeMeta(options["_meta"], map[string]any{
			"toolName":  toolName,
			"startTime": now.Format(time.RFC3339),
			"source":    "tool-call-manager",
		}),
	}
	for _, key := range []string{"kind", "locations", "rawInput", "content", "rawOutput"} {
		if v, ok := options[key]; ok {
			update[key] = v
		}
	}

	notification := m.buildNotification(sessionID, update)

	m.mu.Lock()
	m.activeToolCalls[toolCallID] = &ToolCallInfo{
		ToolCallID:       toolCallID,
		SessionID:        sessionID,
		ToolName:         toolName,
		Status:           status,
		StartTime:        now,
		LastNotification: notification,
	}
	m.mu.Unlock()

	m.logger.Debug("Reporting tool call", map[string]any{"toolCallId": toolCallID, "sessionId": sessionID, "toolName": toolName, "status": status})
	m.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": notification})
	return toolCallID
}

func (m *Manager) UpdateToolCall(sessionID, toolCallID string, updates map[string]any) {
	m.mu.Lock()
	info, ok := m.activeToolCalls[toolCallID]
	m.mu.Unlock()
	if !ok {
		m.logger.Warn("Tool call not found for update", map[string]any{"toolCallId": toolCallID, "sessionId": sessionID})
		return
	}

	now := time.Now().UTC()
	if status, ok := updates["status"].(string); ok && status != "" {
		info.Status = status
		if status == "completed" || status == "failed" {
			info.EndTime = &now
		}
	}

	update := map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    toolCallID,
		"_meta":         mergeMeta(updates["_meta"], map[string]any{"updateTime": now.Format(time.RFC3339), "source": "tool-call-manager"}),
	}
	for _, key := range []string{"title", "kind", "status", "content", "locations", "rawInput", "rawOutput"} {
		if v, ok := updates[key]; ok {
			update[key] = v
		}
	}

	notification := m.buildNotification(sessionID, update)
	m.mu.Lock()
	info.LastNotification = notification
	m.mu.Unlock()

	m.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": notification})
}

func (m *Manager) CompleteToolCall(sessionID, toolCallID string, options map[string]any) {
	updates := cloneMap(options)
	updates["status"] = "completed"
	m.UpdateToolCall(sessionID, toolCallID, updates)
	m.scheduleCleanup(toolCallID)
}

func (m *Manager) FailToolCall(sessionID, toolCallID string, options map[string]any) {
	title, _ := options["title"].(string)
	if title == "" {
		title = "Tool execution failed"
	}
	errMsg, _ := options["error"].(string)
	updates := map[string]any{
		"title":  title,
		"status": "failed",
		"content": []map[string]any{{
			"type": "content",
			"content": map[string]any{
				"type": "text",
				"text": "Error: " + errMsg,
			},
		}},
	}
	if raw, ok := options["rawOutput"]; ok && raw != nil {
		updates["rawOutput"] = raw
	}
	m.UpdateToolCall(sessionID, toolCallID, updates)
	m.scheduleCleanup(toolCallID)
}

func (m *Manager) RequestToolPermission(sessionID, toolCallID string, options []permissions.PermissionOption) permissions.PermissionOutcome {
	if m.requestPermission == nil {
		m.logger.Warn("Permission request not supported - no requestPermission handler provided", nil)
		if len(options) > 0 {
			return permissions.PermissionOutcome{Outcome: "selected", OptionID: options[0].OptionID}
		}
		return permissions.PermissionOutcome{Outcome: "selected", OptionID: "allow-once"}
	}

	m.mu.Lock()
	info, ok := m.activeToolCalls[toolCallID]
	m.mu.Unlock()
	if !ok {
		m.logger.Warn("Tool call not found for permission request", map[string]any{"toolCallId": toolCallID, "sessionId": sessionID})
		return permissions.PermissionOutcome{Outcome: "selected", OptionID: "reject-once"}
	}

	params := permissions.RequestPermissionParams{
		SessionID: sessionID,
		Options:   options,
	}
	if update, ok := info.LastNotification["update"].(map[string]any); ok {
		params.ToolCall = update
	} else {
		params.ToolCall = map[string]any{"toolCallId": toolCallID}
	}

	outcome := m.requestPermission(params)
	if outcome.Outcome == "" {
		m.logger.Warn("Permission request returned no outcome", map[string]any{"toolCallId": toolCallID, "sessionId": sessionID})
		return permissions.PermissionOutcome{Outcome: "selected", OptionID: "reject-once"}
	}
	return outcome
}

func (m *Manager) GetToolCallInfo(toolCallID string) *ToolCallInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.activeToolCalls[toolCallID]
	if !ok {
		return nil
	}
	copy := *info
	return &copy
}

func (m *Manager) GetSessionToolCalls(sessionID string) []ToolCallInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ToolCallInfo, 0)
	for _, call := range m.activeToolCalls {
		if call.SessionID == sessionID {
			out = append(out, *call)
		}
	}
	return out
}

func (m *Manager) CancelSessionToolCalls(sessionID string) {
	calls := m.GetSessionToolCalls(sessionID)
	for _, call := range calls {
		if call.Status == "pending" || call.Status == "in_progress" {
			m.UpdateToolCall(sessionID, call.ToolCallID, map[string]any{"status": "failed", "title": "Cancelled by user"})
		}
		m.mu.Lock()
		if info, ok := m.activeToolCalls[call.ToolCallID]; ok {
			if info.cleanupTimer != nil {
				info.cleanupTimer.Stop()
			}
			delete(m.activeToolCalls, call.ToolCallID)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) Metrics() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	statusCounts := map[string]int{"pending": 0, "in_progress": 0, "completed": 0, "failed": 0}
	for _, call := range m.activeToolCalls {
		statusCounts[call.Status]++
	}
	return map[string]any{
		"activeToolCalls": len(m.activeToolCalls),
		"statusCounts":    statusCounts,
		"totalToolCalls":  m.toolCallCounter,
	}
}

func (m *Manager) Cleanup() {
	m.mu.Lock()
	for _, call := range m.activeToolCalls {
		if call.cleanupTimer != nil {
			call.cleanupTimer.Stop()
		}
	}
	m.activeToolCalls = map[string]*ToolCallInfo{}
	m.mu.Unlock()
}

func (m *Manager) ConvertDiffContent(diffBlocks []any) []map[string]any {
	content := make([]map[string]any, 0)
	for _, block := range diffBlocks {
		content = append(content, map[string]any{"type": "content", "content": block})
	}
	return content
}

func (m *Manager) CreateTerminalContent(terminalID string) []map[string]any {
	return []map[string]any{{"type": "terminal", "terminalId": terminalID}}
}

func (m *Manager) buildNotification(sessionID string, update map[string]any) map[string]any {
	m.mu.Lock()
	m.notificationSeq++
	seq := m.notificationSeq
	m.mu.Unlock()
	return map[string]any{
		"sessionId": sessionID,
		"update":    update,
		"_meta": map[string]any{
			"timestamp":            time.Now().UTC().Format(time.RFC3339),
			"notificationSequence": seq,
		},
	}
}

func (m *Manager) scheduleCleanup(toolCallID string) {
	m.mu.Lock()
	info, ok := m.activeToolCalls[toolCallID]
	if !ok {
		m.mu.Unlock()
		return
	}
	if info.cleanupTimer != nil {
		info.cleanupTimer.Stop()
	}
	info.cleanupTimer = time.AfterFunc(30*time.Second, func() {
		m.mu.Lock()
		delete(m.activeToolCalls, toolCallID)
		m.mu.Unlock()
	})
	m.mu.Unlock()
}

func mergeMeta(existing any, add map[string]any) map[string]any {
	out := map[string]any{}
	if m, ok := existing.(map[string]any); ok {
		for k, v := range m {
			out[k] = v
		}
	}
	for k, v := range add {
		out[k] = v
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
