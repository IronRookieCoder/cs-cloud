package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"cs-cloud/internal/logger"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type wsControlMsg struct {
	Type string `json:"t"`
	SessionID string `json:"s,omitempty"`
	Version int    `json:"v,omitempty"`
}

type InputWsHandler struct {
	mgr *TerminalManager
}

func NewInputWsHandler(mgr *TerminalManager) *InputWsHandler {
	return &InputWsHandler{mgr: mgr}
}

func (h *InputWsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// InsecureSkipVerify disables the library's Origin check. This endpoint
	// is reached both from local dev (http://localhost:*) and via the cloud
	// tunnel, whose browser-side Origin is the production domain — neither
	// fits a static allowlist, so Origin filtering breaks legitimate traffic
	// while buying nothing: the real CSRF barrier is that auth credentials
	// are not carried in cookies (Authorization header / WS subprotocol),
	// so a cross-site page cannot forge an authenticated request regardless.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		logger.Error("terminal: ws accept error: %v", err)
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	ws := &inputWsConn{
		conn:   conn,
		mgr:    h.mgr,
		ctx:    ctx,
		cancel: cancel,
	}
	ws.run()
}

type inputWsConn struct {
	conn      *websocket.Conn
	mgr       *TerminalManager
	ctx       context.Context
	cancel    context.CancelFunc
	sessionID string
	mu        sync.Mutex
}

func (c *inputWsConn) run() {
	defer c.cancel()

	go c.pingLoop()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		msgType, data, err := c.conn.Read(c.ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				logger.Debug("terminal: ws read error: %v", err)
			}
			return
		}

		switch msgType {
		case websocket.MessageText:
			text := string(data)
			if len(text) > 0 && text[0] == '{' {
				c.handleControl(text)
			} else {
				c.handleInput(data)
			}
		case websocket.MessageBinary:
			if len(data) > 0 && data[0] == 0x01 {
				c.handleControl(string(data[1:]))
			} else {
				c.handleInput(data)
			}
		}
	}
}

func (c *inputWsConn) handleControl(text string) {
	var msg wsControlMsg
	if err := json.Unmarshal([]byte(text), &msg); err != nil {
		return
	}

	switch msg.Type {
	case "b":
		c.mu.Lock()
		c.sessionID = msg.SessionID
		c.mu.Unlock()
		logger.Debug("terminal: ws bound to session %s", msg.SessionID)
	case "p":
		c.sendPong()
	default:
		logger.Debug("terminal: ws unknown control type=%s", msg.Type)
	}
}

func (c *inputWsConn) handleInput(data []byte) {
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()

	if sid == "" {
		logger.Debug("terminal: ws input dropped, no session bound")
		return
	}

	if err := c.mgr.Write(sid, data); err != nil {
		logger.Debug("terminal: ws write to session %s error: %v", sid, err)
	}
}

func (c *inputWsConn) sendPong() {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()

	wsjson.Write(ctx, c.conn, wsControlMsg{Type: "po", Version: 1})
}

func (c *inputWsConn) pingLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.sendPong()
		}
	}
}
