package usage_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
	"github.com/circlexo/circlexo-go/usage"
)

// flaky fails the first n calls with a 503.
type flaky struct {
	next  usage.Sender
	fails atomic.Int32
	calls atomic.Int32
}

func (f *flaky) ReportUsage(ctx context.Context, r circlexo.UsageReport) (circlexo.UsageResult, error) {
	f.calls.Add(1)
	if f.fails.Add(-1) >= 0 {
		return circlexo.UsageResult{}, &circlexo.APIError{Status: 503}
	}
	return f.next.ReportUsage(ctx, r)
}

func TestReporter(t *testing.T) {
	h := circlexotest.New(t, "demo")
	f := &flaky{next: circlexo.NewClient(h.Config(), nil)}
	f.fails.Store(2)
	r := usage.NewReporter(f, 10, 2)
	r.Backoff = time.Millisecond
	var mu sync.Mutex
	var results []circlexo.UsageResult
	r.OnResult = func(_ circlexo.UsageReport, res circlexo.UsageResult, err error) {
		if err != nil {
			t.Errorf("report: %v", err)
		}
		mu.Lock()
		results = append(results, res)
		mu.Unlock()
	}
	for i, k := range []string{"a", "b", "a"} {
		if err := r.Report("org-1", "api_calls", int64(i+1), k); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Report("org-1", "api_calls", 1, ""); err == nil {
		t.Fatal("report without key accepted")
	}
	if err := r.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Report("org-1", "api_calls", 1, "c"); err != usage.ErrClosed {
		t.Fatalf("after close: %v", err)
	}
	h.Lock()
	n := len(h.Usage)
	h.Unlock()
	if len(results) != 3 || n != 2 || f.calls.Load() != 5 {
		t.Fatalf("results %d, keys %d, calls %d", len(results), n, f.calls.Load())
	}

	// A 402 (out of balance) is not retried.
	f2 := &flaky{next: circlexo.NewClient(h.Config(), nil)}
	r2 := usage.NewReporter(senderFunc(func(context.Context, circlexo.UsageReport) (circlexo.UsageResult, error) {
		f2.calls.Add(1)
		return circlexo.UsageResult{}, &circlexo.APIError{Status: 402, Code: "insufficient_balance"}
	}), 1, 1)
	if _, err := r2.Send(context.Background(), circlexo.UsageReport{OrgID: "o", Feature: "f", Qty: 1, IdempotencyKey: usage.Key()}); !circlexo.IsStatus(err, 402) || f2.calls.Load() != 1 {
		t.Fatalf("402: %v, %d calls", err, f2.calls.Load())
	}
	_ = r2.Close(context.Background())
}

type senderFunc func(context.Context, circlexo.UsageReport) (circlexo.UsageResult, error)

func (f senderFunc) ReportUsage(ctx context.Context, r circlexo.UsageReport) (circlexo.UsageResult, error) {
	return f(ctx, r)
}
