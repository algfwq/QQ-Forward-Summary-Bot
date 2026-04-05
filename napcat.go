package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

type NapCatClient struct {
	logger  *log.Logger
	token   string
	onEvent func([]byte)

	upgrader websocket.Upgrader

	connMu  sync.RWMutex
	conn    *websocket.Conn
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan wsActionResult
	echoSeq   uint64
}

type wsActionResult struct {
	response wsActionResponse
	err      error
}

type wsActionResponse struct {
	Status  json.RawMessage `json:"status"`
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Wording string          `json:"wording"`
	Echo    json.RawMessage `json:"echo"`
}

func NewNapCatClient(token string, logger *log.Logger, onEvent func([]byte)) *NapCatClient {
	return &NapCatClient{
		logger:  logger,
		token:   strings.TrimSpace(token),
		onEvent: onEvent,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
		pending: make(map[string]chan wsActionResult),
	}
}

func (c *NapCatClient) GetForwardMessages(ctx context.Context, forwardID string) ([]ForwardNode, error) {
	var data struct {
		Messages []ForwardNode `json:"messages"`
	}

	err := c.call(ctx, "get_forward_msg", map[string]string{"message_id": forwardID}, &data)
	if err == nil {
		return data.Messages, nil
	}

	fallbackErr := c.call(ctx, "get_forward_msg", map[string]string{"id": forwardID}, &data)
	if fallbackErr == nil {
		return data.Messages, nil
	}

	return nil, fmt.Errorf("get_forward_msg failed: %v; fallback failed: %w", err, fallbackErr)
}

func (c *NapCatClient) SendPrivateReply(ctx context.Context, userID, messageID, text string) error {
	payload := map[string]any{
		"user_id": userID,
		"message": []MessageSegment{
			{
				Type: "reply",
				Data: map[string]any{"id": messageID},
			},
			{
				Type: "text",
				Data: map[string]any{"text": text},
			},
		},
	}

	return c.call(ctx, "send_private_msg", payload, nil)
}

func (c *NapCatClient) HandleReverseWebSocket(w http.ResponseWriter, r *http.Request) {
	if !c.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := c.upgrader.Upgrade(w, r, nil)
	if err != nil {
		c.logger.Printf("websocket upgrade failed: %v", err)
		return
	}

	conn.SetReadLimit(4 << 20)

	old := c.swapConn(conn)
	if old != nil {
		_ = old.Close()
		c.failPending(errors.New("napcat reverse websocket reconnected"))
	}

	c.logger.Printf("napcat reverse websocket connected from %s", r.RemoteAddr)
	go c.readLoop(conn)
}

func (c *NapCatClient) IsConnected() bool {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.conn != nil
}

func (c *NapCatClient) Close() error {
	c.failPending(errors.New("napcat reverse websocket closed"))

	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn == nil {
		return nil
	}

	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *NapCatClient) call(ctx context.Context, action string, payload any, out any) error {
	conn := c.getConn()
	if conn == nil {
		return errors.New("napcat reverse websocket is not connected")
	}

	// 反向 WS 的事件和动作调用共用一条连接，
	// 因此每个动作都要带 echo，用于匹配异步返回包。
	echo := fmt.Sprintf("echo-%d", atomic.AddUint64(&c.echoSeq, 1))
	requestBody, err := json.Marshal(map[string]any{
		"action": action,
		"params": payload,
		"echo":   echo,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resultCh := make(chan wsActionResult, 1)

	c.pendingMu.Lock()
	c.pending[echo] = resultCh
	c.pendingMu.Unlock()

	c.writeMu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, requestBody)
	c.writeMu.Unlock()
	if err != nil {
		c.removePending(echo)
		return fmt.Errorf("write websocket request: %w", err)
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			return result.err
		}
		if result.response.RetCode != 0 || statusIsFailed(result.response.Status) {
			msg := strings.TrimSpace(strings.Join([]string{result.response.Message, result.response.Wording}, " "))
			return fmt.Errorf("napcat retcode=%d: %s", result.response.RetCode, msg)
		}
		if out != nil && len(strings.TrimSpace(string(result.response.Data))) > 0 && string(result.response.Data) != "null" {
			if err := json.Unmarshal(result.response.Data, out); err != nil {
				return fmt.Errorf("decode response data: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		c.removePending(echo)
		return ctx.Err()
	}
}

func (c *NapCatClient) authorize(r *http.Request) bool {
	if c.token == "" {
		return true
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == c.token || auth == "Bearer "+c.token {
		return true
	}

	return strings.TrimSpace(r.URL.Query().Get("access_token")) == c.token
}

func (c *NapCatClient) readLoop(conn *websocket.Conn) {
	defer func() {
		_ = conn.Close()
		c.clearConn(conn)
		c.failPending(errors.New("napcat reverse websocket disconnected"))
		c.logger.Printf("napcat reverse websocket disconnected")
	}()

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}

		if c.handleResponse(payload) {
			continue
		}
		if c.onEvent != nil {
			go c.onEvent(payload)
		}
	}
}

func (c *NapCatClient) handleResponse(payload []byte) bool {
	var probe struct {
		PostType string          `json:"post_type"`
		Status   json.RawMessage `json:"status"`
		Echo     json.RawMessage `json:"echo"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		c.logger.Printf("decode incoming websocket message failed: %v", err)
		return false
	}
	if probe.PostType != "" {
		return false
	}
	// 心跳等元事件里的 status 可能是对象。
	// 只有没有 post_type、同时带 status 或 echo 的包，才按动作响应处理。
	if len(bytesTrimJSON(probe.Status)) == 0 && len(probe.Echo) == 0 {
		return false
	}

	var response wsActionResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		c.logger.Printf("decode websocket action response failed: %v", err)
		return true
	}

	echo, err := normalizeID(response.Echo)
	if err != nil {
		return true
	}

	c.pendingMu.Lock()
	resultCh, ok := c.pending[echo]
	if ok {
		delete(c.pending, echo)
	}
	c.pendingMu.Unlock()
	if !ok {
		return true
	}

	resultCh <- wsActionResult{response: response}
	close(resultCh)
	return true
}

func bytesTrimJSON(raw json.RawMessage) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func statusIsFailed(raw json.RawMessage) bool {
	trimmed := bytesTrimJSON(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return false
	}

	var asString string
	if err := json.Unmarshal(trimmed, &asString); err == nil {
		return strings.EqualFold(strings.TrimSpace(asString), "failed")
	}

	return false
}

func (c *NapCatClient) swapConn(conn *websocket.Conn) *websocket.Conn {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	old := c.conn
	c.conn = conn
	return old
}

func (c *NapCatClient) clearConn(conn *websocket.Conn) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn == conn {
		c.conn = nil
	}
}

func (c *NapCatClient) getConn() *websocket.Conn {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.conn
}

func (c *NapCatClient) removePending(echo string) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	delete(c.pending, echo)
}

func (c *NapCatClient) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	for echo, resultCh := range c.pending {
		resultCh <- wsActionResult{err: err}
		close(resultCh)
		delete(c.pending, echo)
	}
}
