package cli

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"cs-cloud/internal/localserver"
)

func TestInitializeStartedServerShutsDownOnFailure(t *testing.T) {
	srv := localserver.New()
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	startupErr := errors.New("reconciliation failed")
	err := initializeStartedServer(srv, func() error { return startupErr })
	if !errors.Is(err, startupErr) {
		t.Fatalf("initializeStartedServer error = %v", err)
	}

	conn, dialErr := net.DialTimeout("tcp", strings.TrimPrefix(srv.URL(), "http://"), 100*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("server still accepts connections after initialization failure")
	}
}
