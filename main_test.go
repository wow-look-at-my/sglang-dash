package main

import (
	"errors"
	"flag"
	"io/fs"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The binary is only worth shipping if the UI travelled with it, so the embed
// is checked here rather than discovered by an operator on a blank page.
func TestTheEmbeddedUIIsPresent(t *testing.T) {
	ui, err := fs.Sub(assets, "web/dist")
	require.NoError(t, err)
	for _, name := range []string{"index.html", "app.js", "app.css"} {
		info, err := fs.Stat(ui, name)
		require.NoError(t, err, "web/dist/%s is missing from the binary", name)
		assert.Positive(t, info.Size())
	}
}

func TestRunRefusesAnUnconfiguredStart(t *testing.T) {
	withArgs(t, "sglang-dash")
	err := run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-upstream")
}

func TestRunReportsAHelpRequestAsSuch(t *testing.T) {
	withArgs(t, "sglang-dash", "-h")
	assert.True(t, errors.Is(run(), flag.ErrHelp))
}

func TestRunServesTheDashboardInDemoMode(t *testing.T) {
	addr := freeAddress(t)
	withArgs(t, "sglang-dash", "-demo", "-listen", addr)

	done := make(chan error, 1)
	go func() { done <- run() }()

	client := &http.Client{Timeout: time.Second}
	base := "http://" + addr
	require.Eventually(t, func() bool {
		resp, err := client.Get(base + "/api/status")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 50*time.Millisecond, "the server never came up")

	page, err := client.Get(base + "/")
	require.NoError(t, err)
	defer page.Body.Close()
	assert.Equal(t, http.StatusOK, page.StatusCode, "the embedded UI must be served at the root")

	require.NoError(t, stopProcess())
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not shut down on a termination signal")
	}
}

// stopProcess delivers the signal run() installs its shutdown handler for.
// The handler is live while run() is, so the test process is not killed.
func stopProcess() error {
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return self.Signal(syscall.SIGTERM)
}

func withArgs(t *testing.T, args ...string) {
	t.Helper()
	original := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = original })
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())
	return addr
}
