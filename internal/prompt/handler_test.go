package prompt

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

func newPromptTestHandler(notify NotifyFn) *Handler {
	if notify == nil {
		notify = func(string, any) {}
	}
	return &Handler{
		logger: logging.New("error"),
		notify: notify,
		processingConfig: promptProcessingConfig{
			EchoUserMessages:      true,
			SendPlan:              false,
			CollectDetailedMetric: true,
			AnnotateContent:       true,
			MarkInternalContent:   false,
		},
		sessionQueues:        map[string]chan struct{}{},
		activeCancels:        map[string]context.CancelFunc{},
		activeStreams:        map[string]context.CancelFunc{},
		activeSessionStreams: map[string]map[string]context.CancelFunc{},
	}
}

func TestDetermineStopReasonCancelled(t *testing.T) {
	h := newPromptTestHandler(nil)
	data := h.determineStopReason(errors.New("cancelled"), true, map[string]any{})
	if data.StopReason != stopReasonCancelled {
		t.Fatalf("expected cancelled stop reason, got %q", data.StopReason)
	}
	if method, _ := data.StopReasonDetails["cancelMethod"].(string); method != "session/cancel" {
		t.Fatalf("unexpected cancelMethod details: %#v", data.StopReasonDetails)
	}
}

func TestDetermineStopReasonRefusalClassification(t *testing.T) {
	h := newPromptTestHandler(nil)

	capability := h.determineStopReason(errors.New("cursor-agent CLI not installed or not in PATH"), false, map[string]any{})
	if capability.StopReason != stopReasonRefusal {
		t.Fatalf("expected refusal, got %q", capability.StopReason)
	}
	if reason, _ := capability.StopReasonDetails["reason"].(string); reason != "capability_unavailable" {
		t.Fatalf("unexpected refusal reason: %#v", capability.StopReasonDetails)
	}

	auth := h.determineStopReason(errors.New("User not authenticated. Please run: cursor-agent login"), false, map[string]any{})
	if reason, _ := auth.StopReasonDetails["reason"].(string); reason != "authentication" {
		t.Fatalf("unexpected auth refusal reason: %#v", auth.StopReasonDetails)
	}
}

func TestSendRefusalExplanation(t *testing.T) {
	var capturedMethod string
	var capturedParams map[string]any
	h := newPromptTestHandler(func(method string, params any) {
		capturedMethod = method
		if p, ok := params.(map[string]any); ok {
			capturedParams = p
		}
	})

	h.sendRefusalExplanation("session-1", errors.New("cursor-agent CLI not installed"), stopReasonData{
		StopReason: stopReasonRefusal,
		StopReasonDetails: map[string]any{
			"reason": "capability_unavailable",
		},
	})

	if capturedMethod != "session/update" {
		t.Fatalf("expected session/update notification, got %q", capturedMethod)
	}
	if capturedParams == nil {
		t.Fatalf("expected params payload")
	}
	update, _ := capturedParams["update"].(map[string]any)
	content, _ := update["content"].(acp.ContentBlock)
	if content.Type != "text" {
		t.Fatalf("expected text content, got %#v", content)
	}
	if !strings.Contains(strings.ToLower(content.Text), "not installed") {
		t.Fatalf("expected installation hint in refusal text, got %q", content.Text)
	}
	meta, _ := content.Annotations["_meta"].(map[string]any)
	if isError, _ := meta["isError"].(bool); !isError {
		t.Fatalf("expected isError=true metadata, got %#v", meta)
	}
}

func TestCancelStream(t *testing.T) {
	h := newPromptTestHandler(nil)
	var cancelled atomic.Bool
	h.registerActiveStream("session-1", "request-1", func() {
		cancelled.Store(true)
	})

	if h.GetActiveStreamCount() != 1 {
		t.Fatalf("expected active stream count 1, got %d", h.GetActiveStreamCount())
	}
	if !h.CancelStream("request-1") {
		t.Fatalf("expected CancelStream to return true")
	}
	if !cancelled.Load() {
		t.Fatalf("expected stream cancel callback to run")
	}
	if h.GetActiveStreamCount() != 0 {
		t.Fatalf("expected active stream count 0, got %d", h.GetActiveStreamCount())
	}
}

func TestSendPlan(t *testing.T) {
	notifications := 0
	h := newPromptTestHandler(func(method string, params any) {
		if method == "session/update" {
			notifications++
		}
	})

	h.SendPlan("session-1", []map[string]any{{
		"content":  "Analyze code",
		"priority": "high",
		"status":   "pending",
	}})
	if notifications != 0 {
		t.Fatalf("expected no notifications when plan sending is disabled")
	}

	h.processingConfig.SendPlan = true
	h.SendPlan("session-1", []map[string]any{{
		"content":  "Analyze code",
		"priority": "high",
		"status":   "pending",
	}})
	if notifications != 1 {
		t.Fatalf("expected one plan notification, got %d", notifications)
	}
}

func TestSessionQueueSerialization(t *testing.T) {
	h := newPromptTestHandler(nil)

	releaseFirst := h.enterSessionQueue("session-1")

	secondEntered := make(chan struct{})
	go func() {
		releaseSecond := h.enterSessionQueue("session-1")
		close(secondEntered)
		releaseSecond()
	}()

	select {
	case <-secondEntered:
		t.Fatalf("second queue entry should block until first release")
	case <-time.After(50 * time.Millisecond):
	}

	releaseFirst()

	select {
	case <-secondEntered:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("second queue entry did not resume after first release")
	}
}
