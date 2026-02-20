package client

type Connection interface {
	ReadTextFile(params ReadTextFileRequest) (ReadTextFileResponse, error)
	WriteTextFile(params WriteTextFileRequest) (WriteTextFileResponse, error)
	CreateTerminal(params CreateTerminalRequest) (CreateTerminalResponse, error)
	GetTerminalOutput(params TerminalOutputRequest) (TerminalOutputResponse, error)
	WaitForTerminalExit(params WaitForTerminalExitRequest) (WaitForTerminalExitResponse, error)
	KillTerminal(params KillTerminalRequest) error
	ReleaseTerminal(params ReleaseTerminalRequest) error
}

type ReadTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      int    `json:"line,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type ReadTextFileResponse struct {
	Content string `json:"content"`
}

type WriteTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

type WriteTextFileResponse struct{}

type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type CreateTerminalRequest struct {
	SessionID       string        `json:"sessionId"`
	Command         string        `json:"command"`
	Args            []string      `json:"args,omitempty"`
	Cwd             string        `json:"cwd,omitempty"`
	Env             []EnvVariable `json:"env,omitempty"`
	OutputByteLimit int           `json:"outputByteLimit,omitempty"`
}

type CreateTerminalResponse struct {
	TerminalID string `json:"terminalId"`
}

type TerminalExitStatus struct {
	ExitCode *int    `json:"exitCode,omitempty"`
	Signal   *string `json:"signal,omitempty"`
}

type TerminalOutputRequest struct {
	TerminalID string `json:"terminalId"`
}

type TerminalOutputResponse struct {
	Output     string              `json:"output"`
	Truncated  bool                `json:"truncated,omitempty"`
	ExitStatus *TerminalExitStatus `json:"exitStatus,omitempty"`
}

type WaitForTerminalExitRequest struct {
	TerminalID string `json:"terminalId"`
}

type WaitForTerminalExitResponse struct {
	ExitCode *int    `json:"exitCode,omitempty"`
	Signal   *string `json:"signal,omitempty"`
}

type KillTerminalRequest struct {
	TerminalID string `json:"terminalId"`
}

type ReleaseTerminalRequest struct {
	TerminalID string `json:"terminalId"`
}
