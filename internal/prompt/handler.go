package prompt

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/content"
	"github.com/spjoes/cursor-agent-acp/internal/cursor"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
	"github.com/spjoes/cursor-agent-acp/internal/session"
	"github.com/spjoes/cursor-agent-acp/internal/slash"
)

type NotifyFn func(method string, params any)

type contentAnnotationOptions struct {
	Priority   *int
	Audience   []string
	Confidence *float64
	Source     string
	Category   string
}

type promptProcessingConfig struct {
	EchoUserMessages      bool
	SendPlan              bool
	CollectDetailedMetric bool
	AnnotateContent       bool
	MarkInternalContent   bool
}

type stopReasonData struct {
	StopReason        string
	StopReasonDetails map[string]any
}

type Handler struct {
	sessions *session.Manager
	cursor   *cursor.Bridge
	content  *content.Processor
	logger   *logging.Logger
	notify   NotifyFn
	slash    *slash.Registry

	processingConfig promptProcessingConfig

	mu                   sync.Mutex
	sessionQueues        map[string]chan struct{}
	activeCancels        map[string]context.CancelFunc
	activeStreams        map[string]context.CancelFunc
	activeSessionStreams map[string]map[string]context.CancelFunc
}

const (
	stopReasonEndTurn         = "end_turn"
	stopReasonMaxTokens       = "max_tokens"
	stopReasonMaxTurnRequests = "max_turn_requests"
	stopReasonRefusal         = "refusal"
	stopReasonCancelled       = "cancelled"
)

var slashCommandPattern = regexp.MustCompile(`^/(\S+)(?:\s+(.*))?$`)

func NewHandler(sessions *session.Manager, cursorBridge *cursor.Bridge, logger *logging.Logger, notify NotifyFn, slashRegistry *slash.Registry) *Handler {
	return &Handler{
		sessions: sessions,
		cursor:   cursorBridge,
		content:  content.NewProcessor(logger),
		logger:   logger,
		notify:   notify,
		slash:    slashRegistry,
		processingConfig: promptProcessingConfig{
			EchoUserMessages:      true,
			SendPlan:              false,
			CollectDetailedMetric: true,
			AnnotateContent:       true,
			MarkInternalContent:   false,
		},
		sessionQueues:        make(map[string]chan struct{}),
		activeCancels:        make(map[string]context.CancelFunc),
		activeStreams:        make(map[string]context.CancelFunc),
		activeSessionStreams: make(map[string]map[string]context.CancelFunc),
	}
}

func (h *Handler) Process(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	return h.ProcessWithRequestID(ctx, req, "")
}

func (h *Handler) ProcessWithRequestID(ctx context.Context, req acp.PromptRequest, requestID string) (acp.PromptResponse, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return acp.PromptResponse{}, fmt.Errorf("sessionId is required")
	}

	contentBlocks := req.Prompt
	if len(contentBlocks) == 0 {
		contentBlocks = req.Content
	}
	if len(contentBlocks) == 0 {
		return acp.PromptResponse{}, fmt.Errorf("prompt is required and must be a non-empty array of ContentBlock")
	}

	validation := h.content.ValidateContentBlocks(contentBlocks)
	if !validation.Valid {
		return acp.PromptResponse{}, fmt.Errorf("Invalid content block: %s", validation.Errors[0])
	}

	releaseQueue := h.enterSessionQueue(sessionID)
	defer releaseQueue()

	sessionData, err := h.sessions.LoadSession(sessionID)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	h.sessions.MarkProcessing(sessionID)
	defer h.sessions.UnmarkProcessing(sessionID)

	pctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.activeCancels[sessionID] = cancel
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.activeCancels, sessionID)
		h.mu.Unlock()
		cancel()
	}()

	start := time.Now().UTC()
	processingText := randomProcessingText()
	h.sendThought(sessionID, processingText, 0, 0)

	var heartbeats atomic.Int64
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(12 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				count := heartbeats.Add(1)
				elapsed := int(count) * 12
				if err := h.sessions.TouchSession(sessionID); err != nil {
					h.logger.Warn("Session not found during heartbeat", map[string]any{"sessionId": sessionID, "error": err.Error()})
					return
				}
				h.sendThought(sessionID, fmt.Sprintf("%s (%ds)", processingText, elapsed), int(count), elapsed)
			case <-heartbeatDone:
				return
			case <-pctx.Done():
				return
			}
		}
	}()
	defer close(heartbeatDone)

	metadata := cloneMeta(req.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}

	// Slash command processing is only applied for regular prompts, matching TS behavior.
	if !req.Stream {
		if command, input, ok := detectSlashCommand(contentBlocks); ok {
			_, _ = h.processSlashCommand(sessionID, command, input)
		}
	}

	userMessage := acp.ConversationMessage{
		ID:        messageID(),
		Role:      "user",
		Content:   contentBlocks,
		Timestamp: time.Now().UTC(),
		Metadata:  cloneMeta(metadata),
	}
	if err := h.sessions.AddMessage(sessionID, userMessage); err != nil {
		return acp.PromptResponse{}, err
	}
	h.echoUserMessage(sessionID, contentBlocks)

	processedContent, err := h.content.ProcessContent(contentBlocks)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	metadata["contentMetadata"] = processedContent.Metadata
	if cwd, ok := sessionData.Metadata["cwd"].(string); ok && strings.TrimSpace(cwd) != "" {
		metadata["cwd"] = cwd
	}
	metadata["model"] = h.sessions.GetSessionModel(sessionID)
	if chatID := h.sessions.GetCursorChatID(sessionID); chatID != "" {
		metadata["cursorChatId"] = chatID
	}

	assistantBlocks := make([]acp.ContentBlock, 0)
	responseMetadata := map[string]any{}
	var processingErr error
	aborted := false

	if req.Stream {
		streamRequestID := strings.TrimSpace(requestID)
		if streamRequestID == "" {
			streamRequestID = messageID()
		}

		streamCtx, streamCancel := context.WithCancel(pctx)
		h.registerActiveStream(sessionID, streamRequestID, streamCancel)
		defer h.unregisterActiveStream(sessionID, streamRequestID)

		h.content.StartStreaming()
		streamResult, serr := h.cursor.SendStreamingPrompt(cursor.StreamingPromptOptions{
			SessionID: sessionID,
			Content:   processedContent.Value,
			Metadata:  metadata,
			Ctx:       streamCtx,
			OnChunk: func(chunk cursor.StreamChunk) error {
				if chunk.Type == "error" {
					return fmt.Errorf("Stream error: %v", chunk.Data)
				}
				if chunk.Type != "content" {
					return nil
				}

				block, berr := h.content.ProcessStreamChunk(chunk.Data)
				if berr != nil {
					return berr
				}
				if block == nil {
					return nil
				}

				assistantBlocks = append(assistantBlocks, *block)
				h.sendAnnotatedAgentMessage(sessionID, *block)
				return nil
			},
			OnProgress: func(progress cursor.StreamProgress) {
				h.logger.Debug("Stream progress", map[string]any{"current": progress.Current, "message": progress.Message})
			},
		})

		finalBlock := h.content.FinalizeStreaming()
		if finalBlock != nil {
			assistantBlocks = append(assistantBlocks, *finalBlock)
			h.sendAnnotatedAgentMessage(sessionID, *finalBlock)
		}

		if serr != nil {
			processingErr = serr
			aborted = streamCtx.Err() != nil || errors.Is(serr, context.Canceled)
		} else if !streamResult.Success {
			if strings.TrimSpace(streamResult.Error) != "" {
				processingErr = errors.New(streamResult.Error)
			} else {
				processingErr = errors.New("Streaming error: Unknown error")
			}
			aborted = streamResult.Aborted || streamCtx.Err() != nil
		} else {
			if len(assistantBlocks) == 0 && strings.TrimSpace(streamResult.Text) != "" {
				assistantBlocks = h.content.ParseResponse(streamResult.Text)
				for _, block := range assistantBlocks {
					h.sendAnnotatedAgentMessage(sessionID, block)
				}
			}
			if streamResult.Metadata != nil {
				responseMetadata = cloneMeta(streamResult.Metadata)
			}
		}
	} else {
		cursorResult, cerr := h.cursor.SendPrompt(cursor.PromptOptions{
			SessionID: sessionID,
			Content:   processedContent.Value,
			Metadata:  metadata,
			Ctx:       pctx,
		})

		if cerr != nil {
			processingErr = cerr
			aborted = pctx.Err() != nil || errors.Is(cerr, context.Canceled)
		} else if !cursorResult.Success {
			if strings.TrimSpace(cursorResult.Error) != "" {
				processingErr = errors.New(cursorResult.Error)
			} else {
				processingErr = errors.New("Cursor CLI error: Unknown error")
			}
		} else {
			assistantBlocks = h.content.ParseResponse(cursorResult.Text)
			if cursorResult.Metadata != nil {
				responseMetadata = cloneMeta(cursorResult.Metadata)
			}
			for _, block := range assistantBlocks {
				h.sendAnnotatedAgentMessage(sessionID, block)
			}
		}
	}

	if h.processingConfig.CollectDetailedMetric {
		responseMetadata["contentMetrics"] = map[string]any{
			"inputBlocks":  len(contentBlocks),
			"inputSize":    h.calculateContentSize(contentBlocks),
			"outputBlocks": len(assistantBlocks),
			"outputSize":   h.calculateContentSize(assistantBlocks),
		}
	}
	responseMetadata["messageBlocks"] = len(assistantBlocks)

	stopData := h.determineStopReason(processingErr, aborted, responseMetadata)
	finalStopReason := stopData.StopReason
	if processingErr != nil && stopData.StopReason == stopReasonRefusal {
		h.sendRefusalExplanation(sessionID, processingErr, stopData)
		finalStopReason = stopReasonEndTurn
	}

	if processingErr == nil {
		assistantMessage := acp.ConversationMessage{
			ID:        messageID(),
			Role:      "assistant",
			Content:   assistantBlocks,
			Timestamp: time.Now().UTC(),
			Metadata:  cloneMeta(responseMetadata),
		}
		if err := h.sessions.AddMessage(sessionID, assistantMessage); err != nil {
			return acp.PromptResponse{}, err
		}
	}

	end := time.Now().UTC()
	meta := map[string]any{
		"processingStartedAt":  start.Format(time.RFC3339),
		"processingEndedAt":    end.Format(time.RFC3339),
		"processingDurationMs": end.Sub(start).Milliseconds(),
		"sessionId":            sessionID,
		"streaming":            req.Stream,
		"heartbeatsCount":      int(heartbeats.Load()),
	}
	if refreshed, err := h.sessions.LoadSession(sessionID); err == nil {
		meta["sessionMessageCount"] = refreshed.State.MessageCount
	}
	if cm, ok := responseMetadata["contentMetrics"]; ok {
		meta["contentMetrics"] = cm
	}
	if stopData.StopReasonDetails != nil {
		meta["stopReasonDetails"] = stopData.StopReasonDetails
	}
	if n := len(assistantBlocks); n > 0 {
		meta["messageBlocks"] = n
	}

	if processingErr != nil {
		h.logger.Warn("Prompt processing completed with error", map[string]any{
			"sessionId":          sessionID,
			"originalStopReason": stopData.StopReason,
			"finalStopReason":    finalStopReason,
			"error":              processingErr.Error(),
			"explanationSent":    finalStopReason == stopReasonEndTurn && stopData.StopReason == stopReasonRefusal,
		})
	}

	return acp.PromptResponse{StopReason: finalStopReason, Meta: meta}, nil
}

func (h *Handler) CancelStream(requestID string) bool {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false
	}

	h.mu.Lock()
	cancel, ok := h.activeStreams[requestID]
	if ok {
		delete(h.activeStreams, requestID)
	}
	for sessionID, streams := range h.activeSessionStreams {
		if _, exists := streams[requestID]; exists {
			delete(streams, requestID)
			if len(streams) == 0 {
				delete(h.activeSessionStreams, sessionID)
			}
			break
		}
	}
	h.mu.Unlock()

	if ok {
		cancel()
		h.logger.Debug("Stream cancelled", map[string]any{"requestId": requestID})
	}
	return ok
}

func (h *Handler) GetActiveStreamCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.activeStreams)
}

func (h *Handler) CancelSession(sessionID string) {
	h.mu.Lock()
	cancel, ok := h.activeCancels[sessionID]
	streamCancels := make([]context.CancelFunc, 0)
	activeStreamCount := 0
	if streams, exists := h.activeSessionStreams[sessionID]; exists {
		activeStreamCount = len(streams)
		for requestID, streamCancel := range streams {
			delete(h.activeStreams, requestID)
			streamCancels = append(streamCancels, streamCancel)
		}
		delete(h.activeSessionStreams, sessionID)
	}
	h.mu.Unlock()

	if ok {
		cancel()
	}
	for _, streamCancel := range streamCancels {
		streamCancel()
	}
	if !ok && activeStreamCount == 0 {
		h.logger.Debug("No active requests found for session", map[string]any{"sessionId": sessionID})
	}
}

func (h *Handler) SendPlan(sessionID string, entries []map[string]any) {
	if !h.processingConfig.SendPlan || len(entries) == 0 {
		return
	}
	h.logger.Debug("Sending plan", map[string]any{"sessionId": sessionID, "stepCount": len(entries)})
	h.sendPlanNotification(sessionID, entries)
}

func (h *Handler) UpdatePlan(sessionID string, entries []map[string]any) {
	if !h.processingConfig.SendPlan {
		return
	}
	h.logger.Debug("Updating plan", map[string]any{"sessionId": sessionID, "entryCount": len(entries)})
	h.sendPlanNotification(sessionID, entries)
}

func (h *Handler) Close() {
	h.mu.Lock()
	for _, cancel := range h.activeCancels {
		cancel()
	}
	for _, cancel := range h.activeStreams {
		cancel()
	}
	h.activeCancels = map[string]context.CancelFunc{}
	h.activeStreams = map[string]context.CancelFunc{}
	h.activeSessionStreams = map[string]map[string]context.CancelFunc{}
	h.sessionQueues = map[string]chan struct{}{}
	h.mu.Unlock()
	h.logger.Debug("PromptHandler cleanup completed", nil)
}

func (h *Handler) enterSessionQueue(sessionID string) func() {
	h.mu.Lock()
	prev := h.sessionQueues[sessionID]
	current := make(chan struct{})
	h.sessionQueues[sessionID] = current
	h.mu.Unlock()

	if prev != nil {
		<-prev
	}

	return func() {
		close(current)
		h.mu.Lock()
		if tail, ok := h.sessionQueues[sessionID]; ok && tail == current {
			delete(h.sessionQueues, sessionID)
		}
		h.mu.Unlock()
	}
}

func (h *Handler) registerActiveStream(sessionID, requestID string, cancel context.CancelFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.activeStreams[requestID] = cancel
	if _, ok := h.activeSessionStreams[sessionID]; !ok {
		h.activeSessionStreams[sessionID] = map[string]context.CancelFunc{}
	}
	h.activeSessionStreams[sessionID][requestID] = cancel
}

func (h *Handler) unregisterActiveStream(sessionID, requestID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.activeStreams, requestID)
	if streams, ok := h.activeSessionStreams[sessionID]; ok {
		delete(streams, requestID)
		if len(streams) == 0 {
			delete(h.activeSessionStreams, sessionID)
		}
	}
}

func (h *Handler) determineStopReason(err error, aborted bool, responseMetadata map[string]any) stopReasonData {
	if aborted {
		details := map[string]any{
			"cancelledAt":  time.Now().UTC().Format(time.RFC3339),
			"cancelMethod": "session/cancel",
		}
		if reason, ok := responseMetadata["cancelReason"]; ok {
			details["reason"] = reason
		}
		return stopReasonData{StopReason: stopReasonCancelled, StopReasonDetails: details}
	}

	if reason, _ := responseMetadata["reason"].(string); reason == stopReasonMaxTokens || truthy(responseMetadata["tokenLimitReached"]) {
		details := map[string]any{"contentTruncated": true}
		for _, key := range []string{"tokensUsed", "tokenLimit", "partialCompletion"} {
			if v, ok := responseMetadata[key]; ok {
				details[key] = v
			}
		}
		return stopReasonData{StopReason: stopReasonMaxTokens, StopReasonDetails: details}
	}

	if reason, _ := responseMetadata["reason"].(string); reason == stopReasonMaxTurnRequests || truthy(responseMetadata["turnLimitReached"]) {
		details := map[string]any{}
		for _, key := range []string{"turnsUsed", "turnLimit", "toolCallsMade"} {
			if v, ok := responseMetadata[key]; ok {
				details[key] = v
			}
		}
		return stopReasonData{StopReason: stopReasonMaxTurnRequests, StopReasonDetails: details}
	}

	if err != nil || truthy(responseMetadata["refused"]) || truthy(responseMetadata["error"]) {
		reason := classifyRefusalReason(err, responseMetadata)
		details := map[string]any{
			"reason":      reason,
			"refusalType": refusalType(err),
		}
		if err != nil {
			details["errorName"] = "Error"
			details["errorMessage"] = err.Error()
		}
		if v, ok := responseMetadata["refusalReason"]; ok {
			details["refusalReason"] = v
		}
		if v, ok := responseMetadata["safeguardTriggered"]; ok {
			details["safeguard"] = v
		}
		return stopReasonData{StopReason: stopReasonRefusal, StopReasonDetails: details}
	}

	details := map[string]any{"completionType": "normal"}
	if blocks, ok := responseMetadata["messageBlocks"]; ok {
		details["contentBlocks"] = blocks
	}
	return stopReasonData{StopReason: stopReasonEndTurn, StopReasonDetails: details}
}

func classifyRefusalReason(err error, responseMetadata map[string]any) string {
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "cursor-agent") || strings.Contains(msg, "cursor cli") || strings.Contains(msg, "enoent") || strings.Contains(msg, "command not found") {
			if strings.Contains(msg, "not installed") || strings.Contains(msg, "not found") || strings.Contains(msg, "enoent") || strings.Contains(msg, "spawn cursor-agent enoent") || strings.Contains(msg, "command not found") {
				return "capability_unavailable"
			}
			if strings.Contains(msg, "not authenticated") || strings.Contains(msg, "authentication") || strings.Contains(msg, "auth") || strings.Contains(msg, "login") || strings.Contains(msg, "sign in") || strings.Contains(msg, "unauthorized") {
				return "authentication"
			}
			if strings.Contains(msg, "cursor cli error") && !strings.Contains(msg, "timeout") && !strings.Contains(msg, "rate limit") {
				return "authentication"
			}
			return "capability_unavailable"
		}
		if strings.Contains(msg, "authentication") {
			return "authentication"
		}
		if strings.Contains(msg, "rate limit") {
			return "rate_limit"
		}
		if strings.Contains(msg, "timeout") {
			return "timeout"
		}
		return "error"
	}
	if truthy(responseMetadata["safeguardTriggered"]) {
		return "content_policy"
	}
	if truthy(responseMetadata["capabilityUnavailable"]) {
		return "capability_limit"
	}
	return "refused"
}

func refusalType(err error) string {
	if err != nil {
		return "error"
	}
	return "refused"
}

func (h *Handler) sendThought(sessionID string, text string, heartbeatNumber int, elapsedSeconds int) {
	content := map[string]any{
		"type": "text",
		"text": text,
	}
	if heartbeatNumber > 0 {
		content["annotations"] = map[string]any{
			"_meta": map[string]any{
				"heartbeat":       true,
				"elapsedSeconds":  elapsedSeconds,
				"heartbeatNumber": heartbeatNumber,
			},
		}
	}

	h.notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"content":       content,
		},
	})
}

func (h *Handler) echoUserMessage(sessionID string, blocks []acp.ContentBlock) {
	if !h.processingConfig.EchoUserMessages {
		return
	}
	for _, block := range blocks {
		annotated := h.annotateContentBlock(block, h.getDefaultAnnotations(block.Type, true))
		h.notify("session/update", map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "user_message_chunk",
				"content":       annotated,
			},
		})
	}
}

func (h *Handler) sendAnnotatedAgentMessage(sessionID string, block acp.ContentBlock) {
	annotated := h.annotateContentBlock(block, h.getDefaultAnnotations(block.Type, false))
	h.notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       annotated,
		},
	})
}

func (h *Handler) sendRefusalExplanation(sessionID string, err error, stopData stopReasonData) {
	reason := "error"
	if value, ok := stopData.StopReasonDetails["reason"].(string); ok && strings.TrimSpace(value) != "" {
		reason = value
	}

	explanationText := "Unable to process your request: " + err.Error()
	lower := strings.ToLower(err.Error())

	if reason == "capability_unavailable" {
		if strings.Contains(lower, "not installed") || strings.Contains(lower, "not found") || strings.Contains(lower, "enoent") || strings.Contains(lower, "command not found") || strings.Contains(lower, "spawn cursor-agent enoent") {
			explanationText = "Unable to process your request because the cursor-agent CLI is not installed or not available in PATH.\n\nTo fix this, install cursor-agent CLI: https://cursor.sh/docs/agent"
		} else if strings.Contains(lower, "cursor cli error") {
			explanationText = "Unable to process your request because cursor-agent CLI is not authenticated.\n\nTo authenticate, run: `cursor-agent login`"
		} else {
			explanationText = "Unable to process your request because cursor-agent CLI is unavailable.\n\nPlease check that cursor-agent CLI is properly installed and accessible."
		}
	} else if reason == "authentication" {
		explanationText = "Unable to process your request because cursor-agent CLI is not authenticated.\n\nTo authenticate, run: `cursor-agent login`"
	}

	priority := 5
	confidence := 1.0
	block := acp.ContentBlock{
		Type: "text",
		Text: explanationText,
		Annotations: map[string]any{
			"_meta": map[string]any{
				"isError":   true,
				"category":  "error",
				"audience":  []string{"user"},
				"priority":  5,
				"errorType": reason,
				"severity":  "error",
			},
		},
	}
	annotated := h.annotateContentBlock(block, contentAnnotationOptions{
		Priority:   &priority,
		Audience:   []string{"user"},
		Confidence: &confidence,
		Source:     "cursor_agent",
		Category:   "text",
	})

	h.notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       annotated,
		},
	})

	h.logger.Debug("Sent refusal explanation to client", map[string]any{"sessionId": sessionID, "reason": reason})
}

func (h *Handler) annotateContentBlock(block acp.ContentBlock, opts contentAnnotationOptions) acp.ContentBlock {
	if !h.processingConfig.AnnotateContent {
		return block
	}

	annotations := map[string]any{}
	for k, v := range block.Annotations {
		annotations[k] = v
	}

	if len(opts.Audience) > 0 {
		annotations["audience"] = append([]string{}, opts.Audience...)
	} else if h.processingConfig.MarkInternalContent {
		annotations["audience"] = []string{"user"}
	}

	if opts.Priority != nil {
		priority := clampInt(*opts.Priority, 1, 5)
		annotations["priority"] = priority
	}

	annotations["lastModified"] = time.Now().UTC().Format(time.RFC3339)

	meta := map[string]any{}
	if existing, ok := annotations["_meta"].(map[string]any); ok {
		for k, v := range existing {
			meta[k] = v
		}
	}

	if opts.Confidence != nil {
		meta["confidence"] = clampFloat(*opts.Confidence, 0, 1)
	}
	if strings.TrimSpace(opts.Source) != "" {
		meta["source"] = opts.Source
	}
	if strings.TrimSpace(opts.Category) != "" {
		meta["category"] = opts.Category
	}
	if len(meta) > 0 {
		annotations["_meta"] = meta
	}

	block.Annotations = annotations
	return block
}

func (h *Handler) getDefaultAnnotations(blockType string, isUserContent bool) contentAnnotationOptions {
	options := contentAnnotationOptions{}
	if isUserContent {
		options.Source = "user_input"
		options.Audience = []string{"user", "assistant"}
	} else {
		options.Source = "cursor_agent"
		options.Audience = []string{"user"}
	}

	switch blockType {
	case "text":
		options.Category = "text"
	case "image":
		options.Category = "media"
	case "resource":
		options.Category = "resource"
	case "diff":
		options.Category = "code"
	default:
		options.Category = "other"
	}
	return options
}

func detectSlashCommand(blocks []acp.ContentBlock) (command string, input string, found bool) {
	for _, block := range blocks {
		if block.Type != "text" {
			continue
		}
		text := strings.TrimSpace(block.Text)
		if !strings.HasPrefix(text, "/") {
			continue
		}
		matches := slashCommandPattern.FindStringSubmatch(text)
		if len(matches) == 0 {
			continue
		}
		command = matches[1]
		if len(matches) > 2 {
			input = matches[2]
		}
		return command, input, true
	}
	return "", "", false
}

func (h *Handler) processSlashCommand(sessionID string, command string, input string) (bool, error) {
	if h.slash == nil {
		h.logger.Debug("Slash commands registry not available", nil)
		return false, nil
	}
	if !h.slash.HasCommand(command) {
		h.logger.Debug("Unknown slash command", map[string]any{"command": command})
		return false, nil
	}

	commandDef := h.slash.GetCommand(command)
	if commandDef == nil {
		return false, nil
	}

	h.logger.Debug("Detected slash command", map[string]any{"command": command, "input": input})
	h.logger.Info("Processing slash command", map[string]any{
		"sessionId":   sessionID,
		"command":     command,
		"input":       input,
		"description": commandDef.Description,
	})

	if command == "model" {
		return h.processModelCommand(sessionID, input)
	}

	h.logger.Debug("Slash command will be processed as part of prompt", map[string]any{"command": command, "input": input})
	return true, nil
}

func (h *Handler) processModelCommand(sessionID string, input string) (bool, error) {
	modelID := strings.TrimSpace(input)
	if modelID == "" {
		h.sendPlainAgentText(sessionID, "Error: Please specify a model ID. Usage: /model <model-id>")
		return false, nil
	}

	availableModels := h.sessions.GetAvailableModels()
	var model *acp.SessionModel
	for i := range availableModels {
		if availableModels[i].ID == modelID {
			model = &availableModels[i]
			break
		}
	}
	if model == nil {
		ids := make([]string, 0, len(availableModels))
		for _, m := range availableModels {
			ids = append(ids, m.ID)
		}
		sort.Strings(ids)
		h.sendPlainAgentText(sessionID, fmt.Sprintf("Error: Unknown model '%s'. Available models: %s", modelID, strings.Join(ids, ", ")))
		return false, nil
	}

	previousModel := h.sessions.GetSessionModel(sessionID)
	if _, err := h.sessions.SetSessionModel(sessionID, modelID); err != nil {
		h.sendPlainAgentText(sessionID, fmt.Sprintf("Error: Failed to change model: %s", err.Error()))
		return false, nil
	}

	h.sendPlainAgentText(sessionID, fmt.Sprintf("✓ Switched model from %s to %s (%s)", previousModel, modelID, model.Name))
	h.logger.Info("Model changed via /model command", map[string]any{"sessionId": sessionID, "previousModel": previousModel, "newModel": modelID})
	return true, nil
}

func (h *Handler) sendPlainAgentText(sessionID string, text string) {
	h.notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content": map[string]any{
				"type": "text",
				"text": text,
			},
		},
	})
}

func (h *Handler) sendPlanNotification(sessionID string, entries []map[string]any) {
	mapped := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{}
		if v, ok := entry["content"]; ok {
			item["content"] = v
		}
		if v, ok := entry["priority"]; ok {
			item["priority"] = v
		}
		if v, ok := entry["status"]; ok {
			item["status"] = v
		}
		if v, ok := entry["_meta"]; ok {
			item["_meta"] = v
		}
		mapped = append(mapped, item)
	}

	h.notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "plan",
			"entries":       mapped,
		},
		"_meta": map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		},
	})
}

func (h *Handler) calculateContentSize(blocks []acp.ContentBlock) int {
	total := 0
	for _, block := range blocks {
		total += getContentSize(block)
	}
	return total
}

func getContentSize(block acp.ContentBlock) int {
	switch block.Type {
	case "text":
		return len(block.Text)
	case "image", "audio":
		return len(block.Data)
	case "resource":
		if block.Resource == nil {
			return 0
		}
		if block.Resource.Text != "" {
			return len(block.Resource.Text)
		}
		return len(block.Resource.Blob)
	case "resource_link":
		return len(block.URI) + len(block.Name)
	default:
		return 0
	}
}

func clampInt(v int, low int, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func clampFloat(v float64, low float64, high float64) float64 {
	if math.IsNaN(v) {
		return low
	}
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func truthy(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(value))
		return trimmed != "" && trimmed != "false" && trimmed != "0"
	case int:
		return value != 0
	case int8:
		return value != 0
	case int16:
		return value != 0
	case int32:
		return value != 0
	case int64:
		return value != 0
	case uint:
		return value != 0
	case uint8:
		return value != 0
	case uint16:
		return value != 0
	case uint32:
		return value != 0
	case uint64:
		return value != 0
	case float32:
		return value != 0
	case float64:
		return value != 0
	default:
		return true
	}
}

func randomProcessingText() string {
	options := []string{
		"Crunching the numbers (and my will to live)...",
		"Hold on, consulting the magic 8-ball...",
		"Doing the thing...",
		"Asking the hamsters to run faster...",
		"Spinning up the chaos engines...",
		"Bribing the servers...",
		"Waking up the code gremlins...",
		"Sacrificing a rubber duck to the programming gods...",
		"Convincing the database to cooperate...",
		"Rolling the dice...",
		"Summoning the data from the void...",
		"Teaching the robots to behave...",
		"Turning it off and on again...",
		"Threatening the API with a timeout...",
		"Hoping this works...",
		"Doing some wizardry...",
		"Making the computers think harder...",
	}
	return options[rand.Intn(len(options))]
}

func messageID() string {
	return fmt.Sprintf("msg_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
}

func cloneMeta(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
