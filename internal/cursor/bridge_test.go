package cursor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

func setupFakeCursorAgent(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake cursor-agent script test is unix-only")
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "cursor-agent")
	script := `#!/bin/sh
mode=""
for arg in "$@"; do
  if [ "$arg" = "--version" ]; then
    mode="version"
  fi
  if [ "$arg" = "status" ]; then
    mode="status"
  fi
  if [ "$arg" = "agent" ]; then
    mode="agent"
  fi
done

if [ "$mode" = "version" ]; then
  echo "cursor-agent 1.2.3"
  exit 0
fi

if [ "$mode" = "status" ]; then
  printf '\033[32m✓\033[0m Logged in as dev@example.com\n'
  printf 'Plan: pro\n'
  exit 0
fi

if [ "$mode" = "agent" ]; then
  if [ "$FAIL_STREAM" = "1" ]; then
    echo "stream failed" >&2
    exit 1
  fi
  printf '{"content":"Hello"}\n'
  printf '{"content":" world"}\n'
  exit 0
fi

echo "unsupported args: $@" >&2
exit 1
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to create fake cursor-agent: %v", err)
	}

	currentPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+":"+currentPath)
}

func newTestBridge() *Bridge {
	cfg := config.Default()
	cfg.Cursor.Timeout = 2000
	cfg.Cursor.Retries = 0
	return NewBridge(cfg, logging.New("error"))
}

func TestGetVersionParsesSemver(t *testing.T) {
	setupFakeCursorAgent(t)
	bridge := newTestBridge()

	version, err := bridge.GetVersion()
	if err != nil {
		t.Fatalf("GetVersion returned error: %v", err)
	}
	if version != "1.2.3" {
		t.Fatalf("unexpected version: %q", version)
	}
}

func TestCheckAuthenticationParsesAnsiOutput(t *testing.T) {
	setupFakeCursorAgent(t)
	bridge := newTestBridge()

	status := bridge.CheckAuthentication()
	if !status.Authenticated {
		t.Fatalf("expected authenticated=true, got %#v", status)
	}
	if status.Email != "dev@example.com" {
		t.Fatalf("unexpected parsed email: %#v", status)
	}
	if status.Plan != "pro" {
		t.Fatalf("unexpected parsed plan: %#v", status)
	}
}

func TestSendStreamingPromptEmitsDoneChunk(t *testing.T) {
	setupFakeCursorAgent(t)
	bridge := newTestBridge()

	chunkTypes := make([]string, 0)
	contentChunks := 0
	result, err := bridge.SendStreamingPrompt(StreamingPromptOptions{
		SessionID: "s1",
		Content:   "hello",
		Metadata:  map[string]any{},
		Ctx:       context.Background(),
		OnChunk: func(chunk StreamChunk) error {
			chunkTypes = append(chunkTypes, chunk.Type)
			if chunk.Type == "content" {
				contentChunks++
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("SendStreamingPrompt returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success result, got %#v", result)
	}
	if contentChunks < 2 {
		t.Fatalf("expected content chunks, got %d (%#v)", contentChunks, chunkTypes)
	}
	if len(chunkTypes) == 0 || chunkTypes[len(chunkTypes)-1] != "done" {
		t.Fatalf("expected final done chunk, got %#v", chunkTypes)
	}
	if !strings.Contains(result.Text, "Hello") {
		t.Fatalf("expected aggregated text in result, got %#v", result)
	}
}

func TestSendStreamingPromptEmitsErrorChunkOnFailure(t *testing.T) {
	setupFakeCursorAgent(t)
	t.Setenv("FAIL_STREAM", "1")
	bridge := newTestBridge()

	errorSeen := false
	result, err := bridge.SendStreamingPrompt(StreamingPromptOptions{
		SessionID: "s1",
		Content:   "hello",
		Metadata:  map[string]any{},
		Ctx:       context.Background(),
		OnChunk: func(chunk StreamChunk) error {
			if chunk.Type == "error" {
				errorSeen = true
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("SendStreamingPrompt should return result error payload, got call error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected unsuccessful streaming result, got %#v", result)
	}
	if !errorSeen {
		t.Fatalf("expected error chunk callback on stream failure")
	}
}
