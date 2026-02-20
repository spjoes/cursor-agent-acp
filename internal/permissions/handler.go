package permissions

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/jsonrpc"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  map[string]any     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

type pendingPermission struct {
	sessionID string
	resolve   func(PermissionOutcome)
	timer     *time.Timer
}

type Handler struct {
	logger *logging.Logger

	mu      sync.Mutex
	pending map[string]*pendingPermission
}

func NewHandler(logger *logging.Logger) *Handler {
	return &Handler{logger: logger, pending: map[string]*pendingPermission{}}
}

func (h *Handler) CreatePermissionRequest(params RequestPermissionParams) <-chan PermissionOutcome {
	requestID := fmt.Sprintf("perm_%d", time.Now().UnixNano())
	out := make(chan PermissionOutcome, 1)

	h.mu.Lock()
	pp := &pendingPermission{sessionID: params.SessionID}
	pp.resolve = func(o PermissionOutcome) {
		select {
		case out <- o:
		default:
		}
		close(out)
	}
	pp.timer = time.AfterFunc(5*time.Minute, func() {
		h.mu.Lock()
		delete(h.pending, requestID)
		h.mu.Unlock()
		h.logger.Warn("Permission request timed out", map[string]any{"requestId": requestID, "sessionId": params.SessionID})
		pp.resolve(PermissionOutcome{Outcome: "selected", OptionID: "reject-once"})
	})
	h.pending[requestID] = pp
	h.mu.Unlock()

	return out
}

func (h *Handler) HandlePermissionRequest(req jsonrpc.Request) (jsonrpc.Response, error) {
	var params RequestPermissionParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return jsonrpc.Response{}, err
		}
	}

	if params.SessionID == "" {
		return jsonrpc.Response{}, fmt.Errorf("sessionId is required and must be a string")
	}
	if params.ToolCall == nil {
		return jsonrpc.Response{}, fmt.Errorf("toolCall is required and must be an object")
	}
	if len(params.Options) == 0 {
		return jsonrpc.Response{}, fmt.Errorf("options is required and must be a non-empty array")
	}
	for _, option := range params.Options {
		if !isValidOption(option) {
			return jsonrpc.Response{}, fmt.Errorf("invalid permission option: %+v", option)
		}
	}

	kind, _ := params.ToolCall["kind"].(string)
	outcome := defaultOutcome(kind, params.Options)

	return jsonrpc.Success(req.ID, map[string]any{"outcome": outcome}), nil
}

func defaultOutcome(kind string, options []PermissionOption) PermissionOutcome {
	allow := firstOptionByKind(options, "allow_once")
	reject := firstOptionByKind(options, "reject_once")

	switch kind {
	case "read", "search", "think", "fetch":
		if allow != nil {
			return PermissionOutcome{Outcome: "selected", OptionID: allow.OptionID}
		}
	case "edit", "delete", "execute", "move":
		if reject != nil {
			return PermissionOutcome{Outcome: "selected", OptionID: reject.OptionID}
		}
	}
	first := options[0]
	return PermissionOutcome{Outcome: "selected", OptionID: first.OptionID}
}

func firstOptionByKind(options []PermissionOption, kind string) *PermissionOption {
	for _, o := range options {
		if o.Kind == kind {
			copy := o
			return &copy
		}
	}
	return nil
}

func isValidOption(o PermissionOption) bool {
	if o.OptionID == "" || o.Name == "" {
		return false
	}
	switch o.Kind {
	case "allow_once", "allow_always", "reject_once", "reject_always":
		return true
	default:
		return false
	}
}

func (h *Handler) ResolvePermissionRequest(requestID string, outcome PermissionOutcome) bool {
	h.mu.Lock()
	pp, ok := h.pending[requestID]
	if ok {
		delete(h.pending, requestID)
	}
	h.mu.Unlock()
	if !ok {
		h.logger.Warn("Permission request not found", map[string]any{"requestId": requestID})
		return false
	}
	if pp.timer != nil {
		pp.timer.Stop()
	}
	pp.resolve(outcome)
	return true
}

func (h *Handler) CancelSessionPermissionRequests(sessionID string) {
	h.mu.Lock()
	ids := make([]string, 0)
	for id, pending := range h.pending {
		if pending.sessionID == sessionID {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		pending := h.pending[id]
		delete(h.pending, id)
		if pending.timer != nil {
			pending.timer.Stop()
		}
		pending.resolve(PermissionOutcome{Outcome: "cancelled"})
	}
	h.mu.Unlock()
	h.logger.Debug("Session permission requests cancelled", map[string]any{"sessionId": sessionID, "count": len(ids)})
}

func (h *Handler) Metrics() map[string]any {
	h.mu.Lock()
	n := len(h.pending)
	h.mu.Unlock()
	return map[string]any{"pendingRequests": n}
}

func (h *Handler) Cleanup() {
	h.mu.Lock()
	pending := h.pending
	h.pending = map[string]*pendingPermission{}
	h.mu.Unlock()
	for _, p := range pending {
		if p.timer != nil {
			p.timer.Stop()
		}
		p.resolve(PermissionOutcome{Outcome: "cancelled"})
	}
}
