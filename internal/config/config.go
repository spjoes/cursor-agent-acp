package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	LogLevel       string       `json:"logLevel"`
	SessionDir     string       `json:"sessionDir"`
	MaxSessions    int          `json:"maxSessions"`
	SessionTimeout int64        `json:"sessionTimeout"` // milliseconds
	Tools          ToolsConfig  `json:"tools"`
	Cursor         CursorConfig `json:"cursor"`
}

type ToolsConfig struct {
	Filesystem FilesystemConfig  `json:"filesystem"`
	Terminal   TerminalConfig    `json:"terminal"`
	Cursor     CursorToolsConfig `json:"cursor,omitempty"`
}

type FilesystemConfig struct {
	Enabled           bool     `json:"enabled"`
	AllowedPaths      []string `json:"allowedPaths,omitempty"`
	MaxFileSize       int64    `json:"maxFileSize,omitempty"`
	AllowedExtensions []string `json:"allowedExtensions,omitempty"`
}

type TerminalConfig struct {
	Enabled                bool     `json:"enabled"`
	MaxProcesses           int      `json:"maxProcesses"`
	DefaultOutputByteLimit int      `json:"defaultOutputByteLimit,omitempty"`
	MaxOutputByteLimit     int      `json:"maxOutputByteLimit,omitempty"`
	ForbiddenCommands      []string `json:"forbiddenCommands,omitempty"`
	AllowedCommands        []string `json:"allowedCommands,omitempty"`
	DefaultCwd             string   `json:"defaultCwd,omitempty"`
}

type CursorToolsConfig struct {
	Enabled                bool `json:"enabled,omitempty"`
	MaxSearchResults       int  `json:"maxSearchResults,omitempty"`
	EnableCodeModification bool `json:"enableCodeModification,omitempty"`
	EnableTestExecution    bool `json:"enableTestExecution,omitempty"`
}

type CursorConfig struct {
	Timeout int64 `json:"timeout"` // milliseconds
	Retries int   `json:"retries"`
}

func Default() Config {
	return Config{
		LogLevel:       "info",
		SessionDir:     "~/.cursor-sessions",
		MaxSessions:    100,
		SessionTimeout: 3_600_000,
		Tools: ToolsConfig{
			Filesystem: FilesystemConfig{
				Enabled:      true,
				AllowedPaths: []string{"."},
				MaxFileSize:  10 * 1024 * 1024,
			},
			Terminal: TerminalConfig{
				Enabled:                true,
				MaxProcesses:           5,
				DefaultOutputByteLimit: 10 * 1024 * 1024,
				MaxOutputByteLimit:     50 * 1024 * 1024,
				ForbiddenCommands:      []string{"rm", "sudo", "su"},
			},
			Cursor: CursorToolsConfig{
				Enabled:                true,
				MaxSearchResults:       50,
				EnableCodeModification: true,
				EnableTestExecution:    true,
			},
		},
		Cursor: CursorConfig{
			Timeout: 30000,
			Retries: 3,
		},
	}
}

func Load(path string, base Config) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Normalize(base)
	}

	resolved, err := expandPath(path)
	if err != nil {
		return Config{}, err
	}
	buf, err := os.ReadFile(resolved)
	if err != nil {
		return Config{}, fmt.Errorf("read config file: %w", err)
	}

	cfg := base
	if err := json.Unmarshal(buf, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config file: %w", err)
	}

	return Normalize(cfg)
}

func Normalize(cfg Config) (Config, error) {
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	resolved, err := expandPath(cfg.SessionDir)
	if err != nil {
		return Config{}, err
	}
	cfg.SessionDir = resolved

	if cfg.Tools.Terminal.DefaultCwd != "" {
		cwd, err := expandPath(cfg.Tools.Terminal.DefaultCwd)
		if err != nil {
			return Config{}, err
		}
		cfg.Tools.Terminal.DefaultCwd = cwd
	}

	if len(cfg.Tools.Filesystem.AllowedPaths) == 0 {
		cfg.Tools.Filesystem.AllowedPaths = []string{"."}
	}
	for i := range cfg.Tools.Filesystem.AllowedPaths {
		p, err := expandPath(cfg.Tools.Filesystem.AllowedPaths[i])
		if err != nil {
			return Config{}, err
		}
		cfg.Tools.Filesystem.AllowedPaths[i] = p
	}

	return cfg, nil
}

func Validate(cfg Config) []error {
	var errs []error

	if cfg.LogLevel != "error" && cfg.LogLevel != "warn" && cfg.LogLevel != "info" && cfg.LogLevel != "debug" {
		errs = append(errs, fmt.Errorf("invalid logLevel: %s", cfg.LogLevel))
	}
	if cfg.MaxSessions < 1 || cfg.MaxSessions > 1000 {
		errs = append(errs, errors.New("maxSessions must be between 1 and 1000"))
	}
	if cfg.SessionTimeout < 60_000 || cfg.SessionTimeout > 86_400_000 {
		errs = append(errs, errors.New("sessionTimeout must be between 60000 and 86400000"))
	}
	if cfg.Cursor.Timeout < 5_000 || cfg.Cursor.Timeout > 300_000 {
		errs = append(errs, errors.New("cursor.timeout must be between 5000 and 300000"))
	}
	if cfg.Cursor.Retries < 0 || cfg.Cursor.Retries > 10 {
		errs = append(errs, errors.New("cursor.retries must be between 0 and 10"))
	}
	if cfg.Tools.Terminal.MaxProcesses < 1 || cfg.Tools.Terminal.MaxProcesses > 20 {
		errs = append(errs, errors.New("tools.terminal.maxProcesses must be between 1 and 20"))
	}
	if cfg.Cursor.Timeout*int64(cfg.Cursor.Retries+1) > 600_000 {
		errs = append(errs, errors.New("cursor.timeout*(retries+1) must not exceed 600000"))
	}

	return errs
}

func EnsureSessionDir(cfg Config) error {
	return os.MkdirAll(cfg.SessionDir, 0o755)
}

func expandPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path cannot be empty")
	}

	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}

	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path for %q: %w", p, err)
	}
	return abs, nil
}
