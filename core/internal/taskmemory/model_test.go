package taskmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestHTTPModelsAreBoundedTextOnlyAndRejectTruncation(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			_, j := fixture(t)
			j.Provider = provider
			for _, incomplete := range []bool{false, true} {
				calls := 0
				client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Method != "POST" || r.URL.Scheme != "https" {
						t.Fatal(r.URL)
					}
					b, _ := io.ReadAll(r.Body)
					if bytes.Contains(b, []byte("private-api-value")) {
						t.Fatal("credential in body")
					}
					var req map[string]any
					if e := json.Unmarshal(b, &req); e != nil {
						t.Fatal(e)
					}
					if req["model"] != j.Model || req["previous_response_id"] != nil || req["conversation"] != nil {
						t.Fatal(req)
					}
					result := map[string]any{}
					if provider == "codex" {
						if r.Header.Get("Authorization") != "Bearer private-api-value" || req["store"] != false || len(req["tools"].([]any)) != 0 || req["max_output_tokens"] != float64(j.Limits.MaxOutputTokens) {
							t.Fatal(req)
						}
						result = map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": string(goodOutput(j))}}}}}
						if incomplete {
							result["status"] = "incomplete"
						}
					} else {
						if r.Header.Get("x-api-key") != "private-api-value" || r.Header.Get("anthropic-version") == "" || req["tools"] != nil || req["max_tokens"] != float64(j.Limits.MaxOutputTokens) {
							t.Fatal(req)
						}
						result = map[string]any{"stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": string(goodOutput(j))}}}
						if incomplete {
							result["stop_reason"] = "max_tokens"
						}
					}
					rb, _ := json.Marshal(result)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(rb)), Header: make(http.Header)}, nil
				})}
				b, e := (HTTPModel{APIKey: "private-api-value", Client: client}).Generate(context.Background(), j)
				if calls != 1 || incomplete != (e != nil) || !incomplete && !bytes.Equal(b, goodOutput(j)) {
					t.Fatal(calls, string(b), e)
				}
			}
		})
	}
}
func TestHTTPModelRejectsToolsCredentialsAndUnsafeErrors(t *testing.T) {
	_, j := fixture(t)
	for _, response := range []string{
		`{"status":"completed","output":[{"type":"function_call","name":"write"}]}`,
		`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"private-api-value"}]}]}`,
		strings.Repeat("x", 257<<10),
	} {
		m := HTTPModel{APIKey: "private-api-value", Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
		})}}
		if _, e := m.Generate(context.Background(), j); e == nil || strings.Contains(e.Error(), "private-api-value") {
			t.Fatal(e)
		}
	}
}
