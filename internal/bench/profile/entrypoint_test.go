//go:build profile

// Package profile_test — entrypoint lifecycle matrix for the P1 profiling rig.
//
// This file adds the two external deployment shapes — `stowage serve` (HTTP) and
// `stowage mcp` (stdio) — to the rig by spawning the real binary as a subprocess
// and checking goroutine-leak and clean-shutdown behaviour. It dogfoods the pprof
// listener built earlier in phase P1.
//
// Goroutine introspection is available only for the serve entrypoint (via the
// pprof listener). The MCP stdio entrypoint has no pprof surface; its test is a
// drain/hang check only.
package profile_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Package-level entrypoint results
// (populated by TestProfileEntrypointServe / TestProfileEntrypointMCP;
//  consumed by TestProfileWriteBaseline in profile_test.go)
// ---------------------------------------------------------------------------

// entrypointResult holds lifecycle measurements for one entrypoint shape.
type entrypointResult struct {
	ran            bool
	g0             int           // goroutines before load (serve only; -1 = unavailable)
	gFinal         int           // goroutines after last load cycle (serve only; -1 = unavailable)
	climbDelta     int           // gFinal - g0 (serve only)
	stabilityOK    bool          // climbDelta <= *flEps (serve only)
	heapAllocBytes float64       // bytes from /metrics (serve only; 0 = unavailable)
	shutdownOK     bool          // process exited cleanly within timeout
	shutdownDur    time.Duration // elapsed from signal/stdin-close to exit
}

var (
	entrypointMu      sync.Mutex
	entrypointResults = map[string]entrypointResult{}
)

// ---------------------------------------------------------------------------
// Subprocess helpers
// ---------------------------------------------------------------------------

// buildStowageBinary compiles ./cmd/stowage from the repo root into a temp
// directory and returns the path to the resulting binary.
func buildStowageBinary(t *testing.T) string {
	t.Helper()
	repoRoot := findRepoRoot(t)
	binPath := filepath.Join(t.TempDir(), "stowage")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/stowage")
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("buildStowageBinary: go build failed: %v\nstderr:\n%s", err, stderr.String())
	}
	return binPath
}

// freePort returns an ephemeral TCP port that is not currently in use on
// 127.0.0.1. The listener is closed immediately so the port can be reused.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// httpGet sends a GET request to url, optionally attaching a Bearer token, and
// returns the HTTP status code and response body.  It uses a fresh *http.Client
// with a 5s timeout so keep-alive connections from prior requests do not
// influence the server's goroutine count.
func httpGet(t *testing.T, url, bearer string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("httpGet: new request for %s: %v", url, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// goroutineTotal fetches the pprof goroutine text profile from the server at
// pprofAddr and returns the total goroutine count.  The response first line is
// "goroutine profile: total N"; this function parses N.  Returns -1 on any
// error so callers can distinguish "unavailable" from "zero".
func goroutineTotal(pprofAddr, bearer string) int {
	url := fmt.Sprintf("http://%s/debug/pprof/goroutine?debug=1", pprofAddr)
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return -1
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return -1
	}
	sc := bufio.NewScanner(resp.Body)
	if sc.Scan() {
		// Line format: "goroutine profile: total 42"
		parts := strings.Fields(sc.Text())
		if len(parts) > 0 {
			if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
				return n
			}
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// TestProfileEntrypointServe
// ---------------------------------------------------------------------------

// TestProfileEntrypointServe boots the real `stowage serve` binary, drives
// three HTTP load cycles, checks goroutine stability via the pprof listener,
// and verifies a clean SIGTERM shutdown.  All stability gates are advisory
// unless -profile.strict is set.
//
// The test is named so that it sorts before TestProfileMatrix and before
// TestProfileWriteBaseline when the test binary runs with -run TestProfile.
func TestProfileEntrypointServe(t *testing.T) {
	bin := buildStowageBinary(t)

	mainPort := freePort(t)
	pprofPort := freePort(t)
	metricsPort := freePort(t)
	// Defensive: retry until all three ports are distinct (collision is extremely
	// unlikely, but a fixed metrics port would risk a bind collision under CI/parallel
	// runs — and serve would fail to start, not just skip metrics).
	for pprofPort == mainPort || metricsPort == mainPort || metricsPort == pprofPort {
		pprofPort = freePort(t)
		metricsPort = freePort(t)
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "serve.db")
	cfgPath := filepath.Join(tmpDir, "stowage.yaml")
	cfgContent := fmt.Sprintf(`server:
  listen: "127.0.0.1:%d"
  pprof_listen: "127.0.0.1:%d"
store:
  driver: sqlite
  dsn: %q
gateway:
  driver: mock
telemetry:
  metrics_listen: "127.0.0.1:%d"
`, mainPort, pprofPort, dbPath, metricsPort)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stderrBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "serve", "--config", cfgPath)
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stowage serve: %v", err)
	}
	// Safety net: Kill is idempotent if the process already exited.
	defer func() { _ = cmd.Process.Kill() }()

	mainAddr := fmt.Sprintf("127.0.0.1:%d", mainPort)
	pprofAddr := fmt.Sprintf("127.0.0.1:%d", pprofPort)
	healthURL := fmt.Sprintf("http://%s/healthz", mainAddr)

	// Poll /healthz until 200 — up to 15 s (60 × 250 ms).
	ready := false
	for i := 0; i < 60; i++ {
		time.Sleep(250 * time.Millisecond)
		if code, _ := httpGet(t, healthURL, ""); code == http.StatusOK {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatalf("stowage serve never reached /healthz 200 within 15s\nstderr:\n%s",
			stderrBuf.String())
	}

	// Bootstrap an admin key (keyring is empty so first POST requires no auth).
	keysURL := fmt.Sprintf("http://%s/v1/admin/keys", mainAddr)
	keysClient := &http.Client{Timeout: 10 * time.Second}
	keysBodyStr := `{"tenant_id":"profile-serve","role":"admin"}`
	keysReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, keysURL,
		strings.NewReader(keysBodyStr))
	if err != nil {
		t.Fatalf("bootstrap keys: new request: %v", err)
	}
	keysReq.Header.Set("Content-Type", "application/json")
	keysResp, err := keysClient.Do(keysReq)
	if err != nil {
		t.Fatalf("bootstrap keys: do: %v\nstderr:\n%s", err, stderrBuf.String())
	}
	defer keysResp.Body.Close()
	if keysResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(keysResp.Body)
		t.Fatalf("bootstrap keys: got %d, want 201\nbody: %s\nstderr:\n%s",
			keysResp.StatusCode, body, stderrBuf.String())
	}
	var keyPayload struct {
		Plaintext string `json:"plaintext"`
	}
	if decErr := json.NewDecoder(keysResp.Body).Decode(&keyPayload); decErr != nil {
		t.Fatalf("bootstrap keys: decode response: %v", decErr)
	}
	if keyPayload.Plaintext == "" {
		t.Fatalf("bootstrap keys: empty plaintext in response\nstderr:\n%s", stderrBuf.String())
	}
	adminKey := keyPayload.Plaintext

	// Poll pprof endpoint until it answers 200 — up to 5 s.
	pprofIndexURL := fmt.Sprintf("http://%s/debug/pprof/", pprofAddr)
	for i := 0; i < 20; i++ {
		time.Sleep(250 * time.Millisecond)
		if code, _ := httpGet(t, pprofIndexURL, adminKey); code == http.StatusOK {
			break
		}
	}

	// Settle then baseline goroutine sample.
	time.Sleep(*flSettle)
	g0 := goroutineTotal(pprofAddr, adminKey)
	t.Logf("serve: g0 (baseline goroutines after settle) = %d", g0)

	// doPost is a fire-and-forget POST using the caller's client.
	// Responses are discarded; only errors are silently swallowed — we measure
	// resource behaviour, not correctness.
	doPost := func(client *http.Client, url, bearer, body string) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			url, strings.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	recordsURL := fmt.Sprintf("http://%s/v1/records", mainAddr)
	retrieveURL := fmt.Sprintf("http://%s/v1/retrieve", mainAddr)

	// Three load cycles: 8 ingest + 8 retrieve goroutines × 50 iterations each.
	// One *http.Client per cycle; CloseIdleConnections() after each cycle prevents
	// HTTP keep-alive connections from inflating the server's goroutine count across
	// the cycle boundary.
	var gCycles [3]int
	for cycle := 0; cycle < 3; cycle++ {
		cycleClient := &http.Client{Timeout: 10 * time.Second}
		var wg sync.WaitGroup

		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(gIdx int) {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					body := fmt.Sprintf(
						`{"records":[{"content":"entrypoint serve load cycle %d g%d i%d","session_id":"s%d","role":"user"}]}`,
						cycle, gIdx, i, gIdx)
					doPost(cycleClient, recordsURL, adminKey, body)
				}
			}(g)
		}

		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(_ int) {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					doPost(cycleClient, retrieveURL, adminKey,
						`{"query":"entrypoint load","limit":10}`)
				}
			}(g)
		}

		wg.Wait()
		cycleClient.CloseIdleConnections()
		time.Sleep(*flSettle)

		gCycles[cycle] = goroutineTotal(pprofAddr, adminKey)
		t.Logf("serve: cycle %d goroutine total = %d", cycle, gCycles[cycle])
	}

	gFinal := gCycles[2]
	climbDelta := gFinal - g0
	stabilityOK := climbDelta <= *flEps
	t.Logf("serve: gFinal=%d g0=%d climbDelta=%d eps=%d stabilityOK=%v",
		gFinal, g0, climbDelta, *flEps, stabilityOK)

	if !stabilityOK {
		gate(t,
			"serve entrypoint goroutine climb: final(%d) exceeds baseline(%d)+eps(%d) across load cycles — possible per-request leak",
			gFinal, g0, *flEps)
	}

	// Heap footprint from /metrics (no auth required).
	var heapAllocBytes float64
	metricsURL := fmt.Sprintf("http://%s/metrics", mainAddr)
	_, metricsBody := httpGet(t, metricsURL, "")
	for _, line := range strings.Split(metricsBody, "\n") {
		// Match "go_memstats_heap_alloc_bytes <value>" exactly (trailing space
		// distinguishes it from other metrics with a common prefix).
		if strings.HasPrefix(line, "go_memstats_heap_alloc_bytes ") {
			parts := strings.Fields(line)
			if len(parts) == 2 {
				if v, parseErr := strconv.ParseFloat(parts[1], 64); parseErr == nil {
					heapAllocBytes = v
				}
			}
			break
		}
	}
	t.Logf("serve: heap_alloc = %.1f MiB", heapAllocBytes/(1024*1024))

	// Clean shutdown: send SIGTERM then wait up to 20 s.
	shutdownStart := time.Now()
	if sigErr := cmd.Process.Signal(syscall.SIGTERM); sigErr != nil {
		t.Logf("serve: SIGTERM: %v", sigErr)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var shutdownOK bool
	var shutdownDur time.Duration
	select {
	case <-done:
		shutdownDur = time.Since(shutdownStart)
		shutdownOK = true
		t.Logf("serve: clean exit in %s", shutdownDur.Round(time.Millisecond))
	case <-time.After(20 * time.Second):
		shutdownDur = time.Since(shutdownStart)
		gate(t, "serve did not exit within 20s of SIGTERM — possible drain hang/deadlock")
		_ = cmd.Process.Kill()
		shutdownOK = false
	}

	entrypointMu.Lock()
	entrypointResults["serve"] = entrypointResult{
		ran:            true,
		g0:             g0,
		gFinal:         gFinal,
		climbDelta:     climbDelta,
		stabilityOK:    stabilityOK,
		heapAllocBytes: heapAllocBytes,
		shutdownOK:     shutdownOK,
		shutdownDur:    shutdownDur,
	}
	entrypointMu.Unlock()
}

// ---------------------------------------------------------------------------
// TestProfileEntrypointMCP
// ---------------------------------------------------------------------------

// TestProfileEntrypointMCP boots the real `stowage mcp` binary in stdio mode,
// sends the JSON-RPC initialization handshake, closes stdin (triggering EOF),
// and verifies that the process drains and exits cleanly within 20 s.
//
// Note: the MCP stdio entrypoint has no pprof surface, so goroutine
// introspection is unavailable.  This test is a drain/hang check only.
func TestProfileEntrypointMCP(t *testing.T) {
	bin := buildStowageBinary(t)

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "mcp.db")
	cfgPath := filepath.Join(tmpDir, "stowage.yaml")
	cfgContent := fmt.Sprintf(`store:
  driver: sqlite
  dsn: %q
gateway:
  driver: mock
mcp:
  stdio_tenant: "profile-mcp"
`, dbPath)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stderrBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "mcp", "--config", cfgPath)
	cmd.Stderr = &stderrBuf

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stowage mcp: %v", err)
	}
	// Safety net.
	defer func() { _ = cmd.Process.Kill() }()

	// Write the JSON-RPC handshake to stdin, one message per line.
	// The messages are buffered in the OS pipe; the server reads them after boot.
	handshake := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"profile","version":"0.0.1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}
	for _, msg := range handshake {
		_, _ = fmt.Fprintln(stdin, msg)
	}

	// Give the server time to boot and process the handshake.
	time.Sleep(1 * time.Second)

	// Close stdin — ServeStdio sees EOF and initiates a clean drain+exit.
	shutdownStart := time.Now()
	_ = stdin.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var shutdownOK bool
	var shutdownDur time.Duration
	select {
	case <-done:
		shutdownDur = time.Since(shutdownStart)
		shutdownOK = true
		t.Logf("mcp: clean exit in %s after stdin close", shutdownDur.Round(time.Millisecond))
	case <-time.After(20 * time.Second):
		shutdownDur = time.Since(shutdownStart)
		gate(t, "mcp stdio did not exit within 20s of stdin close — possible drain hang/deadlock\nstderr:\n%s",
			stderrBuf.String())
		_ = cmd.Process.Kill()
		shutdownOK = false
	}

	entrypointMu.Lock()
	entrypointResults["mcp"] = entrypointResult{
		ran:         true,
		shutdownOK:  shutdownOK,
		shutdownDur: shutdownDur,
	}
	entrypointMu.Unlock()
}
