package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type IncomingEvent struct {
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SubType     string          `json:"sub_type"`
	MessageID   json.RawMessage `json:"message_id"`
	UserID      json.RawMessage `json:"user_id"`
	SelfID      json.RawMessage `json:"self_id"`
	Message     json.RawMessage `json:"message"`
	RawMessage  string          `json:"raw_message"`
}

type MessageSegment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type ForwardSender struct {
	UserID   any    `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
	Remark   string `json:"remark"`
	UIN      any    `json:"uin"`
}

type ForwardNode struct {
	Sender  ForwardSender   `json:"sender"`
	Time    json.RawMessage `json:"time"`
	Content json.RawMessage `json:"content"`
	Message json.RawMessage `json:"message"`
}

type SummaryImage struct {
	Index   int
	URL     string
	Caption string
}

type ImageCollector struct {
	max    int
	images []SummaryImage
}

func NewImageCollector(max int) *ImageCollector {
	return &ImageCollector{max: max}
}

func (c *ImageCollector) Images() []SummaryImage {
	if c == nil {
		return nil
	}

	out := make([]SummaryImage, len(c.images))
	copy(out, c.images)
	return out
}

func (c *ImageCollector) AddSegment(data map[string]any) (SummaryImage, bool, string) {
	caption := strings.TrimSpace(firstString(data, "summary"))
	url := normalizeImageURL(data)
	if url == "" {
		return SummaryImage{}, false, caption
	}
	if c == nil {
		return SummaryImage{}, false, caption
	}
	if c.max > 0 && len(c.images) >= c.max {
		return SummaryImage{}, false, caption
	}

	image := SummaryImage{
		Index:   len(c.images) + 1,
		URL:     url,
		Caption: caption,
	}
	c.images = append(c.images, image)
	return image, true, caption
}

func (n ForwardNode) Segments() ([]MessageSegment, error) {
	if len(bytes.TrimSpace(n.Content)) > 0 && !bytes.Equal(bytes.TrimSpace(n.Content), []byte("null")) {
		return parseMessageSegments(n.Content)
	}
	if len(bytes.TrimSpace(n.Message)) > 0 && !bytes.Equal(bytes.TrimSpace(n.Message), []byte("null")) {
		return parseMessageSegments(n.Message)
	}
	return nil, nil
}

func parseMessageSegments(raw json.RawMessage) ([]MessageSegment, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}

	var list []MessageSegment
	if err := json.Unmarshal(trimmed, &list); err == nil {
		return list, nil
	}

	var single MessageSegment
	if err := json.Unmarshal(trimmed, &single); err == nil && single.Type != "" {
		return []MessageSegment{single}, nil
	}

	var wrapped struct {
		Content json.RawMessage `json:"content"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(trimmed, &wrapped); err == nil {
		if len(bytes.TrimSpace(wrapped.Content)) > 0 {
			return parseMessageSegments(wrapped.Content)
		}
		if len(bytes.TrimSpace(wrapped.Message)) > 0 {
			return parseMessageSegments(wrapped.Message)
		}
	}

	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return []MessageSegment{{
			Type: "text",
			Data: map[string]any{"text": text},
		}}, nil
	}

	return nil, fmt.Errorf("unsupported message format: %s", string(trimmed))
}

func parseForwardNodes(raw json.RawMessage) ([]ForwardNode, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}

	// NapCat 返回的合并转发内容有时是裸节点数组，
	// 有时又包在 data/messages/content/message 这些字段里，这里统一兼容。
	var nodes []ForwardNode
	if err := json.Unmarshal(trimmed, &nodes); err == nil {
		return nodes, nil
	}

	var wrapped struct {
		Messages json.RawMessage `json:"messages"`
		Content  json.RawMessage `json:"content"`
		Message  json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(trimmed, &wrapped); err == nil {
		if len(bytes.TrimSpace(wrapped.Messages)) > 0 {
			return parseForwardNodes(wrapped.Messages)
		}
		if len(bytes.TrimSpace(wrapped.Content)) > 0 {
			return parseForwardNodes(wrapped.Content)
		}
		if len(bytes.TrimSpace(wrapped.Message)) > 0 {
			return parseForwardNodes(wrapped.Message)
		}
	}

	var single ForwardNode
	if err := json.Unmarshal(trimmed, &single); err == nil {
		if len(bytes.TrimSpace(single.Content)) > 0 || len(bytes.TrimSpace(single.Message)) > 0 {
			return []ForwardNode{single}, nil
		}
	}

	return nil, fmt.Errorf("unsupported forward node format: %s", string(trimmed))
}

func rawMessageFromValue(value any) (json.RawMessage, bool, error) {
	if value == nil {
		return nil, false, nil
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false, err
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false, nil
	}

	return json.RawMessage(trimmed), true, nil
}

func firstRawMessage(data map[string]any, keys ...string) (json.RawMessage, bool, error) {
	for _, key := range keys {
		value, ok := data[key]
		if !ok {
			continue
		}

		raw, hasValue, err := rawMessageFromValue(value)
		if err != nil {
			return nil, false, err
		}
		if hasValue {
			return raw, true, nil
		}
	}

	return nil, false, nil
}

func normalizeID(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", fmt.Errorf("missing id")
	}

	var asString string
	if err := json.Unmarshal(trimmed, &asString); err == nil {
		if asString == "" {
			return "", fmt.Errorf("empty id")
		}
		return asString, nil
	}

	var asNumber json.Number
	if err := json.Unmarshal(trimmed, &asNumber); err == nil {
		return asNumber.String(), nil
	}

	var asInt int64
	if err := json.Unmarshal(trimmed, &asInt); err == nil {
		return strconv.FormatInt(asInt, 10), nil
	}

	return "", fmt.Errorf("invalid id: %s", string(trimmed))
}

func normalizeAnyID(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case float32:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	default:
		return fmt.Sprint(v)
	}
}

func firstString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := data[key]
		if !ok {
			continue
		}

		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return v
			}
		case fmt.Stringer:
			text := strings.TrimSpace(v.String())
			if text != "" {
				return text
			}
		default:
			text := strings.TrimSpace(normalizeAnyID(v))
			if text != "" {
				return text
			}
		}
	}

	return ""
}

func collectForwardIDs(segments []MessageSegment) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, segment := range segments {
		if segment.Type != "forward" {
			continue
		}

		id := firstString(segment.Data, "id")
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}

		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func senderName(sender ForwardSender) string {
	if name := strings.TrimSpace(sender.Card); name != "" {
		return name
	}
	if name := strings.TrimSpace(sender.Remark); name != "" {
		return name
	}
	if name := strings.TrimSpace(sender.Nickname); name != "" {
		return name
	}
	if id := normalizeAnyID(sender.UserID); id != "" {
		return id
	}
	if id := normalizeAnyID(sender.UIN); id != "" {
		return id
	}
	return "未知发送者"
}

func formatForwardTime(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}

	var asNumber json.Number
	if err := json.Unmarshal(trimmed, &asNumber); err == nil {
		if seconds, err := asNumber.Int64(); err == nil && seconds > 0 {
			return time.Unix(seconds, 0).Local().Format("2006-01-02 15:04:05")
		}
	}

	var asString string
	if err := json.Unmarshal(trimmed, &asString); err == nil {
		asString = strings.TrimSpace(asString)
		if asString == "" {
			return ""
		}
		if seconds, err := strconv.ParseInt(asString, 10, 64); err == nil && seconds > 0 {
			return time.Unix(seconds, 0).Local().Format("2006-01-02 15:04:05")
		}
		if parsed, err := time.Parse(time.RFC3339, asString); err == nil {
			return parsed.Local().Format("2006-01-02 15:04:05")
		}
	}

	return ""
}

func clipRunes(input string, limit int) string {
	if limit <= 0 {
		return input
	}

	runes := []rune(input)
	if len(runes) <= limit {
		return input
	}

	headLimit := limit * 2 / 3
	tailLimit := limit - headLimit
	head := string(runes[:headLimit])
	tail := string(runes[len(runes)-tailLimit:])
	return head + "\n\n[内容过长，已截断中间部分]\n\n" + tail
}

func normalizeImageURL(data map[string]any) string {
	candidates := []string{
		firstString(data, "url"),
		firstString(data, "file"),
		firstString(data, "path"),
	}

	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate)
		if value == "" {
			continue
		}

		switch {
		case strings.HasPrefix(value, "http://"), strings.HasPrefix(value, "https://"), strings.HasPrefix(value, "data:"):
			return value
		case strings.HasPrefix(value, "base64://"):
			ext := strings.ToLower(filepath.Ext(firstString(data, "file")))
			mime := "image/png"
			switch ext {
			case ".jpg", ".jpeg":
				mime = "image/jpeg"
			case ".gif":
				mime = "image/gif"
			case ".webp":
				mime = "image/webp"
			}
			return "data:" + mime + ";base64," + strings.TrimPrefix(value, "base64://")
		}
	}

	return ""
}

func segmentTypesSummary(segments []MessageSegment) string {
	if len(segments) == 0 {
		return ""
	}

	counts := make(map[string]int)
	order := make([]string, 0)
	for _, segment := range segments {
		if _, ok := counts[segment.Type]; !ok {
			order = append(order, segment.Type)
		}
		counts[segment.Type]++
	}

	parts := make([]string, 0, len(order))
	for _, kind := range order {
		parts = append(parts, fmt.Sprintf("%s=%d", kind, counts[kind]))
	}
	return strings.Join(parts, ",")
}
