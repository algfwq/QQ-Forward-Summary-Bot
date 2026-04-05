package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type Bot struct {
	cfg    Config
	logger *log.Logger
	root   context.Context
	napcat *NapCatClient
	openai *OpenAIClient
}

func NewBot(root context.Context, cfg Config, logger *log.Logger) *Bot {
	bot := &Bot{
		cfg:    cfg,
		logger: logger,
		root:   root,
		openai: NewOpenAIClient(cfg.OpenAI),
	}
	bot.napcat = NewNapCatClient(cfg.Server.AccessToken, logger, bot.handleIncomingMessage)
	return bot
}

func (b *Bot) HandleReverseWebSocket(w http.ResponseWriter, r *http.Request) {
	b.napcat.HandleReverseWebSocket(w, r)
}

func (b *Bot) handleIncomingMessage(body []byte) {
	var event IncomingEvent
	if err := json.Unmarshal(body, &event); err != nil {
		b.logger.Printf("decode websocket event failed: %v", err)
		return
	}

	if event.PostType != "message" || event.MessageType != "private" {
		return
	}

	userID, err := normalizeID(event.UserID)
	if err != nil {
		b.logger.Printf("skip event with invalid user_id: %v", err)
		return
	}
	messageID, err := normalizeID(event.MessageID)
	if err != nil {
		b.logger.Printf("skip event with invalid message_id: %v", err)
		return
	}
	if selfID, err := normalizeID(event.SelfID); err == nil && selfID == userID {
		return
	}

	segments, err := parseMessageSegments(event.Message)
	if err != nil {
		b.logger.Printf("parse message segments failed: %v", err)
		return
	}

	forwardIDs := collectForwardIDs(segments)
	if len(forwardIDs) == 0 {
		return
	}

	b.logger.Printf("received private forward message: user_id=%s message_id=%s forward_count=%d", userID, messageID, len(forwardIDs))
	go b.processForwardMessage(userID, messageID, forwardIDs)
}

func (b *Bot) processForwardMessage(userID, messageID string, forwardIDs []string) {
	ctx, cancel := context.WithTimeout(b.root, time.Duration(b.cfg.Bot.ProcessTimeoutSeconds)*time.Second)
	defer cancel()

	// 在展开合并转发时同步收集图片，保证整理出来的文字顺序
	// 与后续发送给多模态模型的图片编号保持一致。
	collector := NewImageCollector(b.cfg.Bot.MaxImages)
	sections := make([]string, 0, len(forwardIDs))
	for index, forwardID := range forwardIDs {
		b.logger.Printf("expanding forward message: user_id=%s message_id=%s forward_id=%s", userID, messageID, forwardID)
		text, err := b.expandForward(ctx, forwardID, 0, map[string]struct{}{}, "", collector)
		if err != nil {
			b.logger.Printf("expand forward %s failed: %v", forwardID, err)
			sections = append(sections, fmt.Sprintf("合并转发 #%d 解析失败：%v", index+1, err))
			continue
		}
		sections = append(sections, fmt.Sprintf("合并转发 #%d\n%s", index+1, text))
	}

	transcript := clipRunes(strings.Join(sections, "\n\n"), b.cfg.Bot.SummaryInputLimit)
	if strings.TrimSpace(transcript) == "" {
		transcript = "未解析到有效内容。"
	}

	images := collector.Images()
	b.logger.Printf("prepared summary input: user_id=%s message_id=%s transcript_chars=%d image_count=%d", userID, messageID, len([]rune(transcript)), len(images))

	summary, err := b.openai.Summarize(ctx, transcript, images)
	if err != nil {
		b.logger.Printf("summarize failed: %v", err)
		summary = "已收到合并转发消息，但调用 AI 总结失败：" + err.Error()
	}
	summary = sanitizePlainTextReply(summary)

	if err := b.napcat.SendPrivateReply(ctx, userID, messageID, summary); err != nil {
		b.logger.Printf("send reply failed: %v", err)
		return
	}
	b.logger.Printf("reply sent: user_id=%s message_id=%s summary_chars=%d", userID, messageID, len([]rune(summary)))
}

func (b *Bot) expandForward(ctx context.Context, forwardID string, depth int, visited map[string]struct{}, indent string, collector *ImageCollector) (string, error) {
	if depth >= b.cfg.Bot.MaxForwardDepth {
		return indent + "[嵌套合并转发过深，已停止展开]", nil
	}
	if _, exists := visited[forwardID]; exists {
		return indent + "[检测到循环引用的合并转发，已停止展开]", nil
	}

	visited[forwardID] = struct{}{}
	defer delete(visited, forwardID)

	nodes, err := b.napcat.GetForwardMessages(ctx, forwardID)
	if err != nil {
		return "", err
	}
	b.logger.Printf("forward fetched: forward_id=%s depth=%d node_count=%d", forwardID, depth, len(nodes))
	if len(nodes) == 0 {
		return indent + "[空合并转发]", nil
	}

	return b.renderForwardNodes(ctx, nodes, depth, visited, indent, collector)
}

func (b *Bot) renderForwardNodes(ctx context.Context, nodes []ForwardNode, depth int, visited map[string]struct{}, indent string, collector *ImageCollector) (string, error) {
	if len(nodes) == 0 {
		return indent + "[空合并转发]", nil
	}

	lines := make([]string, 0, len(nodes)*2)
	for index, node := range nodes {
		header := fmt.Sprintf("%s%d. %s", indent, index+1, senderName(node.Sender))
		if ts := formatForwardTime(node.Time); ts != "" {
			header += " [" + ts + "]"
		}
		lines = append(lines, header)

		body, err := b.renderNode(ctx, node, depth, visited, indent+"  ", collector)
		if err != nil {
			lines = append(lines, indent+"  [消息解析失败: "+err.Error()+"]")
			continue
		}
		lines = append(lines, body)
	}

	return strings.Join(lines, "\n"), nil
}

func (b *Bot) renderNode(ctx context.Context, node ForwardNode, depth int, visited map[string]struct{}, indent string, collector *ImageCollector) (string, error) {
	segments, err := node.Segments()
	if err != nil {
		return "", err
	}
	if len(segments) == 0 {
		return indent + "[空消息]", nil
	}
	b.logger.Printf("render forward node: depth=%d segment_types=%s", depth, segmentTypesSummary(segments))

	return b.renderSegments(ctx, segments, depth, visited, indent, collector)
}

func (b *Bot) renderSegments(ctx context.Context, segments []MessageSegment, depth int, visited map[string]struct{}, indent string, collector *ImageCollector) (string, error) {
	lines := make([]string, 0, len(segments))
	inline := make([]string, 0, len(segments))
	flushInline := func() {
		if len(inline) == 0 {
			return
		}
		lines = append(lines, indent+strings.Join(inline, ""))
		inline = inline[:0]
	}

	for _, segment := range segments {
		switch segment.Type {
		case "forward":
			flushInline()
			nested, handled, err := b.renderForwardSegment(ctx, segment, depth, visited, indent, collector)
			if err != nil {
				lines = append(lines, indent+"[嵌套合并转发解析失败: "+err.Error()+"]")
				continue
			}
			if handled {
				lines = append(lines, nested)
				continue
			}
			lines = append(lines, indent+"[嵌套合并转发缺少内容]")
		case "node":
			flushInline()
			nested, handled, err := b.renderNodeSegment(ctx, segment, depth, visited, indent, collector)
			if err != nil {
				lines = append(lines, indent+"[节点消息解析失败: "+err.Error()+"]")
				continue
			}
			if handled {
				lines = append(lines, nested)
				continue
			}
			lines = append(lines, bracketed("node", firstString(segment.Data, "nickname", "user_id", "id")))
		default:
			inline = append(inline, renderInlineSegment(segment, collector))
		}
	}

	flushInline()
	if len(lines) == 0 {
		return indent + "[空消息]", nil
	}

	return strings.Join(lines, "\n"), nil
}

func (b *Bot) renderForwardSegment(ctx context.Context, segment MessageSegment, depth int, visited map[string]struct{}, indent string, collector *ImageCollector) (string, bool, error) {
	// 优先消费段里自带的嵌套内容，而不是立刻再次调用 get_forward_msg。
	// NapCat 可能已经把内层内容塞进 content 里，而内层 id 本身可能会过期。
	if raw, ok, err := firstRawMessage(segment.Data, "content"); err != nil {
		return "", false, err
	} else if ok {
		if nodes, err := parseForwardNodes(raw); err == nil && len(nodes) > 0 {
			b.logger.Printf("render nested forward from embedded nodes: depth=%d node_count=%d", depth+1, len(nodes))
			text, renderErr := b.renderForwardNodes(ctx, nodes, depth+1, visited, indent+"  ", collector)
			if renderErr != nil {
				return "", false, renderErr
			}
			return indent + "[嵌套合并转发]\n" + text, true, nil
		}
		if segments, err := parseMessageSegments(raw); err == nil && len(segments) > 0 {
			b.logger.Printf("render nested forward from embedded segments: depth=%d segment_types=%s", depth+1, segmentTypesSummary(segments))
			text, renderErr := b.renderSegments(ctx, segments, depth+1, visited, indent+"  ", collector)
			if renderErr != nil {
				return "", false, renderErr
			}
			return indent + "[嵌套合并转发]\n" + text, true, nil
		}
	}

	nestedID := firstString(segment.Data, "id", "message_id")
	if nestedID == "" {
		return "", false, nil
	}

	b.logger.Printf("render nested forward by api lookup: depth=%d forward_id=%s", depth+1, nestedID)
	text, err := b.expandForward(ctx, nestedID, depth+1, visited, indent+"  ", collector)
	if err != nil {
		return "", false, err
	}
	return indent + "[嵌套合并转发]\n" + text, true, nil
}

func (b *Bot) renderNodeSegment(ctx context.Context, segment MessageSegment, depth int, visited map[string]struct{}, indent string, collector *ImageCollector) (string, bool, error) {
	// 部分嵌套转发内容会以 node 段而不是 forward 段出现，
	// 这里同时兼容内嵌内容和基于 id 的回退解析。
	if raw, ok, err := firstRawMessage(segment.Data, "content", "message"); err != nil {
		return "", false, err
	} else if ok {
		if nodes, err := parseForwardNodes(raw); err == nil && len(nodes) > 0 {
			b.logger.Printf("render node segment as nested forward nodes: depth=%d node_count=%d", depth+1, len(nodes))
			text, renderErr := b.renderForwardNodes(ctx, nodes, depth+1, visited, indent, collector)
			if renderErr != nil {
				return "", false, renderErr
			}
			return text, true, nil
		}

		if segments, err := parseMessageSegments(raw); err == nil && len(segments) > 0 {
			sender := senderName(ForwardSender{
				UserID:   segment.Data["user_id"],
				Nickname: firstString(segment.Data, "nickname"),
				Card:     firstString(segment.Data, "name"),
			})
			b.logger.Printf("render node segment from embedded content: depth=%d sender=%s segment_types=%s", depth+1, sender, segmentTypesSummary(segments))
			body, renderErr := b.renderSegments(ctx, segments, depth+1, visited, indent+"  ", collector)
			if renderErr != nil {
				return "", false, renderErr
			}
			return indent + sender + "\n" + body, true, nil
		}
	}

	nestedID := firstString(segment.Data, "id", "message_id")
	if nestedID == "" {
		return "", false, nil
	}

	b.logger.Printf("render node segment by api lookup: depth=%d forward_id=%s", depth+1, nestedID)
	text, err := b.expandForward(ctx, nestedID, depth+1, visited, indent+"  ", collector)
	if err != nil {
		return "", false, err
	}
	return indent + "[嵌套合并转发]\n" + text, true, nil
}

func renderInlineSegment(segment MessageSegment, collector *ImageCollector) string {
	switch segment.Type {
	case "text":
		return firstString(segment.Data, "text")
	case "at":
		return "@" + firstString(segment.Data, "qq")
	case "reply":
		return "[回复消息#" + firstString(segment.Data, "id") + "]"
	case "image":
		image, ok, caption := collector.AddSegment(segment.Data)
		if !ok {
			detail := firstString(segment.Data, "summary", "file", "url")
			if detail == "" {
				detail = caption
			}
			return bracketed("图片", detail)
		}
		if image.Caption == "" {
			return "[图片#" + fmt.Sprint(image.Index) + "]"
		}
		return "[图片#" + fmt.Sprint(image.Index) + ": " + image.Caption + "]"
	case "file":
		return bracketed("文件", firstString(segment.Data, "name", "file"))
	case "record":
		return bracketed("语音", firstString(segment.Data, "file", "path"))
	case "video":
		return bracketed("视频", firstString(segment.Data, "file", "url"))
	case "json":
		return bracketed("JSON消息", firstString(segment.Data, "data"))
	case "face":
		return bracketed("表情", firstString(segment.Data, "id"))
	case "mface":
		return bracketed("商城表情", firstString(segment.Data, "summary", "emoji_id"))
	case "dice":
		return bracketed("骰子", firstString(segment.Data, "result"))
	case "rps":
		return bracketed("猜拳", firstString(segment.Data, "result"))
	case "poke":
		return bracketed("戳一戳", firstString(segment.Data, "type", "id"))
	case "contact":
		return bracketed("联系人卡片", firstString(segment.Data, "nickname", "id"))
	case "music":
		return bracketed("音乐", firstString(segment.Data, "title", "id", "url"))
	default:
		return bracketed(segment.Type, firstString(segment.Data, "text", "id", "file", "url"))
	}
}

func bracketed(kind, detail string) string {
	if strings.TrimSpace(detail) == "" {
		return "[" + kind + "]"
	}
	return "[" + kind + ": " + detail + "]"
}

func (b *Bot) IsConnected() bool {
	return b.napcat.IsConnected()
}

func (b *Bot) Close() error {
	return b.napcat.Close()
}
