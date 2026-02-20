package agent

import (
	"context"
	"testing"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
	"github.com/spjoes/cursor-agent-acp/internal/server"
)

func TestImplementationInitialize(t *testing.T) {
	cfg := config.Default()
	cfg.SessionDir = t.TempDir()
	logger := logging.New("error")
	srv := server.New(cfg, logger)
	if err := srv.Initialize(); err != nil {
		t.Fatalf("server initialize failed: %v", err)
	}
	defer srv.Close()

	impl := NewImplementation(srv, logger)
	resp, err := impl.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion: 1,
		ClientCapabilities: map[string]any{
			"fs": map[string]any{
				"readTextFile":  true,
				"writeTextFile": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("impl initialize failed: %v", err)
	}
	if resp.ProtocolVersion != 1 {
		t.Fatalf("expected protocolVersion 1, got %d", resp.ProtocolVersion)
	}
}

func TestImplementationExtMethodNotFound(t *testing.T) {
	cfg := config.Default()
	cfg.SessionDir = t.TempDir()
	logger := logging.New("error")
	srv := server.New(cfg, logger)
	if err := srv.Initialize(); err != nil {
		t.Fatalf("server initialize failed: %v", err)
	}
	defer srv.Close()

	impl := NewImplementation(srv, logger)
	_, err := impl.ExtMethod(context.Background(), "_missing/method", map[string]any{})
	if err == nil {
		t.Fatalf("expected ext method error")
	}
}
