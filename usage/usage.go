// Package usage reports metered usage to the hub. Reports are buffered and
// sent in the background with retries; every report carries an idempotency
// key, so a retry after a timeout is never charged twice.
package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/circlexo/circlexo-go"
)

// ErrClosed is returned by Report after Close.
var ErrClosed = errors.New("circlexo: usage reporter closed")

// ErrFull is returned when the buffer is full; report synchronously with
// Send or raise Buffer.
var ErrFull = errors.New("circlexo: usage buffer full")

// Sender is the hub call; *circlexo.Client implements it.
type Sender interface {
	ReportUsage(ctx context.Context, r circlexo.UsageReport) (circlexo.UsageResult, error)
}

// Reporter buffers usage and sends it in the background.
type Reporter struct {
	sender Sender
	// OnResult, if set, sees every sent report's outcome, e.g. to warn about
	// LowBalance or log a 402 (limit or balance exhausted, not retried).
	OnResult func(circlexo.UsageReport, circlexo.UsageResult, error)
	// Retries per report on network or 5xx errors (default 5, backing off from 500ms).
	Retries int
	Backoff time.Duration

	ch     chan circlexo.UsageReport
	wg     sync.WaitGroup
	mu     sync.RWMutex
	closed bool
}

// NewReporter starts a reporter with room for buffer reports and workers
// sending concurrently.
func NewReporter(s Sender, buffer, workers int) *Reporter {
	if buffer <= 0 {
		buffer = 1024
	}
	if workers <= 0 {
		workers = 2
	}
	r := &Reporter{sender: s, Retries: 5, Backoff: 500 * time.Millisecond, ch: make(chan circlexo.UsageReport, buffer)}
	for i := 0; i < workers; i++ {
		r.wg.Add(1)
		go r.work()
	}
	return r
}

// Key returns a random idempotency key, for usage that has no natural one.
// Prefer a key derived from the work itself (e.g. "invoice:123:send") so a
// crash-and-retry of the same work is recognised.
func Key() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Report queues qty of feature for orgID. It does not wait for the hub.
func (r *Reporter) Report(orgID, feature string, qty int64, idempotencyKey string) error {
	if idempotencyKey == "" {
		return errors.New("circlexo: usage needs an idempotency key")
	}
	if qty <= 0 {
		return errors.New("circlexo: usage qty must be positive")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrClosed
	}
	select {
	case r.ch <- circlexo.UsageReport{OrgID: orgID, Feature: feature, Qty: qty, IdempotencyKey: idempotencyKey}:
		return nil
	default:
		return ErrFull
	}
}

// Send reports synchronously with retries, for usage the caller must know
// was accepted (e.g. before doing pay-as-you-go work).
func (r *Reporter) Send(ctx context.Context, u circlexo.UsageReport) (circlexo.UsageResult, error) {
	var res circlexo.UsageResult
	var err error
	wait := r.Backoff
	for i := 0; i <= r.Retries; i++ {
		res, err = r.sender.ReportUsage(ctx, u)
		if err == nil || !retryable(err) {
			return res, err
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
	return res, err
}

func retryable(err error) bool {
	var ae *circlexo.APIError
	if errors.As(err, &ae) {
		return ae.Status >= 500 || ae.Status == 429
	}
	return !errors.Is(err, context.Canceled)
}

func (r *Reporter) work() {
	defer r.wg.Done()
	for u := range r.ch {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		res, err := r.Send(ctx, u)
		cancel()
		if r.OnResult != nil {
			r.OnResult(u, res, err)
		}
	}
}

// Close stops accepting reports and waits for the buffer to drain or ctx to end.
func (r *Reporter) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.ch)
	}
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
