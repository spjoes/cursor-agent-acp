package session

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type ModelsProvider interface {
	ListModels() ([]acp.SessionModel, error)
}

type Manager struct {
	cfg    config.Config
	logger *logging.Logger

	mu         sync.RWMutex
	sessions   map[string]*acp.SessionData
	processing map[string]bool

	availableModes  []acp.SessionMode
	availableModels []acp.SessionModel

	cleanupTicker *time.Ticker
	stopCh        chan struct{}
}

func NewManager(cfg config.Config, logger *logging.Logger) *Manager {
	m := &Manager{
		cfg:        cfg,
		logger:     logger,
		sessions:   make(map[string]*acp.SessionData),
		processing: make(map[string]bool),
		availableModes: []acp.SessionMode{
			{ID: "agent", Name: "Agent", Description: "Write and modify code with full tool access"},
			{ID: "plan", Name: "Plan", Description: "Design and plan software systems without implementation"},
			{ID: "ask", Name: "Ask", Description: "Request permission before making any changes"},
		},
		availableModels: []acp.SessionModel{{ID: "auto", Name: "Auto", Provider: "cursor"}},
		stopCh:          make(chan struct{}),
	}

	m.startCleanupLoop()
	return m
}

func (m *Manager) Close() {
	if m.cleanupTicker != nil {
		m.cleanupTicker.Stop()
	}
	close(m.stopCh)
}

func (m *Manager) LoadModelsFromProvider(provider ModelsProvider) {
	models, err := provider.ListModels()
	if err != nil {
		m.logger.Warn("failed to load models from cursor-agent, using defaults", map[string]any{"error": err.Error()})
		return
	}
	if len(models) == 0 {
		return
	}

	m.mu.Lock()
	m.availableModels = models
	m.mu.Unlock()
}

func (m *Manager) GetAvailableModes() []acp.SessionMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]acp.SessionMode, len(m.availableModes))
	copy(out, m.availableModes)
	return out
}

func (m *Manager) GetAvailableModels() []acp.SessionModel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]acp.SessionModel, len(m.availableModels))
	copy(out, m.availableModels)
	return out
}

func (m *Manager) GetSessionModeState(sessionID string) *acp.SessionModeState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	current := "ask"
	if s, ok := m.sessions[sessionID]; ok {
		if s.State.CurrentMode != "" {
			current = s.State.CurrentMode
		}
	}
	modes := make([]acp.SessionMode, len(m.availableModes))
	copy(modes, m.availableModes)
	return &acp.SessionModeState{CurrentModeID: current, AvailableModes: modes}
}

func (m *Manager) GetSessionModelState(sessionID string) *acp.SessionModelState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	current := "auto"
	if s, ok := m.sessions[sessionID]; ok {
		if s.State.CurrentModel != "" {
			current = s.State.CurrentModel
		}
	}
	models := make([]acp.SessionModelEntry, 0, len(m.availableModels))
	for _, model := range m.availableModels {
		models = append(models, acp.SessionModelEntry{ModelID: model.ID, Name: model.Name})
	}
	return &acp.SessionModelState{AvailableModels: models, CurrentModelID: current}
}

func (m *Manager) HasSession(sessionID string) bool {
	m.mu.RLock()
	_, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if ok {
		return true
	}

	_, err := os.Stat(m.sessionPath(sessionID))
	return err == nil
}

func (m *Manager) CreateSession(metadata map[string]any) (*acp.SessionData, error) {
	if metadata == nil {
		metadata = map[string]any{}
	}

	m.mu.Lock()
	if len(m.sessions) >= m.cfg.MaxSessions {
		m.mu.Unlock()
		if _, err := m.CleanupExpiredSessions(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		if len(m.sessions) >= m.cfg.MaxSessions {
			m.mu.Unlock()
			return nil, errors.New("maximum number of sessions reached")
		}
	}

	now := time.Now().UTC()
	sessionID := randomID()
	name, _ := metadata["name"].(string)
	if strings.TrimSpace(name) == "" {
		name = "Session " + sessionID[:8]
	}
	mode := "ask"
	if v, ok := metadata["mode"].(string); ok && strings.TrimSpace(v) != "" {
		mode = v
	}
	model := "auto"
	if v, ok := metadata["model"].(string); ok && strings.TrimSpace(v) != "" {
		model = v
	}
	metadata["name"] = name
	metadata["mode"] = mode
	metadata["model"] = model

	s := &acp.SessionData{
		ID:           sessionID,
		Metadata:     metadata,
		Conversation: []acp.ConversationMessage{},
		State: acp.SessionState{
			LastActivity: now,
			MessageCount: 0,
			Status:       "active",
			CurrentMode:  mode,
			CurrentModel: model,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	m.sessions[sessionID] = s
	m.mu.Unlock()

	if err := m.persistSession(s); err != nil {
		return nil, err
	}

	copy := cloneSession(*s)
	return &copy, nil
}

func (m *Manager) LoadSession(sessionID string) (*acp.SessionData, error) {
	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		now := time.Now().UTC()
		s.State.LastActivity = now
		s.UpdatedAt = now
		copy := cloneSession(*s)
		m.mu.Unlock()
		return &copy, nil
	}
	m.mu.Unlock()

	s, err := m.loadSessionFromDisk(sessionID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	m.mu.Lock()
	if existing, ok := m.sessions[sessionID]; ok {
		now := time.Now().UTC()
		existing.State.LastActivity = now
		existing.UpdatedAt = now
		copy := cloneSession(*existing)
		m.mu.Unlock()
		return &copy, nil
	}
	now := time.Now().UTC()
	s.State.LastActivity = now
	s.UpdatedAt = now
	m.sessions[sessionID] = s
	copy := cloneSession(*s)
	m.mu.Unlock()
	return &copy, nil
}

func (m *Manager) UpdateSession(sessionID string, updates map[string]any) (*acp.SessionData, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		loaded, err := m.loadSessionFromDisk(sessionID)
		m.mu.Lock()
		if err != nil {
			return nil, err
		}
		if loaded == nil {
			return nil, fmt.Errorf("session not found: %s", sessionID)
		}
		s = loaded
		m.sessions[sessionID] = s
	}

	if updates != nil {
		for k, v := range updates {
			s.Metadata[k] = v
		}
	}
	now := time.Now().UTC()
	s.UpdatedAt = now
	s.State.LastActivity = now

	if err := m.persistSession(s); err != nil {
		return nil, err
	}

	copy := cloneSession(*s)
	return &copy, nil
}

func (m *Manager) DeleteSession(sessionID string) error {
	m.mu.Lock()
	delete(m.sessions, sessionID)
	delete(m.processing, sessionID)
	m.mu.Unlock()

	if err := os.Remove(m.sessionPath(sessionID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *Manager) AddMessage(sessionID string, msg acp.ConversationMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		loaded, err := m.loadSessionFromDisk(sessionID)
		m.mu.Lock()
		if err != nil {
			return err
		}
		if loaded == nil {
			return fmt.Errorf("session not found: %s", sessionID)
		}
		s = loaded
		m.sessions[sessionID] = s
	}

	s.Conversation = append(s.Conversation, msg)
	s.State.MessageCount = len(s.Conversation)
	now := time.Now().UTC()
	s.State.LastActivity = now
	s.UpdatedAt = now

	return m.persistSession(s)
}

func (m *Manager) ListSessions(limit int, offset int, filter map[string]any) ([]acp.SessionInfo, int, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	all, err := m.allSessions()
	if err != nil {
		return nil, 0, false, err
	}

	filtered := make([]acp.SessionData, 0, len(all))
	for _, s := range all {
		if matchesFilter(s, filter) {
			filtered = append(filtered, s)
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].State.LastActivity.After(filtered[j].State.LastActivity)
	})

	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}

	infos := make([]acp.SessionInfo, 0, end-offset)
	for _, s := range filtered[offset:end] {
		infos = append(infos, acp.SessionInfo{
			ID:        s.ID,
			Metadata:  cloneMetadata(s.Metadata),
			CreatedAt: s.CreatedAt,
			UpdatedAt: s.UpdatedAt,
			Status:    m.sessionStatus(s),
		})
	}
	return infos, total, end < total, nil
}

func (m *Manager) SetSessionMode(sessionID string, modeID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	valid := false
	for _, mode := range m.availableModes {
		if mode.ID == modeID {
			valid = true
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("invalid mode: %s", modeID)
	}

	s, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		loaded, err := m.loadSessionFromDisk(sessionID)
		m.mu.Lock()
		if err != nil {
			return "", err
		}
		if loaded == nil {
			return "", fmt.Errorf("session not found: %s", sessionID)
		}
		s = loaded
		m.sessions[sessionID] = s
	}

	prev := s.State.CurrentMode
	s.State.CurrentMode = modeID
	s.Metadata["mode"] = modeID
	now := time.Now().UTC()
	s.State.LastActivity = now
	s.UpdatedAt = now
	if err := m.persistSession(s); err != nil {
		return "", err
	}
	return prev, nil
}

func (m *Manager) SetSessionModel(sessionID string, modelID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	valid := false
	for _, model := range m.availableModels {
		if model.ID == modelID {
			valid = true
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("invalid model: %s", modelID)
	}

	s, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		loaded, err := m.loadSessionFromDisk(sessionID)
		m.mu.Lock()
		if err != nil {
			return "", err
		}
		if loaded == nil {
			return "", fmt.Errorf("session not found: %s", sessionID)
		}
		s = loaded
		m.sessions[sessionID] = s
	}

	prev := s.State.CurrentModel
	s.State.CurrentModel = modelID
	s.Metadata["model"] = modelID
	now := time.Now().UTC()
	s.State.LastActivity = now
	s.UpdatedAt = now
	if err := m.persistSession(s); err != nil {
		return "", err
	}
	return prev, nil
}

func (m *Manager) GetSessionMode(sessionID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[sessionID]; ok && s.State.CurrentMode != "" {
		return s.State.CurrentMode
	}
	return "ask"
}

func (m *Manager) GetSessionModel(sessionID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[sessionID]; ok && s.State.CurrentModel != "" {
		return s.State.CurrentModel
	}
	return "auto"
}

func (m *Manager) GetCursorChatID(sessionID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[sessionID]; ok {
		if chatID, ok := s.Metadata["cursorChatId"].(string); ok {
			return chatID
		}
	}
	return ""
}

func (m *Manager) SetCursorChatID(sessionID string, chatID string) error {
	_, err := m.UpdateSession(sessionID, map[string]any{"cursorChatId": chatID})
	return err
}

func (m *Manager) MarkProcessing(sessionID string) {
	m.mu.Lock()
	m.processing[sessionID] = true
	m.mu.Unlock()
}

func (m *Manager) UnmarkProcessing(sessionID string) {
	m.mu.Lock()
	delete(m.processing, sessionID)
	m.mu.Unlock()
}

func (m *Manager) IsProcessing(sessionID string) bool {
	m.mu.RLock()
	v := m.processing[sessionID]
	m.mu.RUnlock()
	return v
}

func (m *Manager) CleanupExpiredSessions() (int, error) {
	m.mu.RLock()
	ids := make([]string, 0)
	now := time.Now().UTC()
	for id, s := range m.sessions {
		if m.processing[id] {
			continue
		}
		if now.Sub(s.State.LastActivity) > time.Duration(m.cfg.SessionTimeout)*time.Millisecond {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()

	deleted := 0
	for _, id := range ids {
		if err := m.DeleteSession(id); err == nil {
			deleted++
		}
	}
	return deleted, nil
}

func (m *Manager) TouchSession(sessionID string) error {
	_, err := m.UpdateSession(sessionID, nil)
	return err
}

func (m *Manager) startCleanupLoop() {
	interval := m.cfg.SessionTimeout / 4
	if interval > 300_000 {
		interval = 300_000
	}
	if interval < 30_000 {
		interval = 30_000
	}

	m.cleanupTicker = time.NewTicker(time.Duration(interval) * time.Millisecond)
	go func() {
		for {
			select {
			case <-m.cleanupTicker.C:
				_, _ = m.CleanupExpiredSessions()
			case <-m.stopCh:
				return
			}
		}
	}()
}

func (m *Manager) sessionPath(sessionID string) string {
	return filepath.Join(m.cfg.SessionDir, sessionID+".json")
}

func (m *Manager) persistSession(s *acp.SessionData) error {
	if err := os.MkdirAll(m.cfg.SessionDir, 0o755); err != nil {
		return err
	}
	buf, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(m.sessionPath(s.ID), buf, 0o644)
}

func (m *Manager) loadSessionFromDisk(sessionID string) (*acp.SessionData, error) {
	buf, err := os.ReadFile(m.sessionPath(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s acp.SessionData
	if err := json.Unmarshal(buf, &s); err != nil {
		return nil, err
	}
	if s.Metadata == nil {
		s.Metadata = map[string]any{}
	}
	return &s, nil
}

func (m *Manager) allSessions() ([]acp.SessionData, error) {
	m.mu.RLock()
	result := make([]acp.SessionData, 0, len(m.sessions))
	seen := make(map[string]bool, len(m.sessions))
	for id, s := range m.sessions {
		result = append(result, cloneSession(*s))
		seen[id] = true
	}
	m.mu.RUnlock()

	entries, err := os.ReadDir(m.cfg.SessionDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), ".json")
		if seen[sessionID] {
			continue
		}
		s, err := m.loadSessionFromDisk(sessionID)
		if err != nil || s == nil {
			continue
		}
		result = append(result, cloneSession(*s))
	}

	return result, nil
}

func matchesFilter(s acp.SessionData, filter map[string]any) bool {
	if len(filter) == 0 {
		return true
	}
	for k, v := range filter {
		switch k {
		case "name":
			name, _ := s.Metadata["name"].(string)
			if !strings.Contains(strings.ToLower(name), strings.ToLower(fmt.Sprint(v))) {
				return false
			}
		case "tags":
			want := strings.ToLower(fmt.Sprint(v))
			ok := false
			slice, _ := s.Metadata["tags"].([]any)
			for _, item := range slice {
				if strings.ToLower(fmt.Sprint(item)) == want {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
		}
	}
	return true
}

func (m *Manager) sessionStatus(s acp.SessionData) string {
	delta := time.Since(s.State.LastActivity)
	timeout := time.Duration(m.cfg.SessionTimeout) * time.Millisecond
	if delta > timeout {
		return "expired"
	}
	if delta > timeout/2 {
		return "inactive"
	}
	return "active"
}

func randomID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	// RFC 4122 version 4 UUID to match JS uuid.v4() session IDs.
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func cloneSession(s acp.SessionData) acp.SessionData {
	copy := s
	copy.Metadata = cloneMetadata(s.Metadata)
	copy.Conversation = make([]acp.ConversationMessage, len(s.Conversation))
	for i := range s.Conversation {
		copy.Conversation[i] = cloneMessage(s.Conversation[i])
	}
	return copy
}

func cloneMessage(m acp.ConversationMessage) acp.ConversationMessage {
	copy := m
	copy.Metadata = cloneMetadata(m.Metadata)
	copy.Content = make([]acp.ContentBlock, len(m.Content))
	for i := range m.Content {
		copy.Content[i] = m.Content[i]
		if m.Content[i].Annotations != nil {
			copy.Content[i].Annotations = cloneMetadata(m.Content[i].Annotations)
		}
		if m.Content[i].Resource != nil {
			res := *m.Content[i].Resource
			copy.Content[i].Resource = &res
		}
	}
	return copy
}

func cloneMetadata(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (m *Manager) Compact() error {
	entries, err := os.ReadDir(m.cfg.SessionDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(m.cfg.SessionDir, e.Name())
		if _, err := os.Stat(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
