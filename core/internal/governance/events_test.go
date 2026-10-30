package governance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sandx/internal/execution"
)

func queued(t *testing.T) (Queue, TeamEvent, []byte) {
	t.Helper()
	q := Queue{Home: t.TempDir(), Snapshot: snapshot(), Scope: "private-workspace"}
	secret := "TOP-SECRET-CREDENTIAL"
	e := execution.Event{Type: execution.EventCompleted, TaskID: secret, AttemptID: "attempt", Time: time.Now().UTC(), Summary: secret, Stage: secret, Result: &execution.Result{TaskID: secret, ExecutionState: execution.Finished}}
	if err := q.Emit(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(q.dir())
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	b, err := os.ReadFile(filepath.Join(q.dir(), files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(secret)) || bytes.Contains(b, []byte("private-workspace")) {
		t.Fatal("secret leaked", string(b))
	}
	var got TeamEvent
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if err = got.Validate(); err != nil {
		t.Fatal(err)
	}
	if got.Subject != Subject() || got.Outcome != "finished" {
		t.Fatal(got)
	}
	return q, got, b
}
func TestOutboxRedactionAndLostAckReplay(t *testing.T) {
	q, event, b := queued(t)
	if err := os.WriteFile(filepath.Join(q.Home, "team-events.token"), []byte("collector-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := []string{}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer collector-secret" || r.Header.Get("Idempotency-Key") != event.ID {
			t.Error("wrong authentication or identity")
		}
		var e TeamEvent
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Error(err)
		}
		requests = append(requests, e.ID)
		if calls.Add(1) == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				connection.Close()
			}
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	q.Snapshot.Policy.Events.Endpoint = server.URL
	auth := func() error { return nil }
	out, err := q.Flush(context.Background(), auth)
	if err == nil || out.Pending != 1 || out.Sent != 0 {
		t.Fatal(out, err)
	}
	retained, e := os.ReadFile(filepath.Join(q.dir(), event.ID+".json"))
	if e != nil || !bytes.Equal(b, retained) {
		t.Fatal("lost acknowledgement changed event", e)
	}
	out, err = q.Flush(context.Background(), auth)
	if err != nil || out.Pending != 0 || out.Sent != 1 {
		t.Fatal(out, err)
	}
	if len(requests) != 2 || requests[0] != requests[1] {
		t.Fatal("retry changed event ID", requests)
	}
}
func TestOutboxRejectsRedirectUnauthorizedCorruptAndRevoked(t *testing.T) {
	for _, kind := range []string{"redirect", "unauthorized", "corrupt", "foreign", "revoked", "credential-mode", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			q, event, _ := queued(t)
			var contacted atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted.Add(1); w.WriteHeader(204) }))
			defer destination.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "redirect" {
					http.Redirect(w, r, destination.URL, 307)
				} else {
					w.WriteHeader(401)
				}
			}))
			defer server.Close()
			q.Snapshot.Policy.Events.Endpoint = server.URL
			token := filepath.Join(q.Home, "team-events.token")
			os.WriteFile(token, []byte("secret"), 0600)
			auth := func() error { return nil }
			switch kind {
			case "corrupt":
				os.WriteFile(filepath.Join(q.dir(), event.ID+".json"), []byte("{}"), 0600)
			case "foreign":
				q.Snapshot.Policy.Organization = "different"
			case "revoked":
				auth = func() error { return errors.New("revoked") }
			case "credential-mode":
				os.Chmod(token, 0644)
			case "symlink":
				os.Remove(token)
				os.Symlink("missing", token)
			}
			out, err := q.Flush(context.Background(), auth)
			if err == nil || out.Pending != 1 || out.Sent != 0 || contacted.Load() != 0 {
				t.Fatal(out, err, contacted.Load())
			}
			if _, err = os.Stat(filepath.Join(q.dir(), event.ID+".json")); err != nil {
				t.Fatal("failed export removed event", err)
			}
		})
	}
}
func TestOutboxFailureAndPrivateState(t *testing.T) {
	q := Queue{Home: t.TempDir(), Snapshot: snapshot()}
	os.Symlink(t.TempDir(), q.dir())
	if err := q.Emit(context.Background(), execution.Event{Type: execution.EventAdmitted, Time: time.Now()}); err == nil {
		t.Fatal("symlink outbox accepted")
	}
	q.Home = t.TempDir()
	os.Mkdir(q.dir(), 0755)
	if err := q.Emit(context.Background(), execution.Event{Type: execution.EventAdmitted, Time: time.Now()}); err == nil {
		t.Fatal("public outbox accepted")
	}
	if err := endpoint("https://example.com/path?secret=x"); err == nil {
		t.Fatal("credential endpoint accepted")
	}
	if err := endpoint("http://example.com"); err == nil {
		t.Fatal("insecure endpoint accepted")
	}
	q, event, _ := queued(t)
	event.Outcome = strings.Repeat("secret", 10)
	if err := event.Validate(); err == nil {
		t.Fatal("free text event accepted")
	}
	_ = q
}
