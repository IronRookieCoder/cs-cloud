package tunnel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"cs-cloud/internal/config"
	"cs-cloud/internal/device"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/version"

	"github.com/hashicorp/yamux"
	"nhooyr.io/websocket"
)

const (
	initialDelay       = 1 * time.Second
	maxDelay           = 60 * time.Second
	wsConnectTimeout   = 15 * time.Second
	rateLimitBaseDelay = 30 * time.Second  // 429 限流的基础等待时间（无 Retry-After 头时使用）
	rateLimitMaxDelay  = 5 * time.Minute   // 429 限流的最大等待时间
)

func Connect(ctx context.Context, localPort int, cfg *config.Config, onSessionChange func(connected bool)) error {
	attempt := 0
	rateLimitAttempt := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		dev, err := device.LoadDevice()
		if err != nil || dev == nil {
			return fmt.Errorf("device not registered, cannot connect tunnel")
		}

		if ownerErr := device.ValidateDeviceOwner(dev); ownerErr != nil {
			logger.Warn("[tunnel] %v, attempting re-registration...", ownerErr)
			dev, err = device.ReRegister(ctx, cfg)
			if err != nil {
				return fmt.Errorf("re-register failed: %w", err)
			}
			logger.Info("[tunnel] device re-registered successfully (device_id=%s)", dev.DeviceID)
		}

		gatewayURL, err := device.AssignGateway(ctx, dev)
		if err != nil {
			// 检测是否是限流错误（429），使用 Retry-After 或较长的退避
			if device.IsGatewayAssignRateLimitError(err) {
				delay := rateLimitBackoff(err, rateLimitAttempt)
				rateLimitAttempt++
				logger.Warn("[tunnel] gateway-assign rate limited (429), waiting %v before retry (attempt=%d)", delay, rateLimitAttempt)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
				}
				continue
			}
			rateLimitAttempt = 0

			// 检测是否是认证错误（401/403），需要重新注册
			if device.IsGatewayAssignAuthError(err) {
				logger.Warn("[tunnel] device token invalid (%v), attempting re-registration...", err)
				dev, err = device.ReRegister(ctx, cfg)
				if err != nil {
					return fmt.Errorf("re-register failed: %w", err)
				}
				logger.Info("[tunnel] device re-registered successfully (device_id=%s)", dev.DeviceID)
				attempt = 0
				continue
			}

			logger.Warn("[tunnel] gateway-assign failed: %v, retrying...", err)
			delay := backoff(attempt)
			attempt++

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		rateLimitAttempt = 0

		err = runSession(ctx, gatewayURL, dev.DeviceID, dev.DeviceToken, localPort, onSessionChange)
		if err != nil {
			logger.Warn("[tunnel] session error: %v", err)
		}

		logger.Info("[tunnel] session ended, reconnecting...")
		attempt = 0

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(initialDelay):
		}
	}
}

func runSession(ctx context.Context, gatewayURL, deviceID, deviceToken string, localPort int, onSessionChange func(connected bool)) error {
	wsURL := strings.Replace(gatewayURL, "http", "ws", 1)
	wsURL = fmt.Sprintf("%s/device/%s/tunnel?token=%s&client_version=%s", wsURL, deviceID, url.QueryEscape(deviceToken), url.QueryEscape(version.Get()))

	logger.Info("[tunnel] connecting to %s", redactToken(wsURL))

	connectCtx, cancel := context.WithTimeout(ctx, wsConnectTimeout)
	defer cancel()

	conn, resp, err := websocket.Dial(connectCtx, wsURL, nil)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			if readErr == nil && len(body) > 0 {
				return fmt.Errorf("ws connect failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			return fmt.Errorf("ws connect failed (HTTP %d): %w", resp.StatusCode, err)
		}
		return fmt.Errorf("ws connect failed: %w", err)
	}

	logger.Info("[tunnel] connected, device_id=%s", deviceID)

	if onSessionChange != nil {
		onSessionChange(true)
	}

	wsNetConn := &wsNetConn{Conn: conn}
	defer wsNetConn.Close()
	defer func() {
		if onSessionChange != nil {
			onSessionChange(false)
		}
	}()

	// 客户端主动发送 WebSocket Ping，确保中间网络设备（WAF/CDN/Nginx）
	// 将连接视为活跃。仅靠 yamux keepalive 的二进制数据帧不够——
	// 某些中间设备只认 WebSocket Ping/Pong 控制帧。
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-ticker.C:
				if err := conn.Ping(pingCtx); err != nil {
					logger.Warn("[tunnel] ws ping failed: %v", err)
					return
				}
			}
		}
	}()

	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.EnableKeepAlive = true
	yamuxCfg.KeepAliveInterval = 15 * time.Second
	yamuxCfg.ConnectionWriteTimeout = 30 * time.Second
	yamuxCfg.MaxStreamWindowSize = 4 * 1024 * 1024

	session, err := yamux.Client(wsNetConn, yamuxCfg)
	if err != nil {
		return fmt.Errorf("yamux client init failed: %w", err)
	}
	defer session.Close()

	for {
		stream, err := session.Accept()
		if err != nil {
			return fmt.Errorf("yamux accept failed: %w", err)
		}
		go handleStream(stream, localPort)
	}
}

// backoff 计算通用指数退避延迟（1s → 2s → 4s → ... → 60s max）
// 使用饱和乘法避免 int64 溢出
func backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	// 循环加倍，一旦超过 maxDelay 立即饱和
	d := initialDelay
	for i := 0; i < attempt; i++ {
		d *= 2
		if d > maxDelay || d <= 0 {
			return applyJitter(maxDelay)
		}
	}
	return applyJitter(d)
}

// rateLimitBackoff 计算 429 限流退避延迟
// 优先使用服务器返回的 Retry-After 头，否则从 rateLimitBaseDelay 开始指数退避
func rateLimitBackoff(err error, attempt int) time.Duration {
	// 尝试从结构化错误中提取 Retry-After
	var gwErr *device.GatewayAssignError
	if !errors.As(err, &gwErr) {
		// 退回到通用指数退避
		return backoff(attempt)
	}

	// 优先使用 Retry-After 头
	if d, ok := gwErr.RetryAfterDuration(); ok {
		// 加 1 秒缓冲，防止精确到秒的竞态
		d += time.Second
		if d > rateLimitMaxDelay {
			return applyJitter(rateLimitMaxDelay)
		}
		return applyJitter(d)
	}

	// 无 Retry-After 头时使用保守的独立退避，循环加倍防溢出
	d := rateLimitBaseDelay
	for i := 0; i < attempt; i++ {
		d *= 2
		if d > rateLimitMaxDelay || d <= 0 {
			return applyJitter(rateLimitMaxDelay)
		}
	}
	return applyJitter(d)
}

// applyJitter 对退避延迟添加 ±25% 的随机抖动，防止惊群效应
func applyJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// ±25% 范围: [0.75d, 1.25d)
	jitter := time.Duration(rand.Int63n(int64(d / 2))) - time.Duration(int64(d / 4))
	return d + jitter
}

func redactToken(s string) string {
	idx := strings.Index(s, "token=")
	if idx < 0 {
		return s
	}
	end := strings.Index(s[idx:], "&")
	if end < 0 {
		return s[:idx+6] + "***"
	}
	return s[:idx+6] + "***" + s[idx+end:]
}

type wsNetConn struct {
	*websocket.Conn
	reader io.Reader
	mu     sync.Mutex
}

func (c *wsNetConn) Read(b []byte) (int, error) {
	for {
		if c.reader != nil {
			n, err := c.reader.Read(b)
			if err == io.EOF {
				c.reader = nil
				continue
			}
			return n, err
		}
		_, msg, err := c.Conn.Read(context.Background())
		if err != nil {
			return 0, err
		}
		c.reader = bytes.NewReader(msg)
	}
}

func (c *wsNetConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.Conn.Write(context.Background(), websocket.MessageBinary, b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *wsNetConn) SetDeadline(t time.Time) error {
	return nil
}

func (c *wsNetConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *wsNetConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func (c *wsNetConn) LocalAddr() net.Addr  { return &net.TCPAddr{} }
func (c *wsNetConn) RemoteAddr() net.Addr { return &net.TCPAddr{} }

func (c *wsNetConn) Close() error {
	return c.Conn.Close(websocket.StatusNormalClosure, "closing")
}
