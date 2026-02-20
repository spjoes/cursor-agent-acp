package terminal

import (
	"fmt"
	"strings"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/client"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
	"github.com/spjoes/cursor-agent-acp/internal/toolcall"
)

type SimpleCommandResult struct {
	Output    string
	ExitCode  *int
	Signal    *string
	Truncated bool
}

type TimeoutCommandResult struct {
	Output    string
	ExitCode  *int
	Signal    *string
	Truncated bool
	TimedOut  bool
}

type ExecuteWithProgressOptions struct {
	Title           string
	Cwd             string
	Env             []client.EnvVariable
	OutputByteLimit int
	PollIntervalMs  int
}

func ExecuteSimpleCommand(manager *Manager, sessionID string, command string, args []string, options *CreateParams) (SimpleCommandResult, error) {
	params := CreateParams{
		Command: command,
		Args:    args,
	}
	if options != nil {
		params.Cwd = options.Cwd
		params.Env = options.Env
		params.OutputByteLimit = options.OutputByteLimit
	}

	terminal, err := manager.CreateTerminal(sessionID, params)
	if err != nil {
		return SimpleCommandResult{}, err
	}
	defer func() { _ = terminal.Release() }()

	exit, err := terminal.WaitForExit()
	if err != nil {
		return SimpleCommandResult{}, err
	}
	output, err := terminal.CurrentOutput()
	if err != nil {
		return SimpleCommandResult{}, err
	}

	return SimpleCommandResult{
		Output:    output.Output,
		ExitCode:  exit.ExitCode,
		Signal:    exit.Signal,
		Truncated: output.Truncated,
	}, nil
}

func ExecuteWithTimeout(manager *Manager, sessionID string, command string, args []string, timeout time.Duration, options *CreateParams) (TimeoutCommandResult, error) {
	params := CreateParams{
		Command: command,
		Args:    args,
	}
	if options != nil {
		params.Cwd = options.Cwd
		params.Env = options.Env
		params.OutputByteLimit = options.OutputByteLimit
	}

	terminal, err := manager.CreateTerminal(sessionID, params)
	if err != nil {
		return TimeoutCommandResult{}, err
	}
	defer func() { _ = terminal.Release() }()

	exitCh := make(chan client.WaitForTerminalExitResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		exit, err := terminal.WaitForExit()
		if err != nil {
			errCh <- err
			return
		}
		exitCh <- exit
	}()

	var timedOut bool
	var exitStatus client.WaitForTerminalExitResponse
	select {
	case err := <-errCh:
		return TimeoutCommandResult{}, err
	case exit := <-exitCh:
		exitStatus = exit
	case <-time.After(timeout):
		timedOut = true
		_ = terminal.Kill()
		select {
		case exit := <-exitCh:
			exitStatus = exit
		case <-time.After(150 * time.Millisecond):
		}
	}

	output, err := terminal.CurrentOutput()
	if err != nil {
		return TimeoutCommandResult{}, err
	}

	return TimeoutCommandResult{
		Output:    output.Output,
		ExitCode:  exitStatus.ExitCode,
		Signal:    exitStatus.Signal,
		Truncated: output.Truncated,
		TimedOut:  timedOut,
	}, nil
}

func ExecuteWithProgress(
	manager *Manager,
	toolCalls *toolcall.Manager,
	sessionID string,
	command string,
	args []string,
	options ExecuteWithProgressOptions,
) (SimpleCommandResult, error) {
	terminal, err := manager.CreateTerminal(sessionID, CreateParams{
		Command:         command,
		Args:            args,
		Cwd:             options.Cwd,
		Env:             options.Env,
		OutputByteLimit: options.OutputByteLimit,
	})
	if err != nil {
		return SimpleCommandResult{}, err
	}
	defer func() { _ = terminal.Release() }()

	title := options.Title
	if title == "" {
		title = fmt.Sprintf("$ %s %s", command, joinArgs(args))
	}

	toolCallID := ""
	if toolCalls != nil {
		toolCallID = toolCalls.ReportToolCall(sessionID, "execute_command", map[string]any{
			"title":    title,
			"kind":     "execute",
			"status":   "pending",
			"rawInput": map[string]any{"command": command, "args": args, "cwd": options.Cwd},
		})
		toolCalls.UpdateToolCall(sessionID, toolCallID, map[string]any{
			"status":  "in_progress",
			"content": toolCalls.CreateTerminalContent(terminal.TerminalID),
		})
	}

	if options.PollIntervalMs <= 0 {
		options.PollIntervalMs = 1000
	}
	stopPoll := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Duration(options.PollIntervalMs) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				manager.UpdateActivity(terminal.TerminalID)
			case <-stopPoll:
				return
			}
		}
	}()

	exitStatus, err := terminal.WaitForExit()
	close(stopPoll)
	if err != nil {
		if toolCalls != nil && toolCallID != "" {
			toolCalls.FailToolCall(sessionID, toolCallID, map[string]any{
				"title": "Command execution failed",
				"error": err.Error(),
			})
		}
		return SimpleCommandResult{}, err
	}

	output, err := terminal.CurrentOutput()
	if err != nil {
		return SimpleCommandResult{}, err
	}

	if toolCalls != nil && toolCallID != "" {
		if exitStatus.ExitCode != nil && *exitStatus.ExitCode == 0 {
			toolCalls.CompleteToolCall(sessionID, toolCallID, map[string]any{
				"title":   "Command completed successfully",
				"content": toolCalls.CreateTerminalContent(terminal.TerminalID),
				"rawOutput": map[string]any{
					"exitCode":     exitStatus.ExitCode,
					"outputLength": len(output.Output),
					"truncated":    output.Truncated,
				},
			})
		} else {
			toolCalls.FailToolCall(sessionID, toolCallID, map[string]any{
				"title": fmt.Sprintf("Command failed (exit code %v)", exitStatus.ExitCode),
				"error": fmt.Sprintf("Command exited with code %v", exitStatus.ExitCode),
				"rawOutput": map[string]any{
					"exitCode":     exitStatus.ExitCode,
					"signal":       exitStatus.Signal,
					"outputLength": len(output.Output),
					"truncated":    output.Truncated,
				},
			})
		}
	}

	return SimpleCommandResult{
		Output:    output.Output,
		ExitCode:  exitStatus.ExitCode,
		Signal:    exitStatus.Signal,
		Truncated: output.Truncated,
	}, nil
}

func ExecuteSequential(
	manager *Manager,
	sessionID string,
	cwd string,
	commands []CreateParams,
	stopOnError bool,
) ([]SimpleCommandResult, error) {
	results := make([]SimpleCommandResult, 0, len(commands))
	for _, cmd := range commands {
		cmd.Cwd = cwd
		result, err := ExecuteSimpleCommand(manager, sessionID, cmd.Command, cmd.Args, &cmd)
		if err != nil {
			return results, err
		}
		results = append(results, result)
		if stopOnError && result.ExitCode != nil && *result.ExitCode != 0 {
			break
		}
	}
	return results, nil
}

func StreamTerminalOutput(handle *Handle, logger *logging.Logger, onOutput func(output string, isComplete bool), pollInterval time.Duration) (SimpleCommandResult, error) {
	if pollInterval <= 0 {
		pollInterval = time.Second
	}

	lastLen := 0
	running := true
	donePoll := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		defer close(donePoll)
		for running {
			<-ticker.C
			out, err := handle.CurrentOutput()
			if err != nil {
				logger.Warn("Error polling terminal output", map[string]any{"error": err.Error()})
				running = false
				return
			}
			if len(out.Output) > lastLen {
				onOutput(out.Output[lastLen:], false)
				lastLen = len(out.Output)
			}
			if out.ExitStatus != nil {
				running = false
				onOutput("", true)
				return
			}
		}
	}()

	exit, err := handle.WaitForExit()
	if err != nil {
		return SimpleCommandResult{}, err
	}
	running = false
	<-donePoll

	finalOutput, err := handle.CurrentOutput()
	if err != nil {
		return SimpleCommandResult{}, err
	}
	if len(finalOutput.Output) > lastLen {
		onOutput(finalOutput.Output[lastLen:], true)
	}

	return SimpleCommandResult{
		Output:    finalOutput.Output,
		ExitCode:  exit.ExitCode,
		Signal:    exit.Signal,
		Truncated: finalOutput.Truncated,
	}, nil
}

func joinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.Join(args, " ")
}
