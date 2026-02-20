package terminal

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/client"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type ManagerConfig struct {
	ClientSupportsTerminals bool
	MaxConcurrentTerminals  int
	DefaultOutputByteLimit  int
	MaxOutputByteLimit      int
	ForbiddenCommands       []string
	AllowedCommands         []string
	DefaultCwd              string
	DefaultEnv              []client.EnvVariable
}

type TerminalMetadata struct {
	ID           string
	SessionID    string
	Command      string
	Args         []string
	CreatedAt    time.Time
	LastActivity time.Time
}

type CreateParams struct {
	Command         string
	Args            []string
	Cwd             string
	Env             []client.EnvVariable
	OutputByteLimit int
}

type Handle struct {
	TerminalID string
	manager    *Manager

	mu       sync.Mutex
	released bool
}

func (h *Handle) CurrentOutput() (client.TerminalOutputResponse, error) {
	return h.manager.conn.GetTerminalOutput(client.TerminalOutputRequest{TerminalID: h.TerminalID})
}

func (h *Handle) WaitForExit() (client.WaitForTerminalExitResponse, error) {
	return h.manager.conn.WaitForTerminalExit(client.WaitForTerminalExitRequest{TerminalID: h.TerminalID})
}

func (h *Handle) Kill() error {
	return h.manager.conn.KillTerminal(client.KillTerminalRequest{TerminalID: h.TerminalID})
}

func (h *Handle) Release() error {
	h.mu.Lock()
	if h.released {
		h.mu.Unlock()
		return nil
	}
	h.released = true
	h.mu.Unlock()

	defer h.manager.ReleaseTerminal(h.TerminalID)
	return h.manager.conn.ReleaseTerminal(client.ReleaseTerminalRequest{TerminalID: h.TerminalID})
}

type Manager struct {
	cfg    ManagerConfig
	conn   client.Connection
	logger *logging.Logger

	mu      sync.Mutex
	termMap map[string]TerminalMetadata
}

func NewManager(cfg ManagerConfig, conn client.Connection, logger *logging.Logger) *Manager {
	if cfg.MaxConcurrentTerminals <= 0 {
		cfg.MaxConcurrentTerminals = 5
	}
	return &Manager{
		cfg:     cfg,
		conn:    conn,
		logger:  logger,
		termMap: map[string]TerminalMetadata{},
	}
}

func (m *Manager) CanCreateTerminals() bool {
	return m.cfg.ClientSupportsTerminals
}

func (m *Manager) CreateTerminal(sessionID string, params CreateParams) (*Handle, error) {
	if !m.cfg.ClientSupportsTerminals {
		return nil, fmt.Errorf("client does not support terminal operations")
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	if strings.TrimSpace(params.Command) == "" {
		return nil, fmt.Errorf("command is required")
	}
	if err := m.validateCommand(params.Command); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if len(m.termMap) >= m.cfg.MaxConcurrentTerminals {
		m.mu.Unlock()
		return nil, fmt.Errorf("maximum concurrent terminals reached (%d)", m.cfg.MaxConcurrentTerminals)
	}
	m.mu.Unlock()

	outputLimit, err := m.validateOutputByteLimit(params.OutputByteLimit)
	if err != nil {
		return nil, err
	}

	cwd := params.Cwd
	if strings.TrimSpace(cwd) == "" {
		cwd = m.cfg.DefaultCwd
	}
	env := params.Env
	if len(env) == 0 && len(m.cfg.DefaultEnv) > 0 {
		env = append(env, m.cfg.DefaultEnv...)
	}

	resp, err := m.conn.CreateTerminal(client.CreateTerminalRequest{
		SessionID:       sessionID,
		Command:         params.Command,
		Args:            params.Args,
		Cwd:             cwd,
		Env:             env,
		OutputByteLimit: outputLimit,
	})
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	m.mu.Lock()
	m.termMap[resp.TerminalID] = TerminalMetadata{
		ID:           resp.TerminalID,
		SessionID:    sessionID,
		Command:      params.Command,
		Args:         append([]string{}, params.Args...),
		CreatedAt:    now,
		LastActivity: now,
	}
	m.mu.Unlock()

	m.logger.Debug("Terminal created", map[string]any{
		"terminalId": resp.TerminalID,
		"sessionId":  sessionID,
		"command":    params.Command,
	})

	return &Handle{
		TerminalID: resp.TerminalID,
		manager:    m,
	}, nil
}

func (m *Manager) ReleaseTerminal(terminalID string) {
	m.mu.Lock()
	delete(m.termMap, terminalID)
	m.mu.Unlock()
}

func (m *Manager) UpdateActivity(terminalID string) {
	m.mu.Lock()
	meta, ok := m.termMap[terminalID]
	if ok {
		meta.LastActivity = time.Now().UTC()
		m.termMap[terminalID] = meta
	}
	m.mu.Unlock()
}

func (m *Manager) ActiveTerminals() []TerminalMetadata {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TerminalMetadata, 0, len(m.termMap))
	for _, v := range m.termMap {
		out = append(out, v)
	}
	return out
}

func (m *Manager) Cleanup() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.termMap))
	for id := range m.termMap {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		_ = m.conn.ReleaseTerminal(client.ReleaseTerminalRequest{TerminalID: id})
		m.ReleaseTerminal(id)
	}
}

func (m *Manager) validateCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("command cannot be empty")
	}
	cmdLower := strings.ToLower(command)

	if len(m.cfg.AllowedCommands) > 0 {
		allowed := false
		for _, c := range m.cfg.AllowedCommands {
			if strings.ToLower(strings.TrimSpace(c)) == cmdLower {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("command %q is not in allowed command list", command)
		}
	}

	for _, forbidden := range m.cfg.ForbiddenCommands {
		if strings.ToLower(strings.TrimSpace(forbidden)) == cmdLower {
			return fmt.Errorf("command %q is forbidden", command)
		}
	}
	return nil
}

func (m *Manager) validateOutputByteLimit(outputByteLimit int) (int, error) {
	if outputByteLimit <= 0 {
		if m.cfg.DefaultOutputByteLimit > 0 {
			return m.cfg.DefaultOutputByteLimit, nil
		}
		return 0, nil
	}
	if m.cfg.MaxOutputByteLimit > 0 && outputByteLimit > m.cfg.MaxOutputByteLimit {
		return 0, fmt.Errorf("outputByteLimit exceeds maximum (%d)", m.cfg.MaxOutputByteLimit)
	}
	return outputByteLimit, nil
}
