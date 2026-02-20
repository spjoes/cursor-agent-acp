package errorfmt

import (
	"errors"
	"testing"

	"github.com/spjoes/cursor-agent-acp/internal/jsonrpc"
)

func TestCodeForError(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{errors.New("sessionId is required"), jsonrpc.InvalidParams},
		{errors.New("resource not found"), jsonrpc.MethodNotFound},
		{errors.New("boom"), jsonrpc.InternalError},
	}

	for _, tc := range cases {
		if got := CodeForError(tc.err); got != tc.want {
			t.Fatalf("CodeForError(%q) = %d, want %d", tc.err.Error(), got, tc.want)
		}
	}
}

func TestFormat(t *testing.T) {
	formatted := Format(errors.New("invalid params"), "fallback", map[string]any{"k": "v"})
	if formatted.Code != jsonrpc.InvalidParams {
		t.Fatalf("expected invalid params code, got %d", formatted.Code)
	}
	if formatted.Message != "invalid params" {
		t.Fatalf("unexpected message: %q", formatted.Message)
	}
	if formatted.Data["k"] != "v" {
		t.Fatalf("unexpected data: %#v", formatted.Data)
	}
}
