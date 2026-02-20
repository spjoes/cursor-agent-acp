package content

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
)

type ProcessedContent struct {
	Value    string
	Metadata map[string]any
}

type ValidationResult struct {
	Valid  bool
	Errors []string
}

type StreamingState struct {
	InCodeBlock        bool
	CodeLanguage       string
	AccumulatedContent string
	PendingTextBlocks  []string
}

type Processor struct {
	logger *logging.Logger

	mu     sync.Mutex
	stream *StreamingState
}

var imageDataPattern = regexp.MustCompile(`\[Image data:[^\]]+\]`)

func NewProcessor(logger *logging.Logger) *Processor {
	return &Processor{logger: logger}
}

func (p *Processor) ProcessContent(blocks []acp.ContentBlock) (ProcessedContent, error) {
	if blocks == nil {
		blocks = []acp.ContentBlock{}
	}
	p.logger.Debug("Processing content blocks", map[string]any{"count": len(blocks)})

	parts := make([]string, 0, len(blocks))
	metadataBlocks := make([]map[string]any, 0, len(blocks))
	totalSize := 0

	for i, block := range blocks {
		processed, err := p.processContentBlock(block, i)
		if err != nil {
			return ProcessedContent{}, err
		}
		parts = append(parts, processed.Value)
		totalSize += len(processed.Value)

		meta := map[string]any{
			"index": i,
			"type":  block.Type,
			"size":  len(processed.Value),
		}
		for k, v := range processed.Metadata {
			meta[k] = v
		}
		metadataBlocks = append(metadataBlocks, meta)
	}

	result := ProcessedContent{
		Value: strings.Join(parts, "\n\n"),
		Metadata: map[string]any{
			"blocks":    metadataBlocks,
			"totalSize": totalSize,
		},
	}

	p.logger.Debug("Content processing completed", map[string]any{
		"totalBlocks": len(blocks),
		"totalSize":   totalSize,
	})
	return result, nil
}

func (p *Processor) ParseResponse(response string) []acp.ContentBlock {
	p.logger.Debug("Parsing Cursor CLI response", map[string]any{"length": len(response)})

	sections := splitResponseSections(response)
	blocks := make([]acp.ContentBlock, 0, len(sections))
	for _, section := range sections {
		if block := parseResponseSection(section); block != nil {
			blocks = append(blocks, *block)
		}
	}
	blocks = postProcessBlocks(blocks)

	p.logger.Debug("Response parsing completed", map[string]any{"blocks": len(blocks)})
	return blocks
}

func (p *Processor) StartStreaming() {
	p.mu.Lock()
	p.stream = &StreamingState{
		InCodeBlock:        false,
		CodeLanguage:       "",
		AccumulatedContent: "",
		PendingTextBlocks:  []string{},
	}
	p.mu.Unlock()
	p.logger.Debug("Started streaming session", nil)
}

func (p *Processor) ResetStreaming() {
	p.mu.Lock()
	p.stream = nil
	p.mu.Unlock()
	p.logger.Debug("Reset streaming session", nil)
}

func (p *Processor) FinalizeStreaming() *acp.ContentBlock {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stream == nil {
		return nil
	}

	state := p.stream
	p.stream = nil

	if state.InCodeBlock && strings.TrimSpace(state.AccumulatedContent) != "" {
		language := state.CodeLanguage
		codeBlockText := fmt.Sprintf("```%s\n%s\n```", language, state.AccumulatedContent)
		return &acp.ContentBlock{Type: "text", Text: normalizeStructuralElement(codeBlockText)}
	}

	if strings.TrimSpace(state.AccumulatedContent) != "" {
		text := state.AccumulatedContent
		return &acp.ContentBlock{Type: "text", Text: normalizeStructuralElement(text)}
	}

	return nil
}

func (p *Processor) ProcessStreamChunk(chunk any) (*acp.ContentBlock, error) {
	if chunk == nil {
		return nil, nil
	}

	// Stream-json may emit already-typed content blocks.
	if block, ok := chunkToContentBlock(chunk); ok {
		if block.Type == "text" {
			block.Text = normalizeStructuralElement(block.Text)
		}
		return &block, nil
	}

	chunkData, ok := chunk.(string)
	if !ok {
		return nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stream == nil {
		p.stream = &StreamingState{PendingTextBlocks: []string{}}
	}
	state := p.stream
	state.AccumulatedContent += chunkData
	accumulated := state.AccumulatedContent

	if !state.InCodeBlock {
		if idx := strings.Index(accumulated, "```"); idx >= 0 {
			markerLen, language := parseCodeFenceOpening(accumulated[idx:])
			beforeCode := accumulated[:idx]

			if strings.TrimSpace(beforeCode) != "" {
				textToReturn := strings.TrimSpace(beforeCode)
				state.AccumulatedContent = accumulated[idx+markerLen:]
				state.InCodeBlock = true
				state.CodeLanguage = language
				return &acp.ContentBlock{Type: "text", Text: textToReturn}, nil
			}

			state.InCodeBlock = true
			state.CodeLanguage = language
			state.AccumulatedContent = accumulated[idx+markerLen:]
			return nil, nil
		}

		if strings.Contains(accumulated, "```") {
			// Might be a partial code fence marker.
			return nil, nil
		}

		if strings.Contains(accumulated, "[Image data:") {
			match := imageDataPattern.FindString(accumulated)
			if match != "" {
				imageIndex := strings.Index(accumulated, match)
				beforeImage := strings.TrimSpace(accumulated[:imageIndex])
				state.AccumulatedContent = accumulated[imageIndex+len(match):]

				if beforeImage != "" {
					return &acp.ContentBlock{Type: "text", Text: beforeImage}, nil
				}
				return &acp.ContentBlock{Type: "text", Text: normalizeStructuralElement(match)}, nil
			}
		}

		if len(accumulated) > 0 && (strings.Contains(accumulated, "\n") || len(accumulated) > 100) {
			lastNewline := strings.LastIndex(accumulated, "\n")
			if lastNewline > 0 {
				textToReturn := accumulated[:lastNewline+1]
				state.AccumulatedContent = accumulated[lastNewline+1:]
				return &acp.ContentBlock{Type: "text", Text: textToReturn}, nil
			}
		}

		return nil, nil
	}

	if state.InCodeBlock {
		closingIndex := strings.Index(accumulated, "```")
		if closingIndex >= 0 {
			beforeMarker := ""
			if closingIndex > 0 {
				beforeMarker = accumulated[closingIndex-1 : closingIndex]
			}

			afterMarker := ""
			afterMarkerStart := closingIndex + 3
			if afterMarkerStart < len(accumulated) {
				afterMarker = accumulated[afterMarkerStart : afterMarkerStart+1]
			}

			isValidClosing := closingIndex == 0 ||
				(beforeMarker == "\n" && (afterMarker == "" || afterMarker == "\n" || unicode.IsSpace(rune(afterMarker[0]))))

			if isValidClosing {
				codeContent := strings.TrimSpace(accumulated[:closingIndex])
				afterCode := strings.TrimLeft(accumulated[closingIndex+3:], " \t\n\r")

				if len(codeContent) > 0 || closingIndex > 0 {
					language := state.CodeLanguage
					codeBlockText := fmt.Sprintf("```%s\n%s\n```", language, codeContent)
					result := acp.ContentBlock{Type: "text", Text: normalizeStructuralElement(codeBlockText)}

					state.InCodeBlock = false
					state.CodeLanguage = ""
					state.AccumulatedContent = afterCode
					return &result, nil
				}
			}
		}

		return nil, nil
	}

	if len(accumulated) > 100 {
		textToReturn := accumulated
		state.AccumulatedContent = ""
		return &acp.ContentBlock{Type: "text", Text: textToReturn}, nil
	}

	return nil, nil
}

func (p *Processor) GetContentStats(blocks []acp.ContentBlock) map[string]any {
	stats := map[string]any{
		"total":     len(blocks),
		"byType":    map[string]int{},
		"totalSize": 0,
	}
	byType := stats["byType"].(map[string]int)
	totalSize := 0

	for _, block := range blocks {
		byType[block.Type] = byType[block.Type] + 1
		switch block.Type {
		case "text":
			totalSize += len(block.Text)
		case "image", "audio":
			totalSize += len(block.Data)
		case "resource":
			if block.Resource != nil {
				if block.Resource.Text != "" {
					totalSize += len(block.Resource.Text)
				} else {
					totalSize += len(block.Resource.Blob)
				}
			}
		case "resource_link":
			totalSize += len(block.URI)
		}
	}

	stats["totalSize"] = totalSize
	return stats
}

func (p *Processor) ValidateContentBlocks(blocks any) ValidationResult {
	errors := []string{}

	if blocks == nil {
		return ValidationResult{Valid: false, Errors: []string{"Content blocks must be an array"}}
	}

	rv := reflect.ValueOf(blocks)
	if rv.Kind() != reflect.Slice {
		return ValidationResult{Valid: false, Errors: []string{"Content blocks must be an array"}}
	}

	for i := 0; i < rv.Len(); i++ {
		errors = append(errors, p.validateContentBlock(rv.Index(i).Interface(), i)...)
	}

	return ValidationResult{Valid: len(errors) == 0, Errors: errors}
}

func (p *Processor) processContentBlock(block acp.ContentBlock, index int) (ProcessedContent, error) {
	switch block.Type {
	case "text":
		value := sanitizeText(block.Text)
		return ProcessedContent{
			Value: value,
			Metadata: map[string]any{
				"originalLength": len(block.Text),
				"sanitized":      value != block.Text,
				"annotations":    block.Annotations,
			},
		}, nil
	case "image":
		if !isValidBase64(block.Data) {
			return ProcessedContent{}, fmt.Errorf("Invalid base64 image data in block %d", index)
		}

		value := ""
		if block.URI != "" {
			value += "# Image: " + block.URI + "\n"
		} else {
			value += fmt.Sprintf("# Image (%s)\n", block.MimeType)
		}
		value += fmt.Sprintf("[Image data: %s, %s base64]", block.MimeType, formatDataSize(int64(len(block.Data))))

		return ProcessedContent{
			Value: value,
			Metadata: map[string]any{
				"mimeType":      block.MimeType,
				"uri":           maybeString(block.URI),
				"dataSize":      len(block.Data),
				"isValidBase64": true,
				"annotations":   block.Annotations,
			},
		}, nil
	case "audio":
		if !isValidBase64(block.Data) {
			return ProcessedContent{}, fmt.Errorf("Invalid base64 audio data in block %d", index)
		}

		audioFormat := "unknown"
		if parts := strings.SplitN(block.MimeType, "/", 2); len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			audioFormat = parts[1]
		}
		value := fmt.Sprintf("[Audio: %s, %s, format: %s]", block.MimeType, formatDataSize(int64(len(block.Data))), audioFormat)
		return ProcessedContent{
			Value: value,
			Metadata: map[string]any{
				"mimeType":      block.MimeType,
				"dataSize":      len(block.Data),
				"format":        audioFormat,
				"isValidBase64": true,
				"annotations":   block.Annotations,
			},
		}, nil
	case "resource":
		if block.Resource == nil {
			return ProcessedContent{}, fmt.Errorf("invalid resource content block at %d", index)
		}
		res := block.Resource
		isText := res.Text != ""
		value := ""
		value += "# Resource: " + res.URI + "\n"
		if res.MimeType != "" {
			value += "# Type: " + res.MimeType + "\n"
		}
		value += "\n"

		size := 0
		if isText {
			value += res.Text
			size = len(res.Text)
		} else if res.Blob != "" {
			value += fmt.Sprintf("[Binary data: %s]", formatDataSize(int64(len(res.Blob))))
			size = len(res.Blob)
		}

		return ProcessedContent{
			Value: value,
			Metadata: map[string]any{
				"uri":         res.URI,
				"mimeType":    maybeString(res.MimeType),
				"isText":      isText,
				"size":        size,
				"annotations": block.Annotations,
			},
		}, nil
	case "resource_link":
		value := ""
		value += "# Resource Link: " + block.Name + "\n"
		value += "URI: " + block.URI + "\n"
		if block.Title != "" {
			value += "Title: " + block.Title + "\n"
		}
		if block.Description != "" {
			value += "Description: " + block.Description + "\n"
		}
		if block.MimeType != "" {
			value += "Type: " + block.MimeType + "\n"
		}
		if block.Size != nil {
			if exact, ok := tryFormatExactSize(block.Size); ok {
				value += "Size: " + exact + "\n"
			} else if parsed, ok := parseSizeNumber(block.Size); ok {
				value += "Size: " + formatDataSize(parsed) + "\n"
			}
		}

		return ProcessedContent{
			Value: value,
			Metadata: map[string]any{
				"uri":         maybeString(block.URI),
				"name":        block.Name,
				"mimeType":    maybeString(block.MimeType),
				"title":       maybeString(block.Title),
				"description": maybeString(block.Description),
				"size":        block.Size,
				"annotations": block.Annotations,
			},
		}, nil
	default:
		name := strings.TrimSpace(block.Type)
		if name == "" {
			name = "unknown"
		}
		return ProcessedContent{}, fmt.Errorf("Unknown content block type: %s", name)
	}
}

func splitResponseSections(response string) []string {
	sections := []string{}
	lines := strings.Split(response, "\n")
	current := []string{}
	inCodeBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			if inCodeBlock {
				current = append(current, line)
				sections = append(sections, strings.Join(current, "\n"))
				current = []string{}
				inCodeBlock = false
			} else {
				if len(current) > 0 {
					sections = append(sections, strings.Join(current, "\n"))
					current = []string{}
				}
				current = append(current, line)
				inCodeBlock = true
			}
		case strings.HasPrefix(trimmed, "# File:") || strings.HasPrefix(trimmed, "# Image:"):
			if len(current) > 0 {
				sections = append(sections, strings.Join(current, "\n"))
				current = []string{}
			}
			current = append(current, line)
		default:
			current = append(current, line)
		}
	}

	if len(current) > 0 {
		sections = append(sections, strings.Join(current, "\n"))
	}

	filtered := make([]string, 0, len(sections))
	for _, section := range sections {
		if strings.TrimSpace(section) != "" {
			filtered = append(filtered, section)
		}
	}
	return filtered
}

func parseResponseSection(section string) *acp.ContentBlock {
	trimmed := strings.TrimSpace(section)
	if trimmed == "" {
		return nil
	}

	switch {
	case strings.HasPrefix(trimmed, "```"):
		return &acp.ContentBlock{Type: "text", Text: normalizeStructuralElement(trimmed)}
	case strings.HasPrefix(trimmed, "# File:"):
		return &acp.ContentBlock{Type: "text", Text: normalizeStructuralElement(trimmed)}
	case strings.HasPrefix(trimmed, "# Image:"):
		return &acp.ContentBlock{Type: "text", Text: normalizeStructuralElement(trimmed)}
	default:
		return &acp.ContentBlock{Type: "text", Text: trimmed}
	}
}

func postProcessBlocks(blocks []acp.ContentBlock) []acp.ContentBlock {
	processed := make([]acp.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		processed = append(processed, block)
	}
	return processed
}

func isStructuralElement(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "```") ||
		strings.HasPrefix(trimmed, "# File:") ||
		strings.HasPrefix(trimmed, "# Image:") ||
		strings.HasPrefix(trimmed, "[Image data:")
}

func normalizeStructuralElement(text string) string {
	if !isStructuralElement(text) {
		return text
	}
	return "\n" + strings.TrimSpace(text) + "\n"
}

func sanitizeText(in string) string {
	in = strings.ReplaceAll(in, "\x00", "")
	in = strings.ReplaceAll(in, "\r\n", "\n")
	in = strings.ReplaceAll(in, "\r", "\n")
	return in
}

func isValidBase64(value string) bool {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return false
	}
	return base64.StdEncoding.EncodeToString(decoded) == value
}

func formatDataSize(bytes int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	size := float64(bytes)
	unitIndex := 0
	for size >= 1024 && unitIndex < len(units)-1 {
		size /= 1024
		unitIndex++
	}
	return fmt.Sprintf("%.1f%s", size, units[unitIndex])
}

func parseCodeFenceOpening(segment string) (markerLen int, language string) {
	if !strings.HasPrefix(segment, "```") {
		return 0, ""
	}
	rest := segment[3:]
	consumed := 3
	i := 0
	for i < len(rest) && isWordChar(rest[i]) {
		i++
	}
	if i > 0 {
		language = strings.TrimSpace(rest[:i])
		consumed += i
	}
	if i < len(rest) && rest[i] == '\n' {
		consumed++
	}
	return consumed, language
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func chunkToContentBlock(chunk any) (acp.ContentBlock, bool) {
	if block, ok := chunk.(acp.ContentBlock); ok {
		return block, true
	}
	if block, ok := chunk.(*acp.ContentBlock); ok && block != nil {
		return *block, true
	}

	m, ok := chunk.(map[string]any)
	if !ok {
		return acp.ContentBlock{}, false
	}
	typ, _ := m["type"].(string)
	if strings.TrimSpace(typ) == "" {
		return acp.ContentBlock{}, false
	}

	raw, err := json.Marshal(m)
	if err != nil {
		return acp.ContentBlock{}, false
	}
	var block acp.ContentBlock
	if err := json.Unmarshal(raw, &block); err != nil {
		return acp.ContentBlock{}, false
	}
	if block.Type == "" {
		block.Type = typ
	}
	return block, true
}

func maybeString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func parseSizeNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		if uint64(n) > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case float32:
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return 0, false
		}
		return int64(n), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
		if f, err := n.Float64(); err == nil {
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return 0, false
			}
			return int64(f), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func tryFormatExactSize(v any) (string, bool) {
	const maxSafe = int64(9007199254740991)
	const minSafe = int64(-9007199254740991)

	if bigPtr, ok := v.(*big.Int); ok && bigPtr != nil {
		if bigPtr.IsInt64() {
			i := bigPtr.Int64()
			if i <= maxSafe && i >= minSafe {
				return "", false
			}
		}
		return bigPtr.String() + " bytes", true
	}

	if bigVal, ok := v.(big.Int); ok {
		if bigVal.IsInt64() {
			i := bigVal.Int64()
			if i <= maxSafe && i >= minSafe {
				return "", false
			}
		}
		return bigVal.String() + " bytes", true
	}

	return "", false
}

func (p *Processor) validateContentBlock(block any, index int) []string {
	errors := []string{}

	if block == nil {
		errors = append(errors, fmt.Sprintf("Block %d: must be an object", index))
		return errors
	}

	blockMap, ok := toMap(block)
	if !ok {
		errors = append(errors, fmt.Sprintf("Block %d: must be an object", index))
		return errors
	}

	typeRaw, hasType := blockMap["type"]
	typeName, typeIsString := typeRaw.(string)
	if !hasType || !typeIsString || strings.TrimSpace(typeName) == "" {
		errors = append(errors, fmt.Sprintf("Block %d: type is required and must be a string", index))
		return errors
	}

	switch typeName {
	case "text":
		if _, ok := blockMap["text"].(string); !ok {
			errors = append(errors, fmt.Sprintf("Block %d: text content must be a string (use 'text' field)", index))
		}
	case "image":
		if data, ok := blockMap["data"].(string); !ok {
			errors = append(errors, fmt.Sprintf("Block %d: data must be a string (use 'data' field)", index))
		} else if !isValidBase64(data) {
			errors = append(errors, fmt.Sprintf("Block %d: data must be valid base64", index))
		}
		if _, ok := blockMap["mimeType"].(string); !ok {
			errors = append(errors, fmt.Sprintf("Block %d: mimeType is required and must be a string", index))
		}
		if uri, exists := blockMap["uri"]; exists && uri != nil {
			if _, ok := uri.(string); !ok {
				errors = append(errors, fmt.Sprintf("Block %d: uri must be a string or null", index))
			}
		}
	case "audio":
		if data, ok := blockMap["data"].(string); !ok {
			errors = append(errors, fmt.Sprintf("Block %d: data must be a string", index))
		} else if !isValidBase64(data) {
			errors = append(errors, fmt.Sprintf("Block %d: data must be valid base64", index))
		}
		if _, ok := blockMap["mimeType"].(string); !ok {
			errors = append(errors, fmt.Sprintf("Block %d: mimeType is required and must be a string", index))
		}
	case "resource":
		resourceVal, exists := blockMap["resource"]
		if !exists || resourceVal == nil {
			errors = append(errors, fmt.Sprintf("Block %d: resource field is required", index))
			break
		}
		resourceMap, ok := toMap(resourceVal)
		if !ok {
			errors = append(errors, fmt.Sprintf("Block %d: resource field is required", index))
			break
		}
		if _, ok := resourceMap["uri"].(string); !ok {
			errors = append(errors, fmt.Sprintf("Block %d: resource.uri is required and must be a string", index))
		}

		textVal, hasText := resourceMap["text"].(string)
		blobVal, hasBlob := resourceMap["blob"].(string)
		_ = textVal
		if !hasText && !hasBlob {
			errors = append(errors, fmt.Sprintf("Block %d: resource must have either text or blob field", index))
		}
		if mimeType, exists := resourceMap["mimeType"]; exists && mimeType != nil {
			if _, ok := mimeType.(string); !ok {
				errors = append(errors, fmt.Sprintf("Block %d: resource.mimeType must be a string or null", index))
			}
		}
		if hasBlob && !isValidBase64(blobVal) {
			errors = append(errors, fmt.Sprintf("Block %d: resource.blob must be valid base64", index))
		}
	case "resource_link":
		if _, ok := blockMap["uri"].(string); !ok {
			errors = append(errors, fmt.Sprintf("Block %d: uri is required and must be a string", index))
		}
		if _, ok := blockMap["name"].(string); !ok {
			errors = append(errors, fmt.Sprintf("Block %d: name is required and must be a string", index))
		}
		if title, exists := blockMap["title"]; exists && title != nil {
			if _, ok := title.(string); !ok {
				errors = append(errors, fmt.Sprintf("Block %d: title must be a string or null", index))
			}
		}
		if desc, exists := blockMap["description"]; exists && desc != nil {
			if _, ok := desc.(string); !ok {
				errors = append(errors, fmt.Sprintf("Block %d: description must be a string or null", index))
			}
		}
		if mimeType, exists := blockMap["mimeType"]; exists && mimeType != nil {
			if _, ok := mimeType.(string); !ok {
				errors = append(errors, fmt.Sprintf("Block %d: mimeType must be a string or null", index))
			}
		}
		if size, exists := blockMap["size"]; exists && size != nil {
			if !isNumberLike(size) && !isBigIntLike(size) {
				errors = append(errors, fmt.Sprintf("Block %d: size must be a bigint or null", index))
			}
		}
	default:
		errors = append(errors, fmt.Sprintf("Block %d: unknown content type '%s' (valid types: text, image, audio, resource, resource_link)", index, typeName))
	}

	if ann, exists := blockMap["annotations"]; exists {
		errors = append(errors, p.validateAnnotations(ann, index)...)
	}

	return errors
}

func (p *Processor) validateAnnotations(annotations any, index int) []string {
	errors := []string{}
	if annotations == nil {
		return errors
	}
	annMap, ok := toMap(annotations)
	if !ok {
		return errors
	}

	if audience, exists := annMap["audience"]; exists && audience != nil {
		rv := reflect.ValueOf(audience)
		if rv.Kind() != reflect.Slice {
			errors = append(errors, fmt.Sprintf("Block %d: annotations.audience must be an array or null", index))
		} else {
			for i := 0; i < rv.Len(); i++ {
				role, ok := rv.Index(i).Interface().(string)
				if !ok || (role != "user" && role != "assistant") {
					errors = append(errors, fmt.Sprintf("Block %d: annotations.audience must contain only 'user' or 'assistant'", index))
					break
				}
			}
		}
	}

	if lastModified, exists := annMap["lastModified"]; exists && lastModified != nil {
		text, ok := lastModified.(string)
		if !ok {
			errors = append(errors, fmt.Sprintf("Block %d: annotations.lastModified must be a string or null", index))
		} else if _, err := time.Parse(time.RFC3339, text); err != nil {
			errors = append(errors, fmt.Sprintf("Block %d: annotations.lastModified must be a valid ISO 8601 timestamp", index))
		}
	}

	if priority, exists := annMap["priority"]; exists && priority != nil {
		if !isNumberLike(priority) {
			errors = append(errors, fmt.Sprintf("Block %d: annotations.priority must be a number or null", index))
		} else if n, ok := numberToFloat(priority); ok && n < 0 {
			errors = append(errors, fmt.Sprintf("Block %d: annotations.priority must be non-negative", index))
		}
	}

	return errors
}

func isNumberLike(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number:
		return true
	default:
		return false
	}
}

func isBigIntLike(v any) bool {
	if _, ok := v.(*big.Int); ok {
		return true
	}
	if _, ok := v.(big.Int); ok {
		return true
	}
	return false
}

func numberToFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func toMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	if out == nil {
		return nil, false
	}
	return out, true
}
