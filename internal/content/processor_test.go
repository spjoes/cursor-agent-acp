package content

import (
	"strings"
	"testing"
	"time"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

func newTestProcessor() *Processor {
	return NewProcessor(logging.New("error"))
}

func TestProcessContentTextSanitization(t *testing.T) {
	p := newTestProcessor()
	result, err := p.ProcessContent([]acp.ContentBlock{{Type: "text", Text: "Hello\r\nworld\x00\rbytes"}})
	if err != nil {
		t.Fatalf("ProcessContent returned error: %v", err)
	}

	if result.Value != "Hello\nworld\nbytes" {
		t.Fatalf("unexpected sanitized text: %q", result.Value)
	}

	blocks, ok := result.Metadata["blocks"].([]map[string]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("unexpected metadata blocks: %#v", result.Metadata["blocks"])
	}
	if sanitized, _ := blocks[0]["sanitized"].(bool); !sanitized {
		t.Fatalf("expected sanitized metadata flag, got %#v", blocks[0])
	}
}

func TestProcessContentImageAndAudioFormatting(t *testing.T) {
	p := newTestProcessor()
	img := "aGVsbG8="
	audio := "Zm9vYmFy"
	result, err := p.ProcessContent([]acp.ContentBlock{
		{Type: "image", Data: img, MimeType: "image/png", URI: "test.png"},
		{Type: "audio", Data: audio, MimeType: "audio/wav"},
	})
	if err != nil {
		t.Fatalf("ProcessContent returned error: %v", err)
	}

	if !strings.Contains(result.Value, "# Image: test.png") {
		t.Fatalf("expected image header, got %q", result.Value)
	}
	if !strings.Contains(result.Value, "[Image data: image/png,") {
		t.Fatalf("expected image data summary, got %q", result.Value)
	}
	if !strings.Contains(result.Value, "[Audio: audio/wav,") {
		t.Fatalf("expected audio summary, got %q", result.Value)
	}
}

func TestProcessContentRejectsInvalidBase64(t *testing.T) {
	p := newTestProcessor()
	_, err := p.ProcessContent([]acp.ContentBlock{{Type: "image", Data: "not-valid-base64!!!", MimeType: "image/png"}})
	if err == nil || !strings.Contains(err.Error(), "Invalid base64 image data") {
		t.Fatalf("expected invalid base64 error, got %v", err)
	}
}

func TestParseResponseCodeAndFileSections(t *testing.T) {
	p := newTestProcessor()
	response := "Here is code:\n```javascript\nconsole.log(1);\n```\n\n# File: test.js\n```javascript\nconst x = 1;\n```"

	blocks := p.ParseResponse(response)
	if len(blocks) < 2 {
		t.Fatalf("expected multiple blocks, got %#v", blocks)
	}
	if blocks[0].Type != "text" {
		t.Fatalf("expected text block, got %#v", blocks[0])
	}

	fullText := ""
	for _, b := range blocks {
		fullText += b.Text
	}
	if !strings.Contains(fullText, "```javascript") {
		t.Fatalf("expected code fence in parsed blocks: %#v", blocks)
	}
	if !strings.Contains(fullText, "# File: test.js") {
		t.Fatalf("expected file section in parsed blocks: %#v", blocks)
	}
}

func TestProcessStreamChunkTextAndCodeLifecycle(t *testing.T) {
	p := newTestProcessor()

	block1, err := p.ProcessStreamChunk("Intro line\n")
	if err != nil {
		t.Fatalf("ProcessStreamChunk returned error: %v", err)
	}
	if block1 == nil || block1.Type != "text" || block1.Text != "Intro line\n" {
		t.Fatalf("unexpected first chunk block: %#v", block1)
	}

	block2, err := p.ProcessStreamChunk("```go\nfmt.")
	if err != nil {
		t.Fatalf("ProcessStreamChunk returned error: %v", err)
	}
	if block2 != nil {
		t.Fatalf("expected nil for partial code chunk, got %#v", block2)
	}

	block3, err := p.ProcessStreamChunk("Println(\"hi\")\n```\n")
	if err != nil {
		t.Fatalf("ProcessStreamChunk returned error: %v", err)
	}
	if block3 == nil || block3.Type != "text" || !strings.Contains(block3.Text, "```go") {
		t.Fatalf("expected formatted code block, got %#v", block3)
	}
}

func TestProcessStreamChunkImageReferenceBehavior(t *testing.T) {
	p := newTestProcessor()
	block, err := p.ProcessStreamChunk("Check this [Image data: image/png, 1.5KB base64] screenshot\n")
	if err != nil {
		t.Fatalf("ProcessStreamChunk returned error: %v", err)
	}
	if block == nil || block.Type != "text" || strings.TrimSpace(block.Text) != "Check this" {
		t.Fatalf("unexpected image reference chunk block: %#v", block)
	}
}

func TestFinalizeStreamingFlushesRemainder(t *testing.T) {
	p := newTestProcessor()
	_, _ = p.ProcessStreamChunk("trailing text without newline")
	final := p.FinalizeStreaming()
	if final == nil || final.Type != "text" || strings.TrimSpace(final.Text) != "trailing text without newline" {
		t.Fatalf("expected trailing final block, got %#v", final)
	}
}

func TestGetContentStats(t *testing.T) {
	p := newTestProcessor()
	stats := p.GetContentStats([]acp.ContentBlock{
		{Type: "text", Text: "Hello"},
		{Type: "text", Text: "World"},
		{Type: "resource", Resource: &acp.EmbeddedResource{URI: "file:///code.js", Text: "const x = 1;"}},
	})

	if total, _ := stats["total"].(int); total != 3 {
		t.Fatalf("unexpected total: %#v", stats)
	}
	if totalSize, _ := stats["totalSize"].(int); totalSize <= 0 {
		t.Fatalf("expected positive totalSize: %#v", stats)
	}
	byType, ok := stats["byType"].(map[string]int)
	if !ok {
		t.Fatalf("unexpected byType: %#v", stats["byType"])
	}
	if byType["text"] != 2 {
		t.Fatalf("unexpected byType counts: %#v", byType)
	}
}

func TestValidateContentBlocks(t *testing.T) {
	p := newTestProcessor()
	valid := []acp.ContentBlock{
		{Type: "text", Text: "Hello", Annotations: map[string]any{"lastModified": time.Now().UTC().Format(time.RFC3339)}},
		{Type: "image", Data: "aGVsbG8=", MimeType: "image/png"},
		{Type: "audio", Data: "Zm9v", MimeType: "audio/wav"},
		{Type: "resource", Resource: &acp.EmbeddedResource{URI: "file:///x", Text: "body"}},
		{Type: "resource_link", URI: "https://example.com", Name: "Example", Size: 12},
	}

	validResult := p.ValidateContentBlocks(valid)
	if !validResult.Valid || len(validResult.Errors) != 0 {
		t.Fatalf("expected valid content blocks, got %#v", validResult)
	}

	invalid := []any{
		nil,
		map[string]any{"type": "text"},
		map[string]any{"type": "image", "data": "invalid", "mimeType": 123},
		map[string]any{"type": "resource_link", "uri": "u", "name": "n", "size": "100"},
		map[string]any{"type": "text", "text": "x", "annotations": map[string]any{"priority": -1}},
	}
	invalidResult := p.ValidateContentBlocks(invalid)
	if invalidResult.Valid || len(invalidResult.Errors) == 0 {
		t.Fatalf("expected validation errors, got %#v", invalidResult)
	}
	if !containsError(invalidResult.Errors, "must be an object") {
		t.Fatalf("expected object validation error: %#v", invalidResult.Errors)
	}
	if !containsError(invalidResult.Errors, "data must be valid base64") {
		t.Fatalf("expected base64 validation error: %#v", invalidResult.Errors)
	}
	if !containsError(invalidResult.Errors, "size must be a bigint or null") {
		t.Fatalf("expected size validation error: %#v", invalidResult.Errors)
	}
	if !containsError(invalidResult.Errors, "annotations.priority must be non-negative") {
		t.Fatalf("expected annotation priority validation error: %#v", invalidResult.Errors)
	}
}

func containsError(errors []string, substring string) bool {
	for _, e := range errors {
		if strings.Contains(e, substring) {
			return true
		}
	}
	return false
}
