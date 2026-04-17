package cmd

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostPort returns the host and port of an httptest server URL for daemon
// liveness probing.
func hostPort(t *testing.T, u string) (string, int) {
	t.Helper()
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	host, portStr, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, port
}

func TestDaemonRunning_returnsTrueWhenServerResponds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mimic the daemon's real behavior: GET /callback/doctor-ping gets
		// 405 because the callback handler only accepts POST.
		if r.URL.Path == "/callback/doctor-ping" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	host, port := hostPort(t, srv.URL)
	assert.True(t, daemonRunning(host, port))
}

func TestDaemonRunning_falseWhenServerClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	host, port := hostPort(t, srv.URL)
	srv.Close()

	// Shorten the timeout so the test isn't slow.
	prev := daemonRunningTimeout
	daemonRunningTimeout = 200 * time.Millisecond
	t.Cleanup(func() { daemonRunningTimeout = prev })

	assert.False(t, daemonRunning(host, port))
}

func TestDaemonRunning_falseOnTimeout(t *testing.T) {
	// Server that accepts connections but never responds. Block in the
	// handler with a long sleep; daemonRunning should bail via its client
	// timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	t.Cleanup(srv.Close)

	prev := daemonRunningTimeout
	daemonRunningTimeout = 150 * time.Millisecond
	t.Cleanup(func() { daemonRunningTimeout = prev })

	host, port := hostPort(t, srv.URL)
	start := time.Now()
	got := daemonRunning(host, port)
	elapsed := time.Since(start)

	assert.False(t, got, "timeout should surface as false, not panic")
	assert.Less(t, elapsed, 1*time.Second, "should respect the client timeout")
}

func TestDaemonRunning_normalizesZeroHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	t.Cleanup(srv.Close)

	_, port := hostPort(t, srv.URL)
	// Calling with 0.0.0.0 should dial loopback, not fail with "can't connect
	// to 0.0.0.0". The only way this succeeds is if daemonRunning rewrites
	// the host.
	assert.True(t, daemonRunning("0.0.0.0", port))
}

func TestOpenListener_succeedsOnFreePort(t *testing.T) {
	ln, err := openListener("127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	assert.NotNil(t, ln.Addr())
}

func TestOpenListener_failsWhenPortTaken(t *testing.T) {
	// Grab a port.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer held.Close()

	addr := held.Addr().String()
	_, err = openListener(addr)
	require.Error(t, err, "binding an already-held port must error")
}
