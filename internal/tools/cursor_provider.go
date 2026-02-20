package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/config"
	"github.com/spjoes/cursor-agent-acp/internal/cursor"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type CursorProvider struct {
	cfg    config.Config
	logger *logging.Logger
	bridge *cursor.Bridge
}

func NewCursorProvider(cfg config.Config, logger *logging.Logger, bridge *cursor.Bridge) *CursorProvider {
	return &CursorProvider{cfg: cfg, logger: logger, bridge: bridge}
}

func (p *CursorProvider) Name() string {
	return "cursor"
}

func (p *CursorProvider) Description() string {
	return "Cursor CLI integration for code analysis and modification"
}

func (p *CursorProvider) GetTools() []Tool {
	if !p.cfg.Tools.Cursor.Enabled {
		return nil
	}

	return []Tool{
		{
			Name:        "search_codebase",
			Description: "Search for code patterns, symbols, or text across the codebase",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":           map[string]any{"type": "string", "description": "Search query (supports regex patterns)"},
					"file_pattern":    map[string]any{"type": "string", "description": "File pattern to limit search scope"},
					"case_sensitive":  map[string]any{"type": "boolean"},
					"include_context": map[string]any{"type": "boolean"},
					"max_results":     map[string]any{"type": "number"},
				},
				"required": []string{"query"},
			},
			Handler: p.searchCodebase,
		},
		{
			Name:        "analyze_code",
			Description: "Analyze code structure, dependencies, and quality metrics",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path":       map[string]any{"type": "string"},
					"analysis_type":   map[string]any{"type": "string"},
					"include_metrics": map[string]any{"type": "boolean"},
				},
				"required": []string{"file_path"},
			},
			Handler: p.analyzeCode,
		},
		{
			Name:        "apply_code_changes",
			Description: "Apply code changes to one or more files atomically",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"changes": map[string]any{"type": "array"},
					"dry_run": map[string]any{"type": "boolean"},
					"backup":  map[string]any{"type": "boolean"},
				},
				"required": []string{"changes"},
			},
			Handler: p.applyCodeChanges,
		},
		{
			Name:        "run_tests",
			Description: "Execute tests using the project's test runner",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"test_pattern":   map[string]any{"type": "string"},
					"test_framework": map[string]any{"type": "string"},
					"watch_mode":     map[string]any{"type": "boolean"},
					"coverage":       map[string]any{"type": "boolean"},
					"timeout":        map[string]any{"type": "number"},
				},
			},
			Handler: p.runTests,
		},
		{
			Name:        "get_project_info",
			Description: "Get information about the current project structure and configuration",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"include_dependencies": map[string]any{"type": "boolean"},
					"include_scripts":      map[string]any{"type": "boolean"},
					"include_structure":    map[string]any{"type": "boolean"},
				},
			},
			Handler: p.getProjectInfo,
		},
		{
			Name:        "explain_code",
			Description: "Get explanations and documentation for code snippets",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path":        map[string]any{"type": "string"},
					"start_line":       map[string]any{"type": "number"},
					"end_line":         map[string]any{"type": "number"},
					"explanation_type": map[string]any{"type": "string"},
				},
				"required": []string{"file_path"},
			},
			Handler: p.explainCode,
		},
	}
}

func (p *CursorProvider) Cleanup() error { return nil }

func (p *CursorProvider) searchCodebase(params map[string]any) (acp.ToolResult, error) {
	query := getString(params, "query")
	filePattern := getString(params, "file_pattern")
	caseSensitive := getBool(params, "case_sensitive", false)
	includeContext := getBool(params, "include_context", true)
	maxResults := getInt(params, "max_results", p.cfg.Tools.Cursor.MaxSearchResults)
	if maxResults <= 0 {
		maxResults = 50
	}

	args := []string{"search"}
	if query != "" {
		args = append(args, "--query", query)
	}
	if filePattern != "" {
		args = append(args, "--files", filePattern)
	}
	if caseSensitive {
		args = append(args, "--case-sensitive")
	}
	args = append(args, "--limit", strconv.Itoa(maxResults))
	if includeContext {
		args = append(args, "--context", "3")
	}

	result, err := p.bridge.ExecuteCommand(nil, prependCursorAgentArg(args), cursor.CommandOptions{})
	if err != nil {
		return acp.ToolResult{Success: false, Error: err.Error()}, nil
	}
	if !result.Success {
		return acp.ToolResult{Success: false, Error: result.Error}, nil
	}

	searchResults := parseSearchResults(result.Stdout, includeContext)
	locations := make([]map[string]any, 0, len(searchResults))
	for i, r := range searchResults {
		if i >= 10 {
			break
		}
		locations = append(locations, map[string]any{"path": filepath.Clean(r.File), "line": r.Line})
	}

	return acp.ToolResult{Success: true, Result: map[string]any{"query": query, "results": searchResults, "total": len(searchResults), "truncated": len(searchResults) >= maxResults}, Metadata: map[string]any{"searchTime": 0, "filePattern": filePattern, "caseSensitive": caseSensitive, "locations": locations}}, nil
}

func (p *CursorProvider) analyzeCode(params map[string]any) (acp.ToolResult, error) {
	filePath := getString(params, "file_path")
	if filePath == "" {
		return acp.ToolResult{Success: false, Error: "Invalid file path"}, nil
	}
	analysisType := getString(params, "analysis_type")
	if analysisType == "" {
		analysisType = "all"
	}
	includeMetrics := getBool(params, "include_metrics", true)

	resolved := filepath.Clean(filePath)
	args := []string{"analyze", resolved}
	if analysisType != "all" {
		args = append(args, "--type", analysisType)
	}
	if includeMetrics {
		args = append(args, "--metrics")
	}

	result, err := p.bridge.ExecuteCommand(nil, prependCursorAgentArg(args), cursor.CommandOptions{})
	if err != nil {
		return acp.ToolResult{Success: false, Error: err.Error()}, nil
	}
	if !result.Success {
		return acp.ToolResult{Success: false, Error: result.Error}, nil
	}
	analysis := parseJSONObjectOrRaw(result.Stdout)

	return acp.ToolResult{Success: true, Result: mergeMaps(map[string]any{"file": filePath, "analysisType": analysisType}, analysis), Metadata: map[string]any{"analysisTime": 0, "includeMetrics": includeMetrics, "locations": []map[string]any{{"path": resolved}}}}, nil
}

func (p *CursorProvider) applyCodeChanges(params map[string]any) (acp.ToolResult, error) {
	if !p.cfg.Tools.Cursor.EnableCodeModification {
		return acp.ToolResult{Success: false, Error: "Code modification is disabled"}, nil
	}

	rawChanges, ok := params["changes"].([]any)
	if !ok || len(rawChanges) == 0 {
		return acp.ToolResult{Success: false, Error: "No changes provided"}, nil
	}

	type CodeChange struct {
		File       string `json:"file"`
		StartLine  int    `json:"startLine"`
		EndLine    int    `json:"endLine"`
		NewContent string `json:"newContent"`
	}
	changes := make([]CodeChange, 0, len(rawChanges))
	diffs := make([]any, 0, len(rawChanges))
	locations := make([]map[string]any, 0, len(rawChanges))
	for i, rc := range rawChanges {
		changeMap, ok := rc.(map[string]any)
		if !ok {
			return acp.ToolResult{Success: false, Error: fmt.Sprintf("Invalid changes: Change %d: Missing change object", i+1)}, nil
		}
		file := getString(changeMap, "file")
		startLine := getInt(changeMap, "startLine", 0)
		endLine := getInt(changeMap, "endLine", 0)
		newContent := getString(changeMap, "newContent")
		if file == "" || startLine < 1 || endLine < startLine {
			return acp.ToolResult{Success: false, Error: fmt.Sprintf("Invalid changes: Change %d has invalid fields", i+1)}, nil
		}

		changes = append(changes, CodeChange{File: file, StartLine: startLine, EndLine: endLine, NewContent: newContent})
		locations = append(locations, map[string]any{"path": filepath.Clean(file), "line": startLine})

		oldText := ""
		if b, err := os.ReadFile(file); err == nil {
			oldText = string(b)
		}
		diffText := formatUnifiedDiff(file, oldText, newContent)
		diffs = append(diffs, acp.ContentBlock{Type: "resource", Resource: &acp.EmbeddedResource{URI: "diff://" + filepath.Clean(file), Text: diffText, MimeType: "text/x-diff"}, Annotations: map[string]any{"_meta": map[string]any{"diffType": "unified", "originalPath": filepath.Clean(file), "isNewFile": oldText == ""}}})
	}

	dryRun := getBool(params, "dry_run", false)
	backup := getBool(params, "backup", true)

	args := []string{"apply-changes"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if backup {
		args = append(args, "--backup")
	}

	payload, _ := json.MarshalIndent(changes, "", "  ")
	tmpFile := filepath.Join(".", ".cursor-changes.json")
	_ = os.WriteFile(tmpFile, payload, 0o644)
	defer func() { _ = os.Remove(tmpFile) }()
	args = append(args, "--changes-file", tmpFile)

	result, err := p.bridge.ExecuteCommand(nil, prependCursorAgentArg(args), cursor.CommandOptions{})
	if err != nil {
		return acp.ToolResult{Success: false, Error: err.Error()}, nil
	}
	if !result.Success {
		return acp.ToolResult{Success: false, Error: result.Error}, nil
	}

	apply := parseJSONObjectOrRaw(result.Stdout)
	return acp.ToolResult{Success: true, Result: mergeMaps(map[string]any{"applied": !dryRun, "changesCount": len(changes)}, apply), Metadata: map[string]any{"applyTime": 0, "dryRun": dryRun, "backup": backup, "diffs": diffs, "locations": locations}}, nil
}

func (p *CursorProvider) runTests(params map[string]any) (acp.ToolResult, error) {
	if !p.cfg.Tools.Cursor.EnableTestExecution {
		return acp.ToolResult{Success: false, Error: "Test execution is disabled"}, nil
	}

	testPattern := getString(params, "test_pattern")
	testFramework := getString(params, "test_framework")
	if testFramework == "" {
		testFramework = "auto"
	}
	watch := getBool(params, "watch_mode", false)
	coverage := getBool(params, "coverage", false)
	timeout := getInt(params, "timeout", 300)

	args := []string{"test"}
	if testPattern != "" {
		args = append(args, "--pattern", testPattern)
	}
	if testFramework != "auto" {
		args = append(args, "--framework", testFramework)
	}
	if watch {
		args = append(args, "--watch")
	}
	if coverage {
		args = append(args, "--coverage")
	}
	if timeout > 0 {
		args = append(args, "--timeout", strconv.Itoa(timeout))
	}

	result, err := p.bridge.ExecuteCommand(nil, prependCursorAgentArg(args), cursor.CommandOptions{Timeout: time.Duration(timeout) * time.Second})
	if err != nil {
		return acp.ToolResult{Success: false, Error: err.Error()}, nil
	}
	parsed := parseTestResults(result.Stdout, result.Stderr)
	return acp.ToolResult{Success: result.Success, Result: mergeMaps(map[string]any{"framework": mapValue(parsed, "framework", testFramework)}, parsed), Error: ternary(!result.Success, result.Error, ""), Metadata: map[string]any{"executionTime": 0, "watchMode": watch, "coverage": coverage}}, nil
}

func (p *CursorProvider) getProjectInfo(params map[string]any) (acp.ToolResult, error) {
	includeDependencies := getBool(params, "include_dependencies", true)
	includeScripts := getBool(params, "include_scripts", true)
	includeStructure := getBool(params, "include_structure", false)

	args := []string{"info"}
	if includeDependencies {
		args = append(args, "--dependencies")
	}
	if includeScripts {
		args = append(args, "--scripts")
	}
	if includeStructure {
		args = append(args, "--structure")
	}

	result, err := p.bridge.ExecuteCommand(nil, prependCursorAgentArg(args), cursor.CommandOptions{})
	if err != nil {
		return acp.ToolResult{Success: false, Error: err.Error()}, nil
	}
	if !result.Success {
		return acp.ToolResult{Success: false, Error: result.Error}, nil
	}
	info := parseJSONObjectOrRaw(result.Stdout)
	return acp.ToolResult{Success: true, Result: info, Metadata: map[string]any{"infoTime": 0, "includeDependencies": includeDependencies, "includeScripts": includeScripts, "includeStructure": includeStructure}}, nil
}

func (p *CursorProvider) explainCode(params map[string]any) (acp.ToolResult, error) {
	filePath := getString(params, "file_path")
	if filePath == "" {
		return acp.ToolResult{Success: false, Error: "file_path is required"}, nil
	}
	startLine, _ := intParam(params, "start_line")
	endLine, _ := intParam(params, "end_line")
	explanationType := getString(params, "explanation_type")
	if explanationType == "" {
		explanationType = "summary"
	}

	args := []string{"explain", filePath}
	if startLine > 0 {
		args = append(args, "--start-line", strconv.Itoa(startLine))
	}
	if endLine > 0 {
		args = append(args, "--end-line", strconv.Itoa(endLine))
	}
	args = append(args, "--type", explanationType)

	result, err := p.bridge.ExecuteCommand(nil, prependCursorAgentArg(args), cursor.CommandOptions{})
	if err != nil {
		return acp.ToolResult{Success: false, Error: err.Error()}, nil
	}
	if !result.Success {
		return acp.ToolResult{Success: false, Error: result.Error}, nil
	}
	explanation := parseJSONObjectOrRaw(result.Stdout)
	return acp.ToolResult{Success: true, Result: mergeMaps(map[string]any{"file": filePath, "startLine": startLine, "endLine": endLine, "explanationType": explanationType}, explanation), Metadata: map[string]any{"explanationTime": 0, "locations": []map[string]any{{"path": filepath.Clean(filePath), "line": startLine}}}}, nil
}

// parsing helpers

type SearchResult struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Column  int      `json:"column,omitempty"`
	Content string   `json:"content"`
	Context []string `json:"context,omitempty"`
}

func parseSearchResults(output string, includeContext bool) []SearchResult {
	jsonObj := parseJSONObject(output)
	if jsonObj != nil {
		if raw, ok := jsonObj["results"]; ok {
			if b, err := json.Marshal(raw); err == nil {
				var parsed []SearchResult
				if err := json.Unmarshal(b, &parsed); err == nil {
					return parsed
				}
			}
		}
	}

	results := make([]SearchResult, 0)
	lines := strings.Split(output, "\n")
	var current *SearchResult
	re := regexp.MustCompile(`^(.+):(\d+):(\d+)?:\s*(.*)$`)
	for _, line := range lines {
		m := re.FindStringSubmatch(line)
		if len(m) > 0 {
			if current != nil {
				results = append(results, *current)
			}
			lineNum, _ := strconv.Atoi(m[2])
			column := 0
			if m[3] != "" {
				column, _ = strconv.Atoi(m[3])
			}
			current = &SearchResult{File: m[1], Line: lineNum, Column: column, Content: strings.TrimSpace(m[4])}
			if includeContext {
				current.Context = []string{}
			}
		} else if current != nil && includeContext && strings.TrimSpace(line) != "" {
			current.Context = append(current.Context, line)
		}
	}
	if current != nil {
		results = append(results, *current)
	}
	return results
}

func parseJSONObjectOrRaw(output string) map[string]any {
	if obj := parseJSONObject(output); obj != nil {
		return obj
	}
	return map[string]any{"raw": output}
}

func parseJSONObject(output string) map[string]any {
	start := strings.Index(output, "{")
	end := strings.LastIndex(output, "}")
	if start < 0 || end < 0 || end < start {
		return nil
	}
	candidate := output[start : end+1]
	var out map[string]any
	if err := json.Unmarshal([]byte(candidate), &out); err != nil {
		return nil
	}
	return out
}

func parseTestResults(stdout, stderr string) map[string]any {
	combined := stdout + "\n" + stderr
	if obj := parseJSONObject(combined); obj != nil {
		return obj
	}

	tests := make([]map[string]any, 0)
	re := regexp.MustCompile(`(PASS|FAIL|SKIP)\s+(.+?)(?:\s+\((\d+(?:\.\d+)?)s\))?$`)
	for _, line := range strings.Split(combined, "\n") {
		m := re.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) > 0 {
			test := map[string]any{"file": m[2], "suite": "", "test": m[2], "status": strings.ToLower(m[1])}
			if m[3] != "" {
				if d, err := strconv.ParseFloat(m[3], 64); err == nil {
					test["duration"] = d
				}
			}
			tests = append(tests, test)
		}
	}

	return map[string]any{
		"framework": "unknown",
		"tests":     tests,
		"summary": map[string]any{
			"total":   len(tests),
			"passed":  countStatus(tests, "pass"),
			"failed":  countStatus(tests, "fail"),
			"skipped": countStatus(tests, "skip"),
		},
		"raw": combined,
	}
}

func countStatus(tests []map[string]any, status string) int {
	count := 0
	for _, t := range tests {
		if strings.Contains(strings.ToLower(fmt.Sprint(t["status"])), status) {
			count++
		}
	}
	return count
}

func mapValue(m map[string]any, key string, def any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

func mergeMaps(parts ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, m := range parts {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func ternary(cond bool, whenTrue string, whenFalse string) string {
	if cond {
		return whenTrue
	}
	return whenFalse
}

func formatUnifiedDiff(filePath, oldContent, newContent string) string {
	lines := make([]string, 0)
	lines = append(lines, "--- "+filePath)
	lines = append(lines, "+++ "+filePath)
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	lines = append(lines, fmt.Sprintf("@@ -1,%d +1,%d @@", len(oldLines), len(newLines)))
	if oldContent != "" {
		for _, line := range oldLines {
			lines = append(lines, "-"+line)
		}
	}
	for _, line := range newLines {
		lines = append(lines, "+"+line)
	}
	return strings.Join(lines, "\n")
}

func prependCursorAgentArg(args []string) []string {
	out := make([]string, 0, len(args)+1)
	out = append(out, "cursor-agent")
	out = append(out, args...)
	return out
}
