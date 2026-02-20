package client

import (
	"fmt"

	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type ReadFileOptions struct {
	SessionID string
	Path      string
	Line      int
	Limit     int
}

type WriteFileOptions struct {
	SessionID string
	Path      string
	Content   string
}

type FileSystemClient interface {
	ReadTextFile(options ReadFileOptions) (string, error)
	WriteTextFile(options WriteFileOptions) error
}

type ACPFileSystemClient struct {
	conn   Connection
	logger *logging.Logger
}

func NewACPFileSystemClient(conn Connection, logger *logging.Logger) *ACPFileSystemClient {
	return &ACPFileSystemClient{conn: conn, logger: logger}
}

func (c *ACPFileSystemClient) ReadTextFile(options ReadFileOptions) (string, error) {
	req := ReadTextFileRequest{SessionID: options.SessionID, Path: options.Path}
	if options.Line > 0 {
		req.Line = options.Line
	}
	if options.Limit > 0 {
		req.Limit = options.Limit
	}
	resp, err := c.conn.ReadTextFile(req)
	if err != nil {
		return "", fmt.Errorf("failed to read file %q: %w", options.Path, err)
	}
	return resp.Content, nil
}

func (c *ACPFileSystemClient) WriteTextFile(options WriteFileOptions) error {
	_, err := c.conn.WriteTextFile(WriteTextFileRequest{
		SessionID: options.SessionID,
		Path:      options.Path,
		Content:   options.Content,
	})
	if err != nil {
		return fmt.Errorf("failed to write file %q: %w", options.Path, err)
	}
	return nil
}
