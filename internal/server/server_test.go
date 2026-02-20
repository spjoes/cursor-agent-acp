package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/jsonrpc"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

func TestSessionNewDefersAvailableCommandsUntilPostResponse(t *testing.T) {
	s := newTestServer(t)

	var stdout bytes.Buffer
	s.stdout = &stdout

	req := mustRequest(t, "req-1", "session/new", map[string]any{
		"cwd":        "/tmp",
		"mcpServers": []map[string]any{},
	})

	resp, postResponse := s.processRequest(context.Background(), req)
	if resp.Error != nil {
		t.Fatalf("session/new failed: %+v", resp.Error)
	}
	if postResponse == nil {
		t.Fatalf("expected post-response callback for session/new")
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no notification before post-response callback, got: %s", stdout.String())
	}

	postResponse()

	lines := splitJSONLines(stdout.String())
	if len(lines) != 1 {
		t.Fatalf("expected exactly one notification after post-response callback, got %d (%q)", len(lines), stdout.String())
	}

	var notification map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &notification); err != nil {
		t.Fatalf("failed to decode notification: %v", err)
	}

	if method, _ := notification["method"].(string); method != "session/update" {
		t.Fatalf("expected session/update notification, got %#v", notification)
	}
	params, _ := notification["params"].(map[string]any)
	update, _ := params["update"].(map[string]any)
	if sessionUpdate, _ := update["sessionUpdate"].(string); sessionUpdate != "available_commands_update" {
		t.Fatalf("expected available_commands_update, got %#v", notification)
	}
}

func TestSessionNewIncludesMCPStatusMetadata(t *testing.T) {
	s := newTestServer(t)

	req := mustRequest(t, "req-2", "session/new", map[string]any{
		"cwd": "/tmp",
		"mcpServers": []map[string]any{
			{"name": "foo", "type": "sse"},
		},
	})

	resp, _ := s.processRequest(context.Background(), req)
	if resp.Error != nil {
		t.Fatalf("session/new failed: %+v", resp.Error)
	}

	result, ok := resp.Result.(acp.NewSessionResponse)
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	meta := result.Meta
	if meta == nil {
		t.Fatalf("expected _meta in session/new response")
	}
	if status, _ := meta["mcpStatus"].(string); status != "not-implemented" {
		t.Fatalf("expected mcpStatus=not-implemented, got %#v", meta["mcpStatus"])
	}
	var serverEntry map[string]any
	switch servers := meta["mcpServers"].(type) {
	case []map[string]any:
		if len(servers) != 1 {
			t.Fatalf("expected one mcp server entry, got %#v", meta["mcpServers"])
		}
		serverEntry = servers[0]
	case []any:
		if len(servers) != 1 {
			t.Fatalf("expected one mcp server entry, got %#v", meta["mcpServers"])
		}
		serverEntry, _ = servers[0].(map[string]any)
	default:
		t.Fatalf("unexpected mcpServers type: %T", meta["mcpServers"])
	}
	if state, _ := serverEntry["status"].(string); state != "pending-implementation" {
		t.Fatalf("expected pending-implementation, got %#v", serverEntry["status"])
	}
}

func TestSessionLoadIncludesMCPServerCount(t *testing.T) {
	s := newTestServer(t)

	newReq := mustRequest(t, "req-3", "session/new", map[string]any{
		"cwd":        "/tmp",
		"mcpServers": []map[string]any{},
	})
	newResp, _ := s.processRequest(context.Background(), newReq)
	if newResp.Error != nil {
		t.Fatalf("session/new failed: %+v", newResp.Error)
	}
	newResult, ok := newResp.Result.(acp.NewSessionResponse)
	if !ok {
		t.Fatalf("unexpected result type: %T", newResp.Result)
	}
	sessionID := newResult.SessionID
	if strings.TrimSpace(sessionID) == "" {
		t.Fatalf("expected sessionId in session/new response")
	}

	loadReq := mustRequest(t, "req-4", "session/load", map[string]any{
		"sessionId": sessionID,
		"cwd":       "/tmp",
		"mcpServers": []map[string]any{
			{"name": "alpha", "type": "stdio"},
			{"name": "beta", "type": "sse"},
		},
	})
	loadResp, _ := s.processRequest(context.Background(), loadReq)
	if loadResp.Error != nil {
		t.Fatalf("session/load failed: %+v", loadResp.Error)
	}

	loadResult, ok := loadResp.Result.(acp.LoadSessionResponse)
	if !ok {
		t.Fatalf("unexpected result type: %T", loadResp.Result)
	}
	meta := loadResult.Meta
	if meta == nil {
		t.Fatalf("expected _meta in session/load response")
	}
	count, ok := meta["mcpServerCount"].(int)
	if !ok {
		t.Fatalf("expected numeric mcpServerCount, got %#v", meta["mcpServerCount"])
	}
	if count != 2 {
		t.Fatalf("expected mcpServerCount=2, got %v", count)
	}
}

func TestSetSessionModeDoesNotSendCurrentModeNotification(t *testing.T) {
	s := newTestServer(t)

	newReq := mustRequest(t, "req-5", "session/new", map[string]any{
		"cwd":        "/tmp",
		"mcpServers": []map[string]any{},
	})
	newResp, _ := s.processRequest(context.Background(), newReq)
	if newResp.Error != nil {
		t.Fatalf("session/new failed: %+v", newResp.Error)
	}
	newResult, ok := newResp.Result.(acp.NewSessionResponse)
	if !ok {
		t.Fatalf("unexpected result type: %T", newResp.Result)
	}
	sessionID := newResult.SessionID
	if strings.TrimSpace(sessionID) == "" {
		t.Fatalf("expected sessionId in session/new response")
	}

	var stdout bytes.Buffer
	s.stdout = &stdout

	modeReq := mustRequest(t, "req-6", "session/set_mode", map[string]any{
		"sessionId": sessionID,
		"modeId":    "plan",
	})
	modeResp, _ := s.processRequest(context.Background(), modeReq)
	if modeResp.Error != nil {
		t.Fatalf("session/set_mode failed: %+v", modeResp.Error)
	}

	// processRequest() no longer emits current_mode_update for direct session/set_mode requests.
	if strings.Contains(stdout.String(), "current_mode_update") {
		t.Fatalf("unexpected current_mode_update notification: %s", stdout.String())
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()

	fakeBinDir := t.TempDir()
	fakeCursor := filepath.Join(fakeBinDir, "cursor-agent")
	script := `#!/usr/bin/env bash
set -euo pipefail
if [[ $# -eq 0 ]]; then
  exit 0
fi
case "$1" in
  --version)
    echo "cursor-agent 1.2.3"
    ;;
  status)
    echo "Signed in as test@example.com"
    ;;
  create-chat)
    echo "chat_test_123"
    ;;
  models)
    echo "auto"
    ;;
  *)
    echo "{}"
    ;;
esac
`
	if err := os.WriteFile(fakeCursor, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake cursor-agent: %v", err)
	}
	t.Setenv("PATH", fakeBinDir+":"+os.Getenv("PATH"))

	cfg := config.Default()
	cfg.SessionDir = t.TempDir()
	normalized, err := config.Normalize(cfg)
	if err != nil {
		t.Fatalf("failed to normalize config: %v", err)
	}

	s := New(normalized, logging.New("error"))
	s.stdout = &bytes.Buffer{}
	t.Cleanup(func() {
		s.Close()
	})
	return s
}

func mustRequest(t *testing.T, id string, method string, params map[string]any) jsonrpc.Request {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	var req jsonrpc.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("failed to unmarshal request: %v", err)
	}
	return req
}

func splitJSONLines(input string) []string {
	lines := strings.Split(strings.TrimSpace(input), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
