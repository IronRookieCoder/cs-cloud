package terminal

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"sync"
	"time"

	"cs-cloud/internal/logger"
)

type Session struct {
	ID        string
	Pid       int
	Cols      uint16
	Rows      uint16
	Cwd       string
	CreatedAt time.Time
	LastInput time.Time
	cancel    context.CancelFunc

	subscribers map[string]chan []byte
	subMu       sync.RWMutex

	ptmx io.ReadWriteCloser

	recentBuf   [][]byte
	recentMu    sync.RWMutex
	recentMax   int
}

func (s *Session) Write(data []byte) error {
	s.LastInput = time.Now()
	_, err := s.ptmx.Write(data)
	return err
}

func (s *Session) Resize(rows, cols uint16) error {
	s.Rows = rows
	s.Cols = cols
	return s.resizePty(rows, cols)
}

func (s *Session) Close() {
	s.cancel()
	killProcessTree(s.Pid)
	_ = s.ptmx.Close()

	s.subMu.Lock()
	for id, ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, id)
	}
	s.subMu.Unlock()
}

func (s *Session) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 256)
	id := generateSubID()

	s.subMu.Lock()
	s.subscribers[id] = ch
	s.subMu.Unlock()

	s.recentMu.RLock()
	for _, data := range s.recentBuf {
		encoded := make([]byte, len(data))
		copy(encoded, data)
		select {
		case ch <- encoded:
		default:
		}
	}
	s.recentMu.RUnlock()

	unsub := func() {
		s.subMu.Lock()
		defer s.subMu.Unlock()
		if existing, ok := s.subscribers[id]; ok {
			close(existing)
			delete(s.subscribers, id)
		}
	}

	return ch, unsub
}

func (s *Session) SubscriberCount() int {
	s.subMu.RLock()
	defer s.subMu.RUnlock()
	return len(s.subscribers)
}

func (s *Session) readOutput(ctx context.Context) {
	buf := make([]byte, 4096)
	logger.Debug("terminal: readOutput started id=%s pid=%d", s.ID, s.Pid)
	for {
		select {
		case <-ctx.Done():
			logger.Debug("terminal: readOutput ctx done id=%s", s.ID)
			return
		default:
		}

		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			s.broadcast(data)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Debug("terminal: readOutput error id=%s err=%v", s.ID, err)
			s.broadcastExit()
			return
		}
	}
}

func (s *Session) broadcast(data []byte) {
	s.recentMu.Lock()
	s.recentBuf = append(s.recentBuf, data)
	if len(s.recentBuf) > s.recentMax {
		s.recentBuf = s.recentBuf[len(s.recentBuf)-s.recentMax:]
	}
	s.recentMu.Unlock()

	s.subMu.RLock()
	defer s.subMu.RUnlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- data:
		default:
		}
	}
}

func (s *Session) broadcastExit() {
	s.subMu.RLock()
	defer s.subMu.RUnlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- nil:
		default:
		}
	}
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("t_%x", b)
}

func generateSubID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
