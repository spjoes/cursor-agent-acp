package terminal

import (
	"testing"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/client"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
	"github.com/spjoes/cursor-agent-acp/internal/toolcall"
)

type fakeConnection struct {
	createReq client.CreateTerminalRequest
	createErr error

	outputReq  client.TerminalOutputRequest
	outputResp client.TerminalOutputResponse
	outputErr  error

	waitReq   client.WaitForTerminalExitRequest
	waitResp  client.WaitForTerminalExitResponse
	waitErr   error
	waitDelay time.Duration

	killReq    client.KillTerminalRequest
	killErr    error
	killCalled bool

	releaseReq    client.ReleaseTerminalRequest
	releaseErr    error
	releaseCalled bool
}

func (f *fakeConnection) ReadTextFile(client.ReadTextFileRequest) (client.ReadTextFileResponse, error) {
	return client.ReadTextFileResponse{}, nil
}

func (f *fakeConnection) WriteTextFile(client.WriteTextFileRequest) (client.WriteTextFileResponse, error) {
	return client.WriteTextFileResponse{}, nil
}

func (f *fakeConnection) CreateTerminal(params client.CreateTerminalRequest) (client.CreateTerminalResponse, error) {
	f.createReq = params
	if f.createErr != nil {
		return client.CreateTerminalResponse{}, f.createErr
	}
	return client.CreateTerminalResponse{TerminalID: "term-1"}, nil
}

func (f *fakeConnection) GetTerminalOutput(params client.TerminalOutputRequest) (client.TerminalOutputResponse, error) {
	f.outputReq = params
	return f.outputResp, f.outputErr
}

func (f *fakeConnection) WaitForTerminalExit(params client.WaitForTerminalExitRequest) (client.WaitForTerminalExitResponse, error) {
	f.waitReq = params
	if f.waitDelay > 0 {
		time.Sleep(f.waitDelay)
	}
	return f.waitResp, f.waitErr
}

func (f *fakeConnection) KillTerminal(params client.KillTerminalRequest) error {
	f.killReq = params
	f.killCalled = true
	return f.killErr
}

func (f *fakeConnection) ReleaseTerminal(params client.ReleaseTerminalRequest) error {
	f.releaseReq = params
	f.releaseCalled = true
	return f.releaseErr
}

func TestCreateTerminalValidation(t *testing.T) {
	conn := &fakeConnection{}
	logger := logging.New("error")

	manager := NewManager(ManagerConfig{
		ClientSupportsTerminals: false,
	}, conn, logger)
	if _, err := manager.CreateTerminal("s1", CreateParams{Command: "echo"}); err == nil {
		t.Fatalf("expected error when client terminal capability is disabled")
	}

	manager = NewManager(ManagerConfig{
		ClientSupportsTerminals: true,
		ForbiddenCommands:       []string{"rm"},
	}, conn, logger)
	if _, err := manager.CreateTerminal("s1", CreateParams{Command: "rm"}); err == nil {
		t.Fatalf("expected forbidden command error")
	}

	manager = NewManager(ManagerConfig{
		ClientSupportsTerminals: true,
		AllowedCommands:         []string{"echo"},
	}, conn, logger)
	if _, err := manager.CreateTerminal("s1", CreateParams{Command: "ls"}); err == nil {
		t.Fatalf("expected allowed commands validation error")
	}
}

func TestExecuteSimpleCommand(t *testing.T) {
	exitCode := 0
	conn := &fakeConnection{
		outputResp: client.TerminalOutputResponse{
			Output:    "hello\n",
			Truncated: false,
		},
		waitResp: client.WaitForTerminalExitResponse{
			ExitCode: &exitCode,
		},
	}
	manager := NewManager(ManagerConfig{
		ClientSupportsTerminals: true,
	}, conn, logging.New("error"))

	result, err := ExecuteSimpleCommand(manager, "session-1", "echo", []string{"hello"}, nil)
	if err != nil {
		t.Fatalf("ExecuteSimpleCommand returned error: %v", err)
	}
	if result.Output != "hello\n" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if !conn.releaseCalled {
		t.Fatalf("expected terminal release to be called")
	}
	if conn.createReq.Command != "echo" {
		t.Fatalf("expected command echo, got %q", conn.createReq.Command)
	}
}

func TestExecuteWithTimeout(t *testing.T) {
	exitCode := 0
	conn := &fakeConnection{
		outputResp: client.TerminalOutputResponse{Output: "late"},
		waitResp:   client.WaitForTerminalExitResponse{ExitCode: &exitCode},
		waitDelay:  250 * time.Millisecond,
	}
	manager := NewManager(ManagerConfig{
		ClientSupportsTerminals: true,
	}, conn, logging.New("error"))

	result, err := ExecuteWithTimeout(manager, "session-1", "sleep", []string{"10"}, 40*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("ExecuteWithTimeout returned error: %v", err)
	}
	if !result.TimedOut {
		t.Fatalf("expected timedOut=true")
	}
	if !conn.killCalled {
		t.Fatalf("expected terminal kill call on timeout")
	}
}

func TestExecuteWithProgress(t *testing.T) {
	exitCode := 0
	conn := &fakeConnection{
		outputResp: client.TerminalOutputResponse{Output: "ok"},
		waitResp:   client.WaitForTerminalExitResponse{ExitCode: &exitCode},
	}
	manager := NewManager(ManagerConfig{
		ClientSupportsTerminals: true,
	}, conn, logging.New("error"))

	notifications := make([]map[string]any, 0)
	toolCalls := toolcall.NewManager(logging.New("error"), func(n map[string]any) {
		notifications = append(notifications, n)
	}, nil)

	result, err := ExecuteWithProgress(
		manager,
		toolCalls,
		"session-1",
		"echo",
		[]string{"ok"},
		ExecuteWithProgressOptions{Title: "Run echo", PollIntervalMs: 10},
	)
	if err != nil {
		t.Fatalf("ExecuteWithProgress returned error: %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(notifications) == 0 {
		t.Fatalf("expected tool call notifications to be emitted")
	}
}
