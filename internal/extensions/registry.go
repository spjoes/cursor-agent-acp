package extensions

import (
	"fmt"
	"sync"

	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type MethodHandler func(params map[string]any) (map[string]any, error)
type NotificationHandler func(params map[string]any) error

type Registry struct {
	logger *logging.Logger

	mu            sync.RWMutex
	methods       map[string]MethodHandler
	notifications map[string]NotificationHandler
}

func NewRegistry(logger *logging.Logger) *Registry {
	return &Registry{
		logger:        logger,
		methods:       map[string]MethodHandler{},
		notifications: map[string]NotificationHandler{},
	}
}

func (r *Registry) validName(name string) bool {
	return len(name) > 0 && name[0] == '_'
}

func (r *Registry) RegisterMethod(name string, handler MethodHandler) error {
	if !r.validName(name) {
		return fmt.Errorf("extension method name must start with underscore: %s", name)
	}
	if handler == nil {
		return fmt.Errorf("extension method handler must be a function")
	}
	r.mu.Lock()
	r.methods[name] = handler
	r.mu.Unlock()
	r.logger.Debug("Registered extension method", map[string]any{"name": name})
	return nil
}

func (r *Registry) RegisterNotification(name string, handler NotificationHandler) error {
	if !r.validName(name) {
		return fmt.Errorf("extension notification name must start with underscore: %s", name)
	}
	if handler == nil {
		return fmt.Errorf("extension notification handler must be a function")
	}
	r.mu.Lock()
	r.notifications[name] = handler
	r.mu.Unlock()
	r.logger.Debug("Registered extension notification", map[string]any{"name": name})
	return nil
}

func (r *Registry) HasMethod(name string) bool {
	r.mu.RLock()
	_, ok := r.methods[name]
	r.mu.RUnlock()
	return ok
}

func (r *Registry) HasNotification(name string) bool {
	r.mu.RLock()
	_, ok := r.notifications[name]
	r.mu.RUnlock()
	return ok
}

func (r *Registry) CallMethod(name string, params map[string]any) (map[string]any, error) {
	r.mu.RLock()
	h, ok := r.methods[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("extension method not found: %s", name)
	}
	r.logger.Debug("Calling extension method", map[string]any{"name": name, "params": params})
	res, err := h(params)
	if err != nil {
		r.logger.Error("Extension method error", map[string]any{"name": name, "error": err.Error()})
		return nil, err
	}
	r.logger.Debug("Extension method completed", map[string]any{"name": name})
	return res, nil
}

func (r *Registry) SendNotification(name string, params map[string]any) {
	r.mu.RLock()
	h, ok := r.notifications[name]
	r.mu.RUnlock()
	if !ok {
		r.logger.Debug("Unrecognized extension notification ignored", map[string]any{"name": name})
		return
	}
	r.logger.Debug("Sending extension notification", map[string]any{"name": name, "params": params})
	if err := h(params); err != nil {
		r.logger.Warn("Extension notification handler error", map[string]any{"name": name, "error": err.Error()})
		return
	}
	r.logger.Debug("Extension notification handled", map[string]any{"name": name})
}

func (r *Registry) RegisteredMethods() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.methods))
	for name := range r.methods {
		out = append(out, name)
	}
	return out
}

func (r *Registry) RegisteredNotifications() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.notifications))
	for name := range r.notifications {
		out = append(out, name)
	}
	return out
}

func (r *Registry) UnregisterMethod(name string) {
	r.mu.Lock()
	_, removed := r.methods[name]
	if removed {
		delete(r.methods, name)
	}
	r.mu.Unlock()
	if removed {
		r.logger.Debug("Unregistered extension method", map[string]any{"name": name})
	}
}

func (r *Registry) UnregisterNotification(name string) {
	r.mu.Lock()
	_, removed := r.notifications[name]
	if removed {
		delete(r.notifications, name)
	}
	r.mu.Unlock()
	if removed {
		r.logger.Debug("Unregistered extension notification", map[string]any{"name": name})
	}
}

func (r *Registry) Clear() {
	r.mu.Lock()
	r.methods = map[string]MethodHandler{}
	r.notifications = map[string]NotificationHandler{}
	r.mu.Unlock()
	r.logger.Debug("Cleared all extension methods and notifications", nil)
}

func (r *Registry) MethodCount() int {
	r.mu.RLock()
	n := len(r.methods)
	r.mu.RUnlock()
	return n
}

func (r *Registry) NotificationCount() int {
	r.mu.RLock()
	n := len(r.notifications)
	r.mu.RUnlock()
	return n
}
