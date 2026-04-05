package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	markdownFencePattern   = regexp.MustCompile(`(?m)^\s*(` + "```" + `|~~~).*$`)
	markdownHeadingPattern = regexp.MustCompile(`^\s{0,3}#{1,6}\s*`)
	markdownQuotePattern   = regexp.MustCompile(`^\s*>\s*`)
	markdownULPattern      = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	markdownOLPattern      = regexp.MustCompile(`^\s*\d+\.\s+(.*)$`)
	markdownLinkPattern    = regexp.MustCompile(`\[(.*?)\]\((.*?)\)`)
)

type OpenAIClient struct {
	baseURL      string
	apiKey       string
	model        string
	systemPrompt string
	temperature  float64
	client       *http.Client
}

func NewOpenAIClient(cfg OpenAIConfig) *OpenAIClient {
	return &OpenAIClient{
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:       strings.TrimSpace(cfg.APIKey),
		model:        strings.TrimSpace(cfg.Model),
		systemPrompt: strings.TrimSpace(cfg.SystemPrompt),
		temperature:  cfg.Temperature,
		client: &http.Client{
			Timeout: time.Duration(cfg.RequestTimeoutSeconds) * time.Second,
		},
	}
}

func (c *OpenAIClient) Summarize(ctx context.Context, transcript string, images []SummaryImage) (string, error) {
	messages := []map[string]any{
		{
			"role":    "system",
			"content": c.systemPrompt,
		},
		{
			"role":    "user",
			"content": buildUserContent(transcript, images),
		},
	}

	payload := map[string]any{
		"model":       c.model,
		"messages":    messages,
		"temperature": c.temperature,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var response struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if response.Error != nil {
		return "", fmt.Errorf("api error: %s", response.Error.Message)
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("empty choices")
	}

	content := extractAssistantContent(response.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("empty assistant content")
	}

	return sanitizePlainTextReply(content), nil
}

func extractAssistantContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}

			text := firstMapString(itemMap, "text")
			if text == "" {
				if textBlock, ok := itemMap["text"].(map[string]any); ok {
					text = firstMapString(textBlock, "value", "text")
				}
			}
			if text == "" {
				text = firstMapString(itemMap, "output_text")
			}
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func firstMapString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := data[key]
		if !ok {
			continue
		}

		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case fmt.Stringer:
			if text := strings.TrimSpace(v.String()); text != "" {
				return text
			}
		}
	}
	return ""
}

func sanitizePlainTextReply(input string) string {
	// 虽然提示词已经要求模型输出纯文本，
	// 这里仍在发送前再清洗一遍常见 Markdown 语法。
	text := strings.ReplaceAll(input, "\r\n", "\n")
	text = markdownFencePattern.ReplaceAllString(text, "")
	text = markdownLinkPattern.ReplaceAllString(text, `$1 ($2)`)
	text = strings.NewReplacer(
		"**", "",
		"__", "",
		"~~", "",
		"`", "",
	).Replace(text)

	lines := strings.Split(text, "\n")
	itemIndex := 0
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			lines[i] = ""
			continue
		}

		line = markdownHeadingPattern.ReplaceAllString(line, "")
		line = markdownQuotePattern.ReplaceAllString(line, "")

		if matches := markdownOLPattern.FindStringSubmatch(line); len(matches) == 2 {
			itemIndex++
			line = "第" + strconv.Itoa(itemIndex) + "项：" + strings.TrimSpace(matches[1])
		} else if matches := markdownULPattern.FindStringSubmatch(line); len(matches) == 2 {
			itemIndex++
			line = "第" + strconv.Itoa(itemIndex) + "项：" + strings.TrimSpace(matches[1])
		}

		lines[i] = line
	}

	text = strings.Join(lines, "\n")
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func buildUserContent(transcript string, images []SummaryImage) any {
	instruction := "请总结下面这份 QQ 合并转发消息整理稿。输出中文纯文本结果，直接给总结，不要加寒暄。\n\n" +
		"要求：\n" +
		"1. 先概括主要话题。\n" +
		"2. 提炼关键结论、待办、时间、地点、人物。\n" +
		"3. 如有冲突、风险、未解决事项，单独指出。\n" +
		"4. 不要编造。\n" +
		"5. 禁止输出任何 Markdown 语法，包括标题、列表、代码块、引用、加粗、反引号、链接包装。"
	if len(images) > 0 {
		instruction += "\n6. 文本里出现的“图片#编号”与后续图片输入一一对应，请结合图片内容一起总结。"
	}

	if len(images) == 0 {
		return instruction + "\n\n聊天记录：\n" + transcript
	}

	parts := make([]map[string]any, 0, 1+len(images)*2)
	parts = append(parts, map[string]any{
		"type": "text",
		"text": instruction + "\n\n聊天记录：\n" + transcript,
	})

	for _, image := range images {
		label := "图片#" + strconv.Itoa(image.Index)
		if image.Caption != "" {
			label += "，备注：" + image.Caption
		}
		parts = append(parts, map[string]any{
			"type": "text",
			"text": label,
		})
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": image.URL,
			},
		})
	}

	return parts
}
