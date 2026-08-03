package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

var errResponseWriteDeadline = errors.New("response write deadline")

type deadlineResponseWriter struct {
	header http.Header
	block  bool

	writeStarted   chan struct{}
	writeReturned  chan struct{}
	deadlineSignal chan struct{}

	writeStartedOnce  sync.Once
	writeReturnedOnce sync.Once
	deadlineOnce      sync.Once

	mu        sync.Mutex
	deadlines []time.Time
}

func newDeadlineResponseWriter(block bool) *deadlineResponseWriter {
	return &deadlineResponseWriter{
		header:         make(http.Header),
		block:          block,
		writeStarted:   make(chan struct{}),
		writeReturned:  make(chan struct{}),
		deadlineSignal: make(chan struct{}),
	}
}

func (w *deadlineResponseWriter) Header() http.Header { return w.header }

func (w *deadlineResponseWriter) WriteHeader(int) {}

func (w *deadlineResponseWriter) Write(p []byte) (int, error) {
	w.writeStartedOnce.Do(func() { close(w.writeStarted) })
	defer w.writeReturnedOnce.Do(func() { close(w.writeReturned) })
	if w.block {
		<-w.deadlineSignal
		return 0, errResponseWriteDeadline
	}
	return len(p), nil
}

func (w *deadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadlines = append(w.deadlines, deadline)
	w.mu.Unlock()
	if !deadline.IsZero() {
		w.deadlineOnce.Do(func() { close(w.deadlineSignal) })
	}
	return nil
}

func (w *deadlineResponseWriter) deadlinesSnapshot() []time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Time(nil), w.deadlines...)
}

func TestServeMCPResponseWriteDeadlineCancellationAndCleanup(t *testing.T) {
	t.Run("cancellation deadline interrupts blocked write", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
		writer := newDeadlineResponseWriter(true)
		writeErr := make(chan error, 1)
		done := make(chan struct{})
		go func() {
			serveMCPResponse(writer, req, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, err := w.Write([]byte("blocked"))
				writeErr <- err
			}))
			close(done)
		}()

		select {
		case <-writer.writeStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("handler did not enter blocked Write")
		}
		cancel()

		select {
		case err := <-writeErr:
			if !errors.Is(err, errResponseWriteDeadline) {
				t.Fatalf("Write error = %v, want deadline interruption", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cancellation did not interrupt blocked Write")
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("serveMCPResponse did not return after cancellation")
		}

		deadlines := writer.deadlinesSnapshot()
		if len(deadlines) < 2 || deadlines[0].IsZero() || !deadlines[len(deadlines)-1].IsZero() {
			t.Fatalf("write deadlines = %v, want cancellation deadline followed by reset", deadlines)
		}
	})

	t.Run("normal return clears request deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
		writer := newDeadlineResponseWriter(false)
		serveMCPResponse(writer, req, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte("ok")); err != nil {
				t.Fatalf("normal Write: %v", err)
			}
		}))

		deadlines := writer.deadlinesSnapshot()
		if len(deadlines) != 2 || deadlines[0].IsZero() || !deadlines[1].IsZero() {
			t.Fatalf("write deadlines = %v, want request deadline followed by reset", deadlines)
		}
	})
}
