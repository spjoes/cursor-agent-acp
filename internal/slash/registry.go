package slash

import (
	"fmt"
	"sync"

	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type AvailableCommandInput struct {
	Hint string `json:"hint"`
}

type AvailableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
}

type ChangeCallback func(commands []AvailableCommand)

type Registry struct {
	logger *logging.Logger

	mu       sync.RWMutex
	commands map[string]AvailableCommand
	order    []string
	onChange ChangeCallback
}

func NewRegistry(logger *logging.Logger) *Registry {
	return &Registry{
		logger:   logger,
		commands: map[string]AvailableCommand{},
		order:    []string{},
	}
}

func ValidateCommand(command any) error {
	c, ok := command.(AvailableCommand)
	if !ok {
		if ptr, ok := command.(*AvailableCommand); ok && ptr != nil {
			c = *ptr
		} else {
			return fmt.Errorf("command must be an object")
		}
	}
	if c.Name == "" {
		return fmt.Errorf("command name must be a non-empty string")
	}
	if c.Description == "" {
		return fmt.Errorf("command description must be a non-empty string")
	}
	if c.Input != nil && c.Input.Hint == "" {
		return fmt.Errorf("command input.hint must be a non-empty string")
	}
	return nil
}

func IsValidCommand(command any) bool {
	return ValidateCommand(command) == nil
}

func (r *Registry) OnChange(cb ChangeCallback) {
	r.mu.Lock()
	r.onChange = cb
	r.mu.Unlock()
	r.logger.Debug("Commands change callback registered", nil)
}

func (r *Registry) notifyChange() {
	r.mu.RLock()
	cb := r.onChange
	commands := r.getCommandsNoLock()
	r.mu.RUnlock()
	if cb != nil {
		cb(commands)
	}
}

func (r *Registry) RegisterCommand(name string, description string, inputHint string) error {
	cmd := AvailableCommand{Name: name, Description: description}
	if inputHint != "" {
		cmd.Input = &AvailableCommandInput{Hint: inputHint}
	}
	if err := ValidateCommand(cmd); err != nil {
		return err
	}

	r.mu.Lock()
	if _, exists := r.commands[name]; !exists {
		r.order = append(r.order, name)
	}
	r.commands[name] = cmd
	r.mu.Unlock()

	r.logger.Debug("Registered slash command", map[string]any{"name": name, "description": description})
	r.notifyChange()
	return nil
}

func (r *Registry) UpdateCommands(commands []AvailableCommand) error {
	for _, c := range commands {
		if err := ValidateCommand(c); err != nil {
			return err
		}
	}

	r.mu.Lock()
	r.commands = map[string]AvailableCommand{}
	r.order = r.order[:0]
	for _, c := range commands {
		r.commands[c.Name] = c
		r.order = append(r.order, c.Name)
	}
	r.mu.Unlock()

	names := make([]string, 0, len(commands))
	for _, c := range commands {
		names = append(names, c.Name)
	}
	r.logger.Debug("Updated slash commands", map[string]any{"count": len(commands), "names": names})
	r.notifyChange()
	return nil
}

func (r *Registry) RemoveCommand(name string) {
	r.mu.Lock()
	_, removed := r.commands[name]
	if removed {
		delete(r.commands, name)
		newOrder := make([]string, 0, len(r.order))
		for _, v := range r.order {
			if v != name {
				newOrder = append(newOrder, v)
			}
		}
		r.order = newOrder
	}
	r.mu.Unlock()

	if removed {
		r.logger.Debug("Removed slash command", map[string]any{"name": name})
		r.notifyChange()
	}
}

func (r *Registry) HasCommand(name string) bool {
	r.mu.RLock()
	_, ok := r.commands[name]
	r.mu.RUnlock()
	return ok
}

func (r *Registry) GetCommand(name string) *AvailableCommand {
	r.mu.RLock()
	cmd, ok := r.commands[name]
	r.mu.RUnlock()
	if !ok {
		return nil
	}
	copy := cmd
	return &copy
}

func (r *Registry) GetCommands() []AvailableCommand {
	r.mu.RLock()
	out := r.getCommandsNoLock()
	r.mu.RUnlock()
	return out
}

func (r *Registry) getCommandsNoLock() []AvailableCommand {
	out := make([]AvailableCommand, 0, len(r.order))
	for _, name := range r.order {
		if cmd, ok := r.commands[name]; ok {
			out = append(out, cmd)
		}
	}
	return out
}

func (r *Registry) Clear() {
	r.mu.Lock()
	r.commands = map[string]AvailableCommand{}
	r.order = r.order[:0]
	r.mu.Unlock()
	r.logger.Debug("Cleared all slash commands", nil)
	r.notifyChange()
}

func (r *Registry) CommandCount() int {
	r.mu.RLock()
	n := len(r.commands)
	r.mu.RUnlock()
	return n
}

func (r *Registry) TriggerUpdate() {
	r.logger.Debug("Manually triggering commands update", nil)
	r.notifyChange()
}
