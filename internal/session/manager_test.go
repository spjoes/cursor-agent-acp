package session

import (
	"regexp"
	"testing"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	cfg := config.Default()
	cfg.SessionDir = t.TempDir()
	normalized, err := config.Normalize(cfg)
	if err != nil {
		t.Fatalf("failed to normalize config: %v", err)
	}
	m := NewManager(normalized, logging.New("error"))
	t.Cleanup(func() { m.Close() })
	return m
}

func TestCreateSessionUsesUUIDv4(t *testing.T) {
	m := newTestManager(t)

	session, err := m.CreateSession(nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !re.MatchString(session.ID) {
		t.Fatalf("expected UUID v4 session ID, got %q", session.ID)
	}
}

func TestLoadSessionUpdatesActivityTimestamps(t *testing.T) {
	m := newTestManager(t)

	session, err := m.CreateSession(nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	initialUpdatedAt := session.UpdatedAt
	initialLastActivity := session.State.LastActivity
	time.Sleep(10 * time.Millisecond)

	loaded, err := m.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("LoadSession returned error: %v", err)
	}

	if !loaded.UpdatedAt.After(initialUpdatedAt) {
		t.Fatalf("expected UpdatedAt to advance on load: before=%s after=%s", initialUpdatedAt, loaded.UpdatedAt)
	}
	if !loaded.State.LastActivity.After(initialLastActivity) {
		t.Fatalf("expected LastActivity to advance on load: before=%s after=%s", initialLastActivity, loaded.State.LastActivity)
	}
}
