package main

import (
	"encoding/json"
	"testing"
)

func TestParseMessageSegmentsArray(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"text","data":{"text":"hello"}},
		{"type":"forward","data":{"id":"abc"}}
	]`)

	segments, err := parseMessageSegments(raw)
	if err != nil {
		t.Fatalf("parseMessageSegments returned error: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}

	ids := collectForwardIDs(segments)
	if len(ids) != 1 || ids[0] != "abc" {
		t.Fatalf("unexpected forward ids: %#v", ids)
	}
}

func TestParseMessageSegmentsString(t *testing.T) {
	raw := json.RawMessage(`"plain text"`)

	segments, err := parseMessageSegments(raw)
	if err != nil {
		t.Fatalf("parseMessageSegments returned error: %v", err)
	}
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segments[0].Type != "text" {
		t.Fatalf("expected text segment, got %s", segments[0].Type)
	}
}

func TestClipRunes(t *testing.T) {
	input := "1234567890"
	got := clipRunes(input, 6)
	if got == input {
		t.Fatalf("expected clipped output")
	}
}

func TestNormalizeImageURL(t *testing.T) {
	got := normalizeImageURL(map[string]any{
		"url": "https://example.com/image.png",
	})
	if got != "https://example.com/image.png" {
		t.Fatalf("unexpected image url: %q", got)
	}
}

func TestParseForwardNodes(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"sender": {"nickname":"张三","user_id":"1"},
			"time": 1710000000,
			"content": [{"type":"text","data":{"text":"hello"}}]
		}
	]`)

	nodes, err := parseForwardNodes(raw)
	if err != nil {
		t.Fatalf("parseForwardNodes returned error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
}

func TestFirstRawMessage(t *testing.T) {
	raw, ok, err := firstRawMessage(map[string]any{
		"content": []any{
			map[string]any{"type": "text", "data": map[string]any{"text": "hi"}},
		},
	}, "content")
	if err != nil {
		t.Fatalf("firstRawMessage returned error: %v", err)
	}
	if !ok {
		t.Fatalf("expected raw content")
	}

	segments, err := parseMessageSegments(raw)
	if err != nil {
		t.Fatalf("parseMessageSegments returned error: %v", err)
	}
	if len(segments) != 1 || segments[0].Type != "text" {
		t.Fatalf("unexpected segments: %#v", segments)
	}
}
