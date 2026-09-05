package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestExplicitHTTPRejectsAmbiguousRequests(t *testing.T) {
	_, ts, st := newTestServer(t)
	_, key := mustCreateAgentKey(t, st, "explicit-errors")
	for _, tc := range []struct {
		name        string
		path        string
		body        string
		contentType string
		key         string
		status      int
	}{
		{"malformed", "/v1/remember", "{", "application/json", "", 400},
		{"trailing", "/v1/remember", "{} {}", "application/json", "", 400},
		{"unknown", "/v1/remember", `{"unknown":true}`, "application/json", "", 400},
		{"conflicting_key", "/v1/remember", `{"idempotency_key":"body"}`, "application/json", "header", 409},
		{"invalid_command", "/v1/remember", `{}`, "application/json", "header", 400},
		{"invalid_correction", "/v1/correct", `{}`, "application/json", "", 400},
		{"content_type", "/v1/remember", `{}`, "text/plain", "", 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", bearerHeader(key))
			req.Header.Set("Content-Type", tc.contentType)
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer drainClose(resp.Body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d; wanted %d", resp.StatusCode, tc.status)
			}
		})
	}
}
