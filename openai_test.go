package main

import (
	"strings"
	"testing"
)

func TestExtractAssistantContentString(t *testing.T) {
	got := extractAssistantContent("总结结果")
	if got != "总结结果" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestExtractAssistantContentArray(t *testing.T) {
	content := []any{
		map[string]any{
			"type": "text",
			"text": "第一段",
		},
		map[string]any{
			"type":        "output_text",
			"output_text": "第二段",
		},
	}

	got := extractAssistantContent(content)
	want := "第一段\n第二段"
	if got != want {
		t.Fatalf("unexpected content: got %q want %q", got, want)
	}
}

func TestSanitizePlainTextReply(t *testing.T) {
	input := "# 标题\n- 第一项\n1. 第二项\n> 引用\n**加粗** [链接](https://example.com)\n```txt\ncode\n```"
	got := sanitizePlainTextReply(input)

	if got == input {
		t.Fatalf("expected sanitized output")
	}
	if got == "" {
		t.Fatalf("expected non-empty output")
	}
	for _, disallowed := range []string{"# ", "- ", "1. ", "> ", "**", "`"} {
		if strings.Contains(got, disallowed) {
			t.Fatalf("unexpected markdown token %q in %q", disallowed, got)
		}
	}
}

func TestBuildUserContentWithImages(t *testing.T) {
	content := buildUserContent("聊天记录正文", []SummaryImage{
		{Index: 1, URL: "https://example.com/a.png", Caption: "示例图"},
	})

	parts, ok := content.([]map[string]any)
	if !ok {
		t.Fatalf("expected multimodal content array")
	}
	if len(parts) != 3 {
		t.Fatalf("unexpected content parts: %d", len(parts))
	}
	if parts[2]["type"] != "image_url" {
		t.Fatalf("expected image_url part, got %#v", parts[2]["type"])
	}
}
