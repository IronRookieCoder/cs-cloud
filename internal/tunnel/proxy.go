package tunnel

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"cs-cloud/internal/logger"
)

const maxBodySize = 50 * 1024 * 1024 // 50MB

func handleStream(stream net.Conn, localPort int) {
	defer stream.Close()
	start := time.Now()

	br := bufio.NewReaderSize(stream, 64*1024)

	// Read request line
	requestLine, err := br.ReadString('\n')
	if err != nil {
		return
	}
	requestLine = strings.TrimRight(requestLine, "\r\n")

	parts := strings.SplitN(requestLine, " ", 3)
	if len(parts) < 2 {
		writeHTTPError(stream, 400, "Bad Request")
		logger.Warn("[tunnel-req] bad request line: %q duration=%s", requestLine, time.Since(start))
		return
	}
	method := parts[0]
	path := parts[1]

	// Read headers
	headers := make(map[string]string)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		colonIdx := strings.Index(line, ": ")
		if colonIdx < 0 {
			continue
		}
		headers[strings.ToLower(line[:colonIdx])] = line[colonIdx+2:]
	}

	contentLength := -1
	if cl, ok := headers["content-length"]; ok {
		fmt.Sscanf(cl, "%d", &contentLength)
	}

	if contentLength > maxBodySize {
		logger.Warn("[tunnel-req] %s %s body too large: %d bytes", method, path, contentLength)
		writeHTTPError(stream, 413, "Request Entity Too Large")
		logger.Info("[tunnel-req] %s %s status=413 duration=%s", method, path, time.Since(start))
		return
	}

	isWS := strings.ToLower(headers["upgrade"]) == "websocket"
	if isWS {
		var body []byte
		if contentLength > 0 {
			body = make([]byte, contentLength)
			if _, err := io.ReadFull(br, body); err != nil {
				logger.Warn("[tunnel-req] %s %s ws body read error: %v", method, path, err)
				return
			}
		}
		logger.Info("[tunnel-req] %s %s ws-start", method, path)
		proxyWebSocket(stream, method, path, headers, body, localPort)
		logger.Info("[tunnel-req] %s %s ws-end duration=%s", method, path, time.Since(start))
	} else {
		status, outBytes := proxyHTTPStream(stream, br, method, path, headers, contentLength, localPort)
		// Successful forwards are the common case; log only failures so the
		// access-log noise doesn't drown out business events in app.log.
		if status >= 400 {
			logger.Warn("[tunnel-req] %s %s status=%d out=%dB duration=%s",
				method, path, status, outBytes, time.Since(start))
		}
	}
}

func proxyHTTPStream(stream net.Conn, bodyReader io.Reader, method, path string, headers map[string]string, contentLength int, localPort int) (status int, outBytes int64) {
	localConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		logger.Warn("[tunnel-req] %s %s local dial failed: %v", method, path, err)
		writeHTTPError(stream, 502, "Bad Gateway")
		return 502, 0
	}
	defer localConn.Close()

	// Build request headers
	var req strings.Builder
	req.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", method, path))
	req.WriteString(fmt.Sprintf("Host: 127.0.0.1:%d\r\n", localPort))
	for k, v := range headers {
		switch k {
		case "host", "connection", "transfer-encoding", "content-length":
			continue
		}
		req.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	if contentLength > 0 {
		req.WriteString(fmt.Sprintf("content-length: %d\r\n", contentLength))
	}
	req.WriteString("\r\n")

	if _, err := localConn.Write([]byte(req.String())); err != nil {
		return 0, 0
	}

	// Stream body directly without full buffering
	if contentLength > 0 {
		if _, err := io.CopyN(localConn, bodyReader, int64(contentLength)); err != nil {
			logger.Warn("[tunnel-req] %s %s body stream error: %v", method, path, err)
			return 0, 0
		}
	}

	// Capture status line + bytes from the backend response so we can log it.
	meta := &responseMeta{}

	// Bidirectional copy for response
	go func() {
		io.Copy(stream, io.TeeReader(localConn, meta))
		stream.Close()
	}()
	io.Copy(localConn, bodyReader)

	return meta.status, meta.bytesRead
}

// responseMeta inspects the backend's HTTP response as it streams back toward
// the tunnel. It captures the status code from the first response line and
// counts the bytes forwarded, so handleStream can log status/duration/size.
type responseMeta struct {
	status    int
	bytesRead int64
	parsed    bool
	buf       []byte
}

func (m *responseMeta) Write(p []byte) (int, error) {
	m.bytesRead += int64(len(p))
	if !m.parsed {
		m.buf = append(m.buf, p...)
		if idx := bytes.Index(m.buf, []byte("\r\n")); idx >= 0 {
			line := string(m.buf[:idx])
			// "HTTP/1.1 <code> <reason>"
			if lineParts := strings.SplitN(line, " ", 3); len(lineParts) >= 2 {
				fmt.Sscanf(lineParts[1], "%d", &m.status)
			}
			m.parsed = true
			m.buf = nil
		} else if len(m.buf) > 1024 {
			// Defensive cap: status line should never be this long.
			m.parsed = true
			m.buf = nil
		}
	}
	return len(p), nil
}

func proxyWebSocket(stream net.Conn, method, path string, headers map[string]string, body []byte, localPort int) {
	localConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		logger.Warn("[tunnel-req] %s %s ws local dial failed: %v", method, path, err)
		return
	}
	defer localConn.Close()

	var req strings.Builder
	req.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", method, path))
	req.WriteString(fmt.Sprintf("Host: 127.0.0.1:%d\r\n", localPort))
	for k, v := range headers {
		if k == "host" {
			continue
		}
		req.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	req.WriteString("\r\n")

	localConn.Write([]byte(req.String()))
	if len(body) > 0 {
		localConn.Write(body)
	}

	go func() {
		io.Copy(stream, localConn)
		stream.Close()
	}()
	io.Copy(localConn, stream)
}

func writeHTTPError(conn net.Conn, code int, message string) {
	body := fmt.Sprintf(`{"error":true,"message":"%s"}`, message)
	resp := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", code, message, len(body), body)
	conn.Write([]byte(resp))
}
