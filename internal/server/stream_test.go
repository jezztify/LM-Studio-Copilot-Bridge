package server

import (
	"testing"
)

func TestThinkingExtractorSimple(t *testing.T) {
	t.Parallel()
	e := &thinkingExtractor{}

	// Full think block in one chunk
	normal, thinking := e.extract("<think>reason here</think>answer")
	if normal != "answer" {
		t.Fatalf("normal = %q, want %q", normal, "answer")
	}
	if thinking != "reason here" {
		t.Fatalf("thinking = %q, want %q", thinking, "reason here")
	}
}

func TestThinkingExtractorNoThink(t *testing.T) {
	t.Parallel()
	e := &thinkingExtractor{}

	normal, thinking := e.extract("plain text")
	if normal != "plain text" {
		t.Fatalf("normal = %q", normal)
	}
	if thinking != "" {
		t.Fatalf("thinking = %q, want empty", thinking)
	}
}

func TestThinkingExtractorSplitAcrossChunks(t *testing.T) {
	t.Parallel()
	e := &thinkingExtractor{}

	// Opening tag split across two chunks: "<thi" then "nk>content</think>rest"
	n1, th1 := e.extract("<thi")
	if n1 != "" || th1 != "" {
		t.Fatalf("chunk1: normal=%q thinking=%q, want both empty", n1, th1)
	}
	if e.pending != "<thi" {
		t.Fatalf("pending = %q, want %q", e.pending, "<thi")
	}

	n2, th2 := e.extract("nk>content</think>rest")
	if n2 != "rest" {
		t.Fatalf("normal = %q, want %q", n2, "rest")
	}
	if th2 != "content" {
		t.Fatalf("thinking = %q, want %q", th2, "content")
	}
}

func TestThinkingExtractorClosingTagSplit(t *testing.T) {
	t.Parallel()
	e := &thinkingExtractor{}

	// Closing tag split: think is open, then "</thi" then "nk>after"
	e.inThinking = true

	n1, th1 := e.extract("inside</thi")
	if n1 != "" {
		t.Fatalf("normal = %q, want empty", n1)
	}
	if th1 != "inside" {
		t.Fatalf("thinking = %q, want %q", th1, "inside")
	}
	if e.pending != "</thi" {
		t.Fatalf("pending = %q, want %q", e.pending, "</thi")
	}

	n2, th2 := e.extract("nk>after")
	if n2 != "after" {
		t.Fatalf("normal = %q, want %q", n2, "after")
	}
	if th2 != "" {
		t.Fatalf("thinking = %q, want empty", th2)
	}
}

func TestThinkingExtractorMultipleBlocks(t *testing.T) {
	t.Parallel()
	e := &thinkingExtractor{}

	n, th := e.extract("<think>r1</think>a<think>r2</think>b")
	if n != "ab" {
		t.Fatalf("normal = %q, want %q", n, "ab")
	}
	if th != "r1r2" {
		t.Fatalf("thinking = %q, want %q", th, "r1r2")
	}
}

func TestThinkingExtractorProcessChunk(t *testing.T) {
	t.Parallel()
	e := &thinkingExtractor{}

	chunk := map[string]any{
		"id": "test",
		"choices": []any{
			map[string]any{
				"index": 0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": "<think>reasoning</think>answer",
				},
			},
		},
	}

	modified := e.processChunk(chunk)
	if !modified {
		t.Fatal("expected chunk to be modified")
	}
	delta := chunk["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "answer" {
		t.Fatalf("content = %q, want %q", delta["content"], "answer")
	}
	if delta["thinking"] != "reasoning" {
		t.Fatalf("thinking = %q, want %q", delta["thinking"], "reasoning")
	}
}

func TestThinkingExtractorProcessChunkNoThink(t *testing.T) {
	t.Parallel()
	e := &thinkingExtractor{}

	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"delta": map[string]any{
					"content": "hello",
				},
			},
		},
	}

	modified := e.processChunk(chunk)
	if modified {
		t.Fatal("expected chunk not to be modified")
	}
}

func TestPartialTagSuffixLen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s, tag string
		want   int
	}{
		{"hello<thi", "<think>", 4},
		{"hello<think", "<think>", 6},
		{"hello<think>", "<think>", 0}, // complete tag at end is not a partial prefix
		{"hello", "<think>", 0},
		{"</thi", "</think>", 5},
		{"</think", "</think>", 7},
		{"</think>", "</think>", 0},
	}
	for _, tc := range cases {
		got := partialTagSuffixLen(tc.s, tc.tag)
		if got != tc.want {
			t.Errorf("partialTagSuffixLen(%q, %q) = %d, want %d", tc.s, tc.tag, got, tc.want)
		}
	}
}
