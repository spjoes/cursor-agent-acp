package errorfmt

import (
	"strings"

	"github.com/spjoes/cursor-agent-acp/internal/jsonrpc"
)

type Formatted struct {
	Code    int
	Message string
	Data    map[string]any
}

func Format(err error, fallbackMessage string, data map[string]any) Formatted {
	msg := fallbackMessage
	if err != nil {
		msg = err.Error()
	}
	if msg == "" {
		msg = "internal error"
	}
	return Formatted{
		Code:    CodeForError(err),
		Message: msg,
		Data:    data,
	}
}

func CodeForError(err error) int {
	if err == nil {
		return jsonrpc.InternalError
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "required"), strings.Contains(msg, "invalid"), strings.Contains(msg, "must"), strings.Contains(msg, "params"):
		return jsonrpc.InvalidParams
	case strings.Contains(msg, "unknown method"), strings.Contains(msg, "not found"):
		return jsonrpc.MethodNotFound
	default:
		return jsonrpc.InternalError
	}
}
