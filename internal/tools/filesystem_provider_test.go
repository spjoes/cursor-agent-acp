package tools

import (
	"errors"
	"io"
	"testing"

	"github.com/spjoes/cursor-agent-acp/internal/client"
	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type mockFSClient struct {
	readContent string
	readErr     error
	writeErr    error
	lastWrite   client.WriteFileOptions
}

func (m *mockFSClient) ReadTextFile(options client.ReadFileOptions) (string, error) {
	if m.readErr != nil {
		return "", m.readErr
	}
	return m.readContent, nil
}

func (m *mockFSClient) WriteTextFile(options client.WriteFileOptions) error {
	m.lastWrite = options
	return m.writeErr
}

func newTestFilesystemProvider(fsClient client.FileSystemClient) *FilesystemProvider {
	cfg := config.Default()
	logger := logging.NewWithOutput("error", io.Discard)
	caps := map[string]any{
		"fs": map[string]any{
			"readTextFile":  true,
			"writeTextFile": true,
		},
	}
	return NewFilesystemProvider(cfg, logger, caps, fsClient)
}

func TestFilesystemProviderReadFileResultMetaParity(t *testing.T) {
	mock := &mockFSClient{readContent: ""}
	provider := newTestFilesystemProvider(mock)

	result, err := provider.readFileOnce(map[string]any{
		"_sessionId": "session-1",
		"path":       "/tmp/example.txt",
	})
	if err != nil {
		t.Fatalf("readFileOnce returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success result, got: %#v", result)
	}
	if result.Metadata != nil {
		t.Fatalf("expected no top-level metadata for filesystem provider parity, got: %#v", result.Metadata)
	}

	payload, ok := result.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result payload, got: %#v", result.Result)
	}
	meta, ok := payload["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected _meta map in result payload, got: %#v", payload["_meta"])
	}
	if meta["acpMethod"] != "fs/read_text_file" {
		t.Fatalf("expected acpMethod fs/read_text_file, got: %#v", meta["acpMethod"])
	}
	if meta["includesUnsavedChanges"] != true {
		t.Fatalf("expected includesUnsavedChanges=true, got: %#v", meta["includesUnsavedChanges"])
	}
	if meta["sessionId"] != "session-1" {
		t.Fatalf("expected sessionId=session-1, got: %#v", meta["sessionId"])
	}
	if meta["lineCount"] != 1 {
		t.Fatalf("expected empty content to report lineCount=1 (JS split behavior), got: %#v", meta["lineCount"])
	}
}

func TestFilesystemProviderWriteFilePathMustBeString(t *testing.T) {
	mock := &mockFSClient{}
	provider := newTestFilesystemProvider(mock)

	_, err := provider.writeFileOnce(map[string]any{
		"_sessionId": "session-1",
		"path":       123,
		"content":    "hello",
	})
	if err == nil {
		t.Fatal("expected writeFileOnce to reject non-string path")
	}
	expected := "Valid file path is required. Path must be a non-empty string."
	if err.Error() != expected {
		t.Fatalf("expected error %q, got %q", expected, err.Error())
	}
}

func TestFilesystemProviderWriteFileContentStringCoercion(t *testing.T) {
	mock := &mockFSClient{}
	provider := newTestFilesystemProvider(mock)

	result, err := provider.writeFileOnce(map[string]any{
		"_sessionId": "session-1",
		"path":       "/tmp/example.txt",
		"content":    42,
	})
	if err != nil {
		t.Fatalf("writeFileOnce returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success result, got: %#v", result)
	}
	if mock.lastWrite.Content != "42" {
		t.Fatalf("expected numeric content to be coerced to string \"42\", got %q", mock.lastWrite.Content)
	}
}

func TestFilesystemProviderWriteFileMissingContentErrorMessageParity(t *testing.T) {
	mock := &mockFSClient{writeErr: errors.New("unused")}
	provider := newTestFilesystemProvider(mock)

	_, err := provider.writeFileOnce(map[string]any{
		"_sessionId": "session-1",
		"path":       "/tmp/example.txt",
	})
	if err == nil {
		t.Fatal("expected writeFileOnce to reject missing content")
	}
	expected := "Content is required. To create an empty file, pass an empty string."
	if err.Error() != expected {
		t.Fatalf("expected error %q, got %q", expected, err.Error())
	}
}
