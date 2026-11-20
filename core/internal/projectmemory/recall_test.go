package projectmemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func recallFixture(t *testing.T) (RecallService, Store) {
	s := queryFixture(t)
	return RecallService{Queries: s.Queries(), Ledger: FileRecallLedger{Path: filepath.Join(t.TempDir(), "recalls.json")}, Project: "demo", Attempt: "attempt1", Selection: Selection{Facts: map[string]string{"node": "24"}}}, s
}
func TestRecallFrozenRetryRevocationAndBudget(t *testing.T) {
	r, s := recallFixture(t)
	ctx := context.Background()
	first, err := r.Recall(ctx, RecallRequest{"one", "E401"})
	if err != nil || first.Status != "ok" {
		t.Fatal(first, err)
	}
	r.Attempt = "attempt2"
	again, err := r.Recall(ctx, RecallRequest{"one", "E401"})
	if err != nil || again != first {
		t.Fatal(again, err)
	}
	if _, err = r.Recall(ctx, RecallRequest{"one", "Other"}); err == nil {
		t.Fatal("key collision accepted")
	}
	// Appending different approved knowledge must not change a retry's material.
	n, err := s.Add("demo", Input{Kind: "testing", Title: "E401 newer", Content: "new", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review("demo", n.ID, n.Digest, "approved", "test", "reviewed"); err != nil {
		t.Fatal(err)
	}
	again, err = r.Recall(ctx, RecallRequest{"one", "E401"})
	if err != nil || again != first {
		t.Fatal(again, err)
	}
	if _, err = s.Review("demo", "c", func() string {
		p, _ := s.Load("demo")
		for _, e := range p.Latest() {
			if e.ID == "c" {
				return e.Digest
			}
		}
		return ""
	}(), "archived", "test", "revoke"); err != nil {
		t.Fatal(err)
	}
	again, err = r.Recall(ctx, RecallRequest{"one", "E401"})
	if err != nil || again.Status != "revoked" || again.Context != "" {
		t.Fatal(again, err)
	}
	if err = r.ValidateHistory(ctx); err == nil {
		t.Fatal("revoked history resumed")
	}
}
func TestRecallConcurrentEmptyCallsBoundedAcrossInstances(t *testing.T) {
	r, _ := recallFixture(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	counts := map[string]int{}
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			copy := r
			copy.Attempt = fmt.Sprint("attempt", i)
			out, err := copy.Recall(context.Background(), RecallRequest{fmt.Sprint("key", i), "nomatch"})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Error(err)
			}
			counts[out.Status]++
		}(i)
	}
	wg.Wait()
	if counts["budget_exhausted"] == 0 || counts["empty"] == 0 {
		t.Fatal(counts)
	}
	if err := r.Ledger.Update(context.Background(), func(h *RecallHistory) error {
		if len(h.Records) > RecallMaxCalls {
			t.Fatal(len(h.Records))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type failingLedger struct {
	RecallLedger
	n int
}

func (l *failingLedger) Update(ctx context.Context, fn func(*RecallHistory) error) error {
	l.n++
	if l.n == 2 {
		return errors.New("crash before prepared commit")
	}
	return l.RecallLedger.Update(ctx, fn)
}
func TestRecallCrashReservationNeverRedeliversNewMaterial(t *testing.T) {
	r, _ := recallFixture(t)
	base := r.Ledger
	r.Ledger = &failingLedger{RecallLedger: base}
	if out, err := r.Recall(context.Background(), RecallRequest{"crash", "E401"}); err == nil || out.Context != "" {
		t.Fatal(out, err)
	}
	r.Ledger = base
	out, err := r.Recall(context.Background(), RecallRequest{"crash", "E401"})
	if err != nil || out.Status != "interrupted" || out.Context != "" {
		t.Fatal(out, err)
	}
	if err = base.Update(context.Background(), func(h *RecallHistory) error {
		if len(h.Records) != 1 || h.Records[0].Bytes != RecallCallBytes {
			t.Fatal(h)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestRecallAuthorityAndFactsRecheckedBeforeDelivery(t *testing.T) {
	r, _ := recallFixture(t)
	calls := 0
	r.Authorize = func(context.Context) error {
		calls++
		if calls > 1 {
			return errors.New("revoked")
		}
		return nil
	}
	out, err := r.Recall(context.Background(), RecallRequest{"revoked", "E401"})
	if err != nil || out.Context != "" || out.Status != "revoked" {
		t.Fatal(out, err)
	}
}
func TestRecallPreparedCommitAndLedgerTamper(t *testing.T) {
	r, _ := recallFixture(t)
	out, err := r.Recall(context.Background(), RecallRequest{"ready", "E401"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Ledger.Update(context.Background(), func(h *RecallHistory) error {
		if len(h.Records) != 1 || h.Records[0].State != "prepared_delivery_unknown" || h.Records[0].Response != out || h.Records[0].Snapshot == nil {
			t.Fatal(h)
		}
		h.Records[0].Query = "tamper"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Recall(context.Background(), RecallRequest{"ready", "E401"}); err == nil {
		t.Fatal("tampered ledger accepted")
	}
}

func TestRecallByteTimeBudgetsAndTimeout(t *testing.T) {
	for _, kind := range []string{"bytes", "time"} {
		t.Run(kind, func(t *testing.T) {
			r, _ := recallFixture(t)
			err := r.Ledger.Update(context.Background(), func(h *RecallHistory) error {
				record := RecallRecord{Key: "previous", Attempt: "attempt0", Query: "old", State: "reserved", Bytes: RecallMaxBytes, Millis: 0, Response: RecallResponse{Status: "interrupted"}}
				if kind == "time" {
					record.Bytes = 0
					record.Millis = RecallTotalMillis
				}
				record.Digest = recallDigest(record)
				h.Records = append(h.Records, record)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := r.Recall(context.Background(), RecallRequest{"new", "E401"})
			if err != nil || out.Status != "budget_exhausted" {
				t.Fatal(out, err)
			}
		})
	}
	r, _ := recallFixture(t)
	original := r.Queries.retriever
	r.Queries.retriever = retrieverFunc(func(ctx context.Context, q string, c []RetrievalCandidate) (RetrievalResult, error) {
		<-ctx.Done()
		return original.Retrieve(ctx, q, c)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out, err := r.Recall(ctx, RecallRequest{"timeout", "E401"})
	if err != nil || out.Status != "timeout" || out.Context != "" {
		t.Fatal(out, err)
	}
	// The actual MCP envelope must fit the charged representation even with JSON escaping.
	result := RecallResponse{Status: "ok", Context: strings.Repeat("\\\"<>&", 200), Digest: "sha256:fixture"}
	data, _ := json.Marshal(result)
	envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": strings.Repeat("x", 120), "result": map[string]any{"isError": false, "structuredContent": result, "content": []any{map[string]string{"type": "text", "text": string(data)}}}})
	if len(envelope) > recallBytes(result) {
		t.Fatalf("wire %d exceeds charge %d", len(envelope), recallBytes(result))
	}
}
