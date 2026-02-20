package cursor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type CommandOptions struct {
	Cwd     string
	Timeout time.Duration
	Env     []string
}

type CommandResult struct {
	Success  bool
	Stdout   string
	Stderr   string
	ExitCode int
	Error    string
}

type AuthStatus struct {
	Authenticated bool
	User          string
	Email         string
	Plan          string
	Error         string
}

type PromptOptions struct {
	SessionID string
	Content   string
	Metadata  map[string]any
	Ctx       context.Context
}

type PromptResult struct {
	Success  bool
	Text     string
	Raw      string
	Metadata map[string]any
	Error    string
}

type StreamChunk struct {
	Type string
	Data any
}

type StreamProgress struct {
	Step     string
	Current  int
	Progress int
	Total    int
	Message  string
}

type StreamingPromptOptions struct {
	SessionID  string
	Content    string
	Metadata   map[string]any
	Ctx        context.Context
	OnChunk    func(chunk StreamChunk) error
	OnProgress func(progress StreamProgress)
}

type StreamingPromptResult struct {
	Success  bool
	Text     string
	Raw      string
	Metadata map[string]any
	Error    string
	Chunks   int
	Aborted  bool
}

type Session struct {
	ID           string
	Status       string
	LastActivity time.Time
	Metadata     map[string]any
}

type Bridge struct {
	cfg    config.Config
	logger *logging.Logger

	mu             sync.Mutex
	activeSessions map[string]Session
}

func NewBridge(cfg config.Config, logger *logging.Logger) *Bridge {
	return &Bridge{
		cfg:            cfg,
		logger:         logger,
		activeSessions: map[string]Session{},
	}
}

func (b *Bridge) GetVersion() (string, error) {
	res, err := b.ExecuteCommand(context.Background(), []string{"--version"}, CommandOptions{})
	if err != nil {
		return "", err
	}
	if !res.Success {
		return "", errors.New(strings.TrimSpace(res.Error))
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return "unknown", nil
	}
	versionRegex := regexp.MustCompile(`\d+\.\d+\.\d+`)
	if match := versionRegex.FindString(out); match != "" {
		return match, nil
	}
	return out, nil
}

func (b *Bridge) CheckAuthentication() AuthStatus {
	res, err := b.ExecuteCommand(context.Background(), []string{"status"}, CommandOptions{})
	if err != nil {
		return AuthStatus{Authenticated: false, Error: err.Error()}
	}
	if !res.Success {
		return AuthStatus{Authenticated: false, Error: strings.TrimSpace(res.Error)}
	}

	out := stripANSI(res.Stdout)
	status := AuthStatus{}

	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		lower := strings.ToLower(line)
		if strings.Contains(lower, "logged in as") || strings.Contains(lower, "signed in as") {
			parts := strings.SplitN(line, "as", 2)
			if len(parts) == 2 {
				value := strings.TrimSpace(parts[1])
				if strings.Contains(value, "@") {
					status.Email = value
				} else {
					status.User = value
				}
			}
		}
		if strings.HasPrefix(lower, "user:") {
			status.User = strings.TrimSpace(strings.TrimPrefix(line, line[:5]))
		}
		if strings.HasPrefix(lower, "email:") {
			status.Email = strings.TrimSpace(strings.TrimPrefix(line, line[:6]))
		}
		if strings.HasPrefix(lower, "plan:") {
			status.Plan = strings.TrimSpace(strings.TrimPrefix(line, line[:5]))
		}
	}

	status.Authenticated = status.User != "" || status.Email != "" || strings.Contains(strings.ToLower(out), "signed in") || strings.Contains(strings.ToLower(out), "logged in")
	return status
}

func (b *Bridge) CreateChat(ctx context.Context) (string, error) {
	res, err := b.ExecuteCommand(ctx, []string{"create-chat"}, CommandOptions{})
	if err != nil {
		return "", err
	}
	if !res.Success {
		return "", errors.New(strings.TrimSpace(res.Error))
	}
	chat := strings.TrimSpace(res.Stdout)
	if chat == "" {
		return "", errors.New("cursor-agent create-chat returned empty chat ID")
	}
	return chat, nil
}

func (b *Bridge) ListModels() ([]acp.SessionModel, error) {
	res, err := b.ExecuteCommand(context.Background(), []string{"models"}, CommandOptions{})
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, errors.New(strings.TrimSpace(res.Error))
	}

	models := parseModelsOutput(res.Stdout)
	if len(models) == 0 {
		models = []acp.SessionModel{{ID: "auto", Name: "Auto", Provider: "cursor"}}
	}
	return models, nil
}

func (b *Bridge) SendPrompt(opts PromptOptions) (PromptResult, error) {
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	metadata := opts.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}

	cwd, _ := metadata["cwd"].(string)
	if cwd == "" {
		cwd = "."
	}
	model, _ := metadata["model"].(string)
	chatID, _ := metadata["cursorChatId"].(string)

	args := make([]string, 0, 12)
	if model != "" {
		args = append(args, "--model", model)
	}
	if chatID != "" {
		args = append(args, "--resume", chatID)
	}
	args = append(args,
		"--print",
		"--output-format", "json",
		"--force",
		opts.Content,
	)

	res, err := b.ExecuteCommand(ctx, args, CommandOptions{Cwd: cwd})
	if err != nil {
		return PromptResult{}, err
	}
	if !res.Success {
		return PromptResult{Success: false, Error: res.Error, Raw: res.Stdout}, nil
	}

	actualText := strings.TrimSpace(res.Stdout)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err == nil {
		for _, key := range []string{"result", "response", "content", "message"} {
			if v, ok := parsed[key]; ok {
				if str, ok := v.(string); ok && strings.TrimSpace(str) != "" {
					actualText = str
					break
				}
			}
		}
	}

	meta := map[string]any{
		"processedAt":   time.Now().UTC().Format(time.RFC3339),
		"contentLength": len(opts.Content),
	}
	for k, v := range metadata {
		meta[k] = v
	}

	return PromptResult{Success: true, Text: actualText, Raw: res.Stdout, Metadata: meta}, nil
}

func (b *Bridge) SendStreamingPrompt(opts StreamingPromptOptions) (StreamingPromptResult, error) {
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	metadata := opts.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}

	cwd, _ := metadata["cwd"].(string)
	if cwd == "" {
		cwd = "."
	}
	model, _ := metadata["model"].(string)
	chatID, _ := metadata["cursorChatId"].(string)

	args := []string{
		"agent",
		"--print",
		"--output-format", "stream-json",
		"--stream-partial-output",
		"--force",
		opts.Content,
	}
	if model != "" {
		args = append([]string{"--model", model}, args...)
	}
	if chatID != "" {
		args = append([]string{"--resume", chatID}, args...)
	}

	timeout := time.Duration(b.cfg.Cursor.Timeout) * time.Millisecond
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "cursor-agent", args...)
	cmd.Dir = cwd
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return StreamingPromptResult{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return StreamingPromptResult{}, err
	}

	rawBuilder := strings.Builder{}
	textBuilder := strings.Builder{}
	chunkCount := 0
	streamErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			if rawBuilder.Len() > 0 {
				rawBuilder.WriteByte('\n')
			}
			rawBuilder.WriteString(line)
			chunkCount++

			var payload any
			parsed := json.Unmarshal([]byte(line), &payload) == nil
			chunk := StreamChunk{Type: "content", Data: line}
			if parsed {
				chunk.Data = payload
				if m, ok := payload.(map[string]any); ok {
					for _, key := range []string{"result", "response", "content", "message"} {
						if value, ok := m[key].(string); ok && strings.TrimSpace(value) != "" {
							if textBuilder.Len() > 0 {
								textBuilder.WriteByte('\n')
							}
							textBuilder.WriteString(value)
							break
						}
					}
				}
			} else {
				if textBuilder.Len() > 0 {
					textBuilder.WriteByte('\n')
				}
				textBuilder.WriteString(line)
			}

			if opts.OnChunk != nil {
				if err := opts.OnChunk(chunk); err != nil {
					streamErr <- err
					return
				}
			}
			if opts.OnProgress != nil {
				opts.OnProgress(StreamProgress{
					Step:     "streaming",
					Current:  chunkCount,
					Progress: chunkCount,
					Message:  fmt.Sprintf("received chunk %d", chunkCount),
				})
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
			streamErr <- err
			return
		}
		streamErr <- nil
	}()

	waitErr := cmd.Wait()
	readErr := <-streamErr
	if readErr != nil {
		if opts.OnChunk != nil {
			_ = opts.OnChunk(StreamChunk{Type: "error", Data: readErr.Error()})
		}
		return StreamingPromptResult{}, readErr
	}

	if ctx.Err() != nil {
		if opts.OnChunk != nil {
			_ = opts.OnChunk(StreamChunk{Type: "error", Data: ctx.Err().Error()})
		}
		return StreamingPromptResult{
			Success:  false,
			Raw:      rawBuilder.String(),
			Text:     strings.TrimSpace(textBuilder.String()),
			Error:    ctx.Err().Error(),
			Metadata: metadataWithRuntime(metadata, opts.Content, chunkCount, true),
			Chunks:   chunkCount,
			Aborted:  true,
		}, nil
	}

	if waitErr != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = waitErr.Error()
		}
		if opts.OnChunk != nil {
			_ = opts.OnChunk(StreamChunk{Type: "error", Data: errMsg})
		}
		return StreamingPromptResult{
			Success:  false,
			Raw:      rawBuilder.String(),
			Text:     strings.TrimSpace(textBuilder.String()),
			Error:    errMsg,
			Metadata: metadataWithRuntime(metadata, opts.Content, chunkCount, true),
			Chunks:   chunkCount,
		}, nil
	}

	text := strings.TrimSpace(textBuilder.String())
	if text == "" {
		text = strings.TrimSpace(rawBuilder.String())
	}
	if opts.OnChunk != nil {
		if err := opts.OnChunk(StreamChunk{Type: "done", Data: map[string]any{"complete": true}}); err != nil {
			return StreamingPromptResult{}, err
		}
	}

	return StreamingPromptResult{
		Success:  true,
		Raw:      rawBuilder.String(),
		Text:     text,
		Metadata: metadataWithRuntime(metadata, opts.Content, chunkCount, true),
		Chunks:   chunkCount,
	}, nil
}

func (b *Bridge) StartInteractiveSession(sessionID string) (Session, error) {
	id := strings.TrimSpace(sessionID)
	if id == "" || id == "new" {
		var err error
		id, err = b.CreateChat(context.Background())
		if err != nil {
			return Session{}, err
		}
	}

	now := time.Now().UTC()
	session := Session{
		ID:           id,
		Status:       "active",
		LastActivity: now,
		Metadata: map[string]any{
			"created":      now.Format(time.RFC3339),
			"type":         "interactive",
			"cursorChatId": id,
		},
	}

	b.mu.Lock()
	b.activeSessions[id] = session
	b.mu.Unlock()
	return session, nil
}

func (b *Bridge) SendSessionInput(sessionID, input string) (string, error) {
	b.mu.Lock()
	session, ok := b.activeSessions[sessionID]
	if !ok {
		b.mu.Unlock()
		return "", fmt.Errorf("session not found: %s", sessionID)
	}
	session.LastActivity = time.Now().UTC()
	b.activeSessions[sessionID] = session
	b.mu.Unlock()

	// Placeholder behavior preserved from JS implementation.
	return "Processed: " + input, nil
}

func (b *Bridge) CloseSession(sessionID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.activeSessions[sessionID]; !ok {
		return nil
	}
	delete(b.activeSessions, sessionID)
	return nil
}

func (b *Bridge) GetActiveSessions() []Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Session, 0, len(b.activeSessions))
	for _, session := range b.activeSessions {
		out = append(out, session)
	}
	return out
}

func (b *Bridge) Close() error {
	b.mu.Lock()
	b.activeSessions = map[string]Session{}
	b.mu.Unlock()
	return nil
}

func (b *Bridge) ExecuteCommand(parent context.Context, args []string, options CommandOptions) (CommandResult, error) {
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = time.Duration(b.cfg.Cursor.Timeout) * time.Millisecond
	}

	attempts := b.cfg.Cursor.Retries + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		res, err := b.executeSingle(ctx, args, options, timeout)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if attempt < attempts {
			backoff := time.Duration(minInt(1<<(attempt-1), 5)) * time.Second
			select {
			case <-ctx.Done():
				return CommandResult{}, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}

	return CommandResult{}, fmt.Errorf("cursor-agent command failed after %d attempts: %w", attempts, lastErr)
}

func (b *Bridge) executeSingle(parent context.Context, args []string, options CommandOptions, timeout time.Duration) (CommandResult, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "cursor-agent", args...)
	if options.Cwd != "" {
		cmd.Dir = options.Cwd
	}
	if len(options.Env) > 0 {
		cmd.Env = append(cmd.Env, options.Env...)
	}

	stdout, err := cmd.Output()
	if err == nil {
		return CommandResult{Success: true, Stdout: string(stdout), ExitCode: 0}, nil
	}

	res := CommandResult{Success: false}
	if exitErr := new(exec.ExitError); errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		res.Stderr = string(exitErr.Stderr)
		res.Error = strings.TrimSpace(res.Stderr)
		if res.Error == "" {
			res.Error = exitErr.Error()
		}
		return res, nil
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CommandResult{}, fmt.Errorf("command timed out after %s", timeout)
	}
	return CommandResult{}, err
}

func parseModelsOutput(output string) []acp.SessionModel {
	models := make([]acp.SessionModel, 0)
	scanner := bufio.NewScanner(strings.NewReader(output))
	inList := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.Contains(strings.ToLower(line), "available models") {
			inList = true
			continue
		}
		if !inList || strings.HasPrefix(strings.ToLower(line), "tip:") {
			continue
		}

		dash := strings.Index(line, " - ")
		if dash < 0 {
			continue
		}
		id := strings.TrimSpace(line[:dash])
		name := strings.TrimSpace(line[dash+3:])
		name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(name, "(current)"), "(default)"))
		if id == "" || name == "" {
			continue
		}

		models = append(models, acp.SessionModel{
			ID:       id,
			Name:     name,
			Provider: inferProvider(id),
		})
	}

	autoFound := false
	for _, m := range models {
		if m.ID == "auto" {
			autoFound = true
			break
		}
	}
	if !autoFound {
		models = append([]acp.SessionModel{{ID: "auto", Name: "Auto", Provider: "cursor"}}, models...)
	}

	return models
}

func inferProvider(modelID string) string {
	id := strings.ToLower(modelID)
	switch {
	case strings.Contains(id, "gpt") || strings.Contains(id, "codex"):
		return "openai"
	case strings.Contains(id, "opus") || strings.Contains(id, "sonnet") || strings.Contains(id, "claude"):
		return "anthropic"
	case strings.Contains(id, "gemini"):
		return "google"
	case strings.Contains(id, "grok"):
		return "xai"
	case id == "auto" || strings.Contains(id, "composer"):
		return "cursor"
	default:
		return "unknown"
	}
}

func stripANSI(in string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]|\x1b\].*?\x07`)
	return ansi.ReplaceAllString(in, "")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func metadataWithRuntime(metadata map[string]any, content string, chunks int, streaming bool) map[string]any {
	out := map[string]any{
		"processedAt":   time.Now().UTC().Format(time.RFC3339),
		"contentLength": len(content),
		"streaming":     streaming,
		"chunks":        chunks,
	}
	for k, v := range metadata {
		out[k] = v
	}
	return out
}

func ParseExitCode(text string) int {
	text = strings.TrimSpace(text)
	i, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return i
}
