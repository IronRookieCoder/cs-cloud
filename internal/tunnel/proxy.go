package tunnel

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"

	"cs-cloud/internal/logger"
)

const maxBodySize = 50 * 1024 * 1024 // 50MB

func handleStream(stream net.Conn, localPort int) {
	defer stream.Close()

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
		logger.Warn("[tunnel] request body too large: %d bytes, path=%s", contentLength, path)
		writeHTTPError(stream, 413, "Request Entity Too Large")
		return
	}

	isWS := strings.ToLower(headers["upgrade"]) == "websocket"
	if isWS {
		var body []byte
		if contentLength > 0 {
			body = make([]byte, contentLength)
			if _, err := io.ReadFull(br, body); err != nil {
				logger.Warn("[tunnel] ws body read error: %v", err)
				return
			}
		}
		proxyWebSocket(stream, method, path, headers, body, localPort)
	} else {
		proxyHTTPStream(stream, br, method, path, headers, contentLength, localPort)
	}
}

func proxyHTTPStream(stream net.Conn, bodyReader io.Reader, method, path string, headers map[string]string, contentLength int, localPort int) {
	localConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		logger.Warn("[tunnel] local dial failed: %v", err)
		writeHTTPError(stream, 502, "Bad Gateway")
		return
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
		return
	}

	// Stream body directly without full buffering
	if contentLength > 0 {
		if _, err := io.CopyN(localConn, bodyReader, int64(contentLength)); err != nil {
			logger.Warn("[tunnel] body stream error: %v", err)
			return
		}
	}

	// Bidirectional copy for response
	go func() {
		io.Copy(stream, localConn)
		stream.Close()
	}()
	io.Copy(localConn, bodyReader)
}

func proxyWebSocket(stream net.Conn, method, path string, headers map[string]string, body []byte, localPort int) {
	localConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		logger.Warn("[tunnel] ws local dial failed: %v", err)
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
