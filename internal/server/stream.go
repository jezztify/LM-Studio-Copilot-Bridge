package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// streamFilteredOpenAIResponse proxies an OpenAI-compatible SSE stream while
// dropping any chunks where "choices" is an empty array. LM Studio emits such
// a chunk at the end of every stream to carry usage statistics; VS Code Copilot
// Chat throws "Response contained no choices" if it sees one.
//
// It also rewrites chunks whose delta.content contains <think>...</think> tags,
// moving the enclosed text into delta.thinking — the field Copilot reads to
// render the reasoning pane.
func streamFilteredOpenAIResponse(w http.ResponseWriter, resp *http.Response, logger *slog.Logger, requestID string) error {
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	extractor := &thinkingExtractor{}
	var skipNextBlank bool
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			if !skipNextBlank {
				if _, err := io.WriteString(w, "\n"); err != nil {
					return err
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			skipNextBlank = false
			continue
		}

		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			logger.Debug("upstream sse chunk", "request_id", requestID, "data", payload)
			if payload != "[DONE]" {
				var chunk map[string]any
				if json.Unmarshal([]byte(payload), &chunk) == nil {
					choices, _ := chunk["choices"].([]any)
					if len(choices) == 0 {
						logger.Debug("dropped empty-choices chunk", "request_id", requestID, "data", payload)
						skipNextBlank = true
						continue
					}
					if extractor.processChunk(chunk) {
						if rewritten, err := json.Marshal(chunk); err == nil {
							line = "data: " + string(rewritten)
							logger.Debug("rewrote thinking chunk", "request_id", requestID, "data", string(rewritten))
						}
					}
				}
			}
		}

		logger.Debug("downstream sse chunk", "request_id", requestID, "line", line)
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	return scanner.Err()
}

// thinkingExtractor splits <think>...</think> content out of delta.content into
// delta.thinking across consecutive SSE chunks. State is preserved between
// chunks so that tag boundaries spanning two chunks are handled correctly.
type thinkingExtractor struct {
	inThinking bool
	pending    string // chars buffered at chunk end that might complete a tag opener/closer
}

// processChunk rewrites chunk in-place. Returns true if any choice was modified.
func (e *thinkingExtractor) processChunk(chunk map[string]any) bool {
	choices, ok := chunk["choices"].([]any)
	if !ok {
		return false
	}
	modified := false
	flushedOnFinish := false
	for _, raw := range choices {
		choice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			delta = map[string]any{}
			choice["delta"] = delta
		}

		if content, ok := delta["content"].(string); ok && content != "" {
			normal, thinking := e.extract(content)
			if narration := extractNarrationPreamble(normal); narration != "" {
				thinking = narration + thinking
				normal = ""
			}
			if thinking != "" || normal != content {
				delta["content"] = normal
				if thinking != "" {
					delta["thinking"] = thinking
				}
				modified = true
			}
		}

		// If stream ends while still in a thinking block, flush buffered tail to
		// delta.thinking on the terminal choice chunk.
		if !flushedOnFinish {
			if finishReason, ok := choice["finish_reason"]; ok && finishReason != nil {
				if tail := e.flushForFinish(); tail != "" {
					if existing, ok := delta["thinking"].(string); ok {
						delta["thinking"] = existing + tail
					} else {
						delta["thinking"] = tail
					}
					modified = true
				}
				flushedOnFinish = true
			}
		}

		if len(delta) == 0 {
			delete(choice, "delta")
		}
	}
	return modified
}

func (e *thinkingExtractor) flushForFinish() string {
	if !e.inThinking {
		return ""
	}
	tail := e.pending
	e.pending = ""
	e.inThinking = false
	return tail
}

const (
	openThinkTag  = "<think>"
	closeThinkTag = "</think>"
)

// extract processes text, returning the non-thinking content and the thinking
// content separately. Any partial tag at the end of text is held in e.pending
// and prepended on the next call.
func (e *thinkingExtractor) extract(text string) (normal, thinking string) {
	all := e.pending + text
	e.pending = ""
	for len(all) > 0 {
		if e.inThinking {
			idx := strings.Index(all, closeThinkTag)
			if idx == -1 {
				cut := partialTagSuffixLen(all, closeThinkTag)
				thinking += all[:len(all)-cut]
				e.pending = all[len(all)-cut:]
				break
			}
			thinking += all[:idx]
			e.inThinking = false
			all = all[idx+len(closeThinkTag):]
			continue
		}

		openIdx := strings.Index(all, openThinkTag)
		closeIdx := strings.Index(all, closeThinkTag)
		switch {
		case openIdx == -1 && closeIdx == -1:
			cut := max(partialTagSuffixLen(all, openThinkTag), partialTagSuffixLen(all, closeThinkTag))
			normal += all[:len(all)-cut]
			e.pending = all[len(all)-cut:]
			all = ""
		case closeIdx != -1 && (openIdx == -1 || closeIdx < openIdx):
			// Stray close in normal mode: treat preceding text as thinking and
			// drop the closing marker from user-visible content.
			thinking += all[:closeIdx]
			all = all[closeIdx+len(closeThinkTag):]
		default:
			normal += all[:openIdx]
			e.inThinking = true
			all = all[openIdx+len(openThinkTag):]
		}
	}
	return
}

// partialTagSuffixLen returns how many trailing bytes of s could be the
// beginning of tag — so they must be buffered rather than emitted.
func partialTagSuffixLen(s, tag string) int {
	for n := min(len(tag)-1, len(s)); n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}

func streamGenerateResponse(w http.ResponseWriter, upstream io.Reader) error {
	return relaySSE(w, upstream, func(data []byte) (map[string]any, bool, error) {
		var chunk openAICompletionStreamChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return nil, false, fmt.Errorf("decode generate stream chunk: %w", err)
		}

		choice := firstCompletionStreamChoice(chunk.Choices)
		body := map[string]any{
			"model":      chunk.Model,
			"created_at": formatCreatedAt(chunk.Created),
			"response":   choice.Text,
			"done":       choice.FinishReason != nil,
		}
		if choice.FinishReason != nil {
			body["done_reason"] = *choice.FinishReason
		}
		return body, choice.FinishReason != nil, nil
	})
}

func streamChatResponse(w http.ResponseWriter, upstream io.Reader) error {
	return relaySSE(w, upstream, func(data []byte) (map[string]any, bool, error) {
		var chunk openAIChatStreamChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return nil, false, fmt.Errorf("decode chat stream chunk: %w", err)
		}

		choice := firstChatStreamChoice(chunk.Choices)
		role := choice.Delta.Role
		if role == "" {
			role = "assistant"
		}
		body := map[string]any{
			"model":      chunk.Model,
			"created_at": formatCreatedAt(chunk.Created),
			"message": map[string]any{
				"role":    role,
				"content": choice.Delta.Content,
			},
			"done": choice.FinishReason != nil,
		}
		if choice.FinishReason != nil {
			body["done_reason"] = *choice.FinishReason
		}
		return body, choice.FinishReason != nil, nil
	})
}

func relaySSE(w http.ResponseWriter, upstream io.Reader, transform func([]byte) (map[string]any, bool, error)) error {
	setStreamingHeaders(w)
	flusher, _ := w.(http.Flusher)

	scanner := bufio.NewScanner(upstream)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	var lastChunk map[string]any
	var emittedFinal bool

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			if !emittedFinal {
				if lastChunk == nil {
					lastChunk = map[string]any{"done": true}
				} else {
					lastChunk["done"] = true
				}
				if _, ok := lastChunk["response"]; ok {
					lastChunk["response"] = ""
				}
				if message, ok := lastChunk["message"].(map[string]any); ok {
					message["content"] = ""
				}
				if err := writeNDJSONChunk(w, lastChunk); err != nil {
					return err
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			return nil
		}

		body, done, err := transform([]byte(payload))
		if err != nil {
			return err
		}
		lastChunk = cloneChunk(body)
		if err := writeNDJSONChunk(w, body); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		if done {
			emittedFinal = true
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read upstream stream: %w", err)
	}

	if !emittedFinal && lastChunk != nil {
		lastChunk["done"] = true
		if _, ok := lastChunk["response"]; ok {
			lastChunk["response"] = ""
		}
		if message, ok := lastChunk["message"].(map[string]any); ok {
			message["content"] = ""
		}
		if err := writeNDJSONChunk(w, lastChunk); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	return nil
}

func setStreamingHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
}

func writeNDJSONChunk(w http.ResponseWriter, body map[string]any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal stream chunk: %w", err)
	}
	if _, err := w.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write stream chunk: %w", err)
	}
	return nil
}

func cloneChunk(chunk map[string]any) map[string]any {
	cloned := make(map[string]any, len(chunk))
	for key, value := range chunk {
		if message, ok := value.(map[string]any); ok {
			messageClone := make(map[string]any, len(message))
			for mk, mv := range message {
				messageClone[mk] = mv
			}
			cloned[key] = messageClone
			continue
		}
		cloned[key] = value
	}
	return cloned
}

func firstCompletionStreamChoice(choices []struct {
	Text         string  `json:"text"`
	FinishReason *string `json:"finish_reason"`
}) struct {
	Text         string  `json:"text"`
	FinishReason *string `json:"finish_reason"`
} {
	if len(choices) == 0 {
		return struct {
			Text         string  `json:"text"`
			FinishReason *string `json:"finish_reason"`
		}{}
	}
	return choices[0]
}

func firstChatStreamChoice(choices []struct {
	Delta struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}) struct {
	Delta struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"delta"`
	FinishReason *string `json:"finish_reason"`
} {
	if len(choices) == 0 {
		return struct {
			Delta struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		}{}
	}
	return choices[0]
}

func extractNarrationPreamble(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if !strings.HasSuffix(trimmed, ":") {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "let me check the current state") {
		return content
	}
	if strings.HasPrefix(lower, "now let me add this patch") {
		return content
	}
	return ""
}