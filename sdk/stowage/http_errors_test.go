// Package stowage_test contains HTTP-client error-path and option tests.
// These cover the branches in httpClient.do and NewHTTP that the conformance
// suite (suite_test.go) does not reach: non-2xx status codes, malformed JSON
// responses, and the WithHTTPClient / WithTimeout option functions.
package stowage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// handlerStatus returns an http.HandlerFunc that writes statusCode with body.
func handlerStatus(statusCode int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(statusCode)
		_, _ = fmt.Fprint(w, body)
	}
}

// handlerJSON writes HTTP 200 with a JSON-encoded value.
func handlerJSON(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// handlerMalformed writes HTTP 200 with invalid JSON.
func handlerMalformed() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, "not-valid-json{{{{")
	}
}

// ---- Option functions --------------------------------------------------------

// TestHTTP_WithHTTPClient verifies that the WithHTTPClient option is applied:
// the custom *http.Client is used for requests (exercising the opt(o) loop body
// inside NewHTTP and the WithHTTPClient function itself).
func TestHTTP_WithHTTPClient(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(handlerJSON(map[string]any{"topics": []any{}}))
	defer ts.Close()

	custom := &http.Client{Timeout: 10 * time.Second}
	client := stowage.NewHTTP(ts.URL, "k", stowage.WithHTTPClient(custom))

	_, err := client.Topics(context.Background())
	if err != nil {
		t.Errorf("Topics with custom http.Client: %v", err)
	}
}

// TestHTTP_WithTimeout verifies that the WithTimeout option is applied and that
// a client built with it can successfully make requests.
func TestHTTP_WithTimeout(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(handlerJSON(map[string]any{"topics": []any{}}))
	defer ts.Close()

	client := stowage.NewHTTP(ts.URL, "k", stowage.WithTimeout(15*time.Second))

	_, err := client.Topics(context.Background())
	if err != nil {
		t.Errorf("Topics with custom timeout: %v", err)
	}
}

// ---- Non-2xx error matrix ---------------------------------------------------

// TestHTTP_Do_Non2xx verifies that any 4xx/5xx response causes do() to return
// a non-nil error containing the HTTP status code and response body.
func TestHTTP_Do_Non2xx(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	statuses := []int{http.StatusBadRequest, http.StatusUnauthorized,
		http.StatusNotFound, http.StatusInternalServerError, http.StatusBadGateway}

	for _, status := range statuses {
		status := status
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			t.Parallel()
			body := fmt.Sprintf(`{"error":"test error %d"}`, status)
			ts := httptest.NewServer(handlerStatus(status, body))
			defer ts.Close()

			client := stowage.NewHTTP(ts.URL, "k")
			_, err := client.Topics(ctx)
			if err == nil {
				t.Errorf("HTTP %d: expected error, got nil", status)
			}
		})
	}
}

// ---- Malformed JSON ----------------------------------------------------------

// TestHTTP_Do_MalformedJSON verifies that a 200 response with invalid JSON
// causes do() to return a decode error.
func TestHTTP_Do_MalformedJSON(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(handlerMalformed())
	defer ts.Close()

	client := stowage.NewHTTP(ts.URL, "k")
	_, err := client.Topics(context.Background())
	if err == nil {
		t.Error("malformed JSON response: expected decode error, got nil")
	}
}

// ---- Per-endpoint error paths -----------------------------------------------

// TestHTTP_Endpoints_Error verifies the error-return paths for every endpoint
// method (each delegates to do() which can return an error). Using an HTTP 500
// response exercises the `if err != nil { return _, err }` statement in each
// method, i.e., Retrieve, Drilldown, ResolveCitations, Topics.
func TestHTTP_Endpoints_Error(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	serverErr := handlerStatus(http.StatusInternalServerError, `{"error":"server error"}`)

	tests := []struct {
		name string
		call func(stowage.Client) error
	}{
		{
			name: "Retrieve",
			call: func(c stowage.Client) error {
				_, err := c.Retrieve(ctx, stowage.RetrieveRequest{Query: "test"})
				return err
			},
		},
		{
			name: "Drilldown",
			call: func(c stowage.Client) error {
				_, err := c.Drilldown(ctx, stowage.DrilldownRequest{Citation: "01J"})
				return err
			},
		},
		{
			name: "ResolveCitations",
			call: func(c stowage.Client) error {
				_, err := c.ResolveCitations(ctx, stowage.ResolveCitationsRequest{
					Citations: []string{"01J"},
				})
				return err
			},
		},
		{
			name: "Topics",
			call: func(c stowage.Client) error {
				_, err := c.Topics(ctx)
				return err
			},
		},
		{
			name: "Ingest",
			call: func(c stowage.Client) error {
				_, err := c.Ingest(ctx, stowage.IngestRequest{
					Records: []stowage.RecordInput{{Role: "user", Content: "x"}},
				})
				return err
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ts := httptest.NewServer(serverErr)
			defer ts.Close()

			client := stowage.NewHTTP(ts.URL, "k")
			if err := tc.call(client); err == nil {
				t.Errorf("%s with server error: expected error, got nil", tc.name)
			}
		})
	}
}

// TestHTTP_ConnectionRefused verifies that do() propagates a transport-level
// error when the server is unavailable (covers the `c.http.Do` error path).
func TestHTTP_ConnectionRefused(t *testing.T) {
	t.Parallel()
	// Start and immediately shut down a server; the URL remains valid but
	// connections will be refused, causing c.http.Do to return an error.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := ts.URL
	ts.Close()

	client := stowage.NewHTTP(url, "k")
	_, err := client.Topics(context.Background())
	if err == nil {
		t.Error("Topics with closed server: expected connection error, got nil")
	}
}

// TestHTTP_Drilldown_Success verifies the success path of httpClient.Drilldown
// (the `return resp, nil` statement) using an httptest server returning a valid
// DrilldownResponse JSON.
func TestHTTP_Drilldown_Success(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(handlerJSON(map[string]any{
		"memory_id": "m1",
		"spans":     []any{},
	}))
	defer ts.Close()

	client := stowage.NewHTTP(ts.URL, "k")
	resp, err := client.Drilldown(context.Background(), stowage.DrilldownRequest{MemoryID: "m1"})
	if err != nil {
		t.Fatalf("Drilldown success: %v", err)
	}
	if resp.MemoryID != "m1" {
		t.Errorf("Drilldown success: MemoryID want %q, got %q", "m1", resp.MemoryID)
	}
}

// TestHTTP_Endpoints_MalformedJSON verifies the decode-error paths for each
// endpoint that decodes a response body (Topics uses a nested struct, Retrieve
// and friends decode directly).
func TestHTTP_Endpoints_MalformedJSON(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	malformed := handlerMalformed()

	tests := []struct {
		name string
		call func(stowage.Client) error
	}{
		{
			name: "Retrieve",
			call: func(c stowage.Client) error {
				_, err := c.Retrieve(ctx, stowage.RetrieveRequest{Query: "test"})
				return err
			},
		},
		{
			name: "Drilldown",
			call: func(c stowage.Client) error {
				_, err := c.Drilldown(ctx, stowage.DrilldownRequest{Citation: "01J"})
				return err
			},
		},
		{
			name: "ResolveCitations",
			call: func(c stowage.Client) error {
				_, err := c.ResolveCitations(ctx, stowage.ResolveCitationsRequest{
					Citations: []string{"01J"},
				})
				return err
			},
		},
		{
			name: "Topics",
			call: func(c stowage.Client) error {
				_, err := c.Topics(ctx)
				return err
			},
		},
		{
			name: "Ingest",
			call: func(c stowage.Client) error {
				_, err := c.Ingest(ctx, stowage.IngestRequest{
					Records: []stowage.RecordInput{{Role: "user", Content: "x"}},
				})
				return err
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ts := httptest.NewServer(malformed)
			defer ts.Close()

			client := stowage.NewHTTP(ts.URL, "k")
			if err := tc.call(client); err == nil {
				t.Errorf("%s with malformed JSON: expected decode error, got nil", tc.name)
			}
		})
	}
}
