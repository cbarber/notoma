package notion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestNewWorkerPool(t *testing.T) {
	client := &Client{limiter: NewRateLimiter(100, 20)}

	tests := []struct {
		name        string
		concurrency int
		want        int
	}{
		{"normal concurrency", 5, 5},
		{"zero defaults to 1", 0, 1},
		{"negative defaults to 1", -1, 1},
		{"capped at 20", 50, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := NewWorkerPool(client, tt.concurrency)
			if pool.concurrency != tt.want {
				t.Errorf("NewWorkerPool(%d) concurrency = %d, want %d", tt.concurrency, pool.concurrency, tt.want)
			}
		})
	}
}

func TestDefaultWorkerPool(t *testing.T) {
	client := &Client{limiter: NewRateLimiter(100, 20)}
	pool := DefaultWorkerPool(client)

	if pool.concurrency != 5 {
		t.Errorf("DefaultWorkerPool() concurrency = %d, want 5", pool.concurrency)
	}
}

func TestWorkerPool_FetchBlocksParallel_ContextCanceled(t *testing.T) {
	client := &Client{limiter: NewRateLimiter(100, 20)}
	pool := NewWorkerPool(client, 3)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	pageIDs := []string{"page1", "page2", "page3"}
	results := pool.FetchBlocksParallel(ctx, pageIDs)

	// Should complete quickly without blocking
	select {
	case <-time.After(1 * time.Second):
		t.Fatal("FetchBlocksParallel did not respect canceled context")
	case _, ok := <-results:
		// Channel should be closed or return quickly
		if ok {
			// Drain any remaining results
			for range results {
			}
		}
	}
}

func TestWorkerPool_SemaphoreLimit(t *testing.T) {
	// This test verifies that the semaphore properly limits concurrency
	client := &Client{limiter: NewRateLimiter(1000, 100)} // High rate to not interfere
	pool := NewWorkerPool(client, 2)                      // Only 2 concurrent

	// Verify semaphore channel has correct capacity
	if cap(pool.semaphore) != 2 {
		t.Errorf("semaphore capacity = %d, want 2", cap(pool.semaphore))
	}
}

func TestWorkerPool_CancelMidBatchClosesResults(t *testing.T) {
	const concurrency = 5
	pageIDs := []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8"}

	fetchers := map[string]func(*WorkerPool, context.Context) <-chan struct{}{
		"FetchBlocksParallel": func(p *WorkerPool, ctx context.Context) <-chan struct{} {
			return drain(p.FetchBlocksParallel(ctx, pageIDs))
		},
		"FetchPagesParallel": func(p *WorkerPool, ctx context.Context) <-chan struct{} {
			return drain(p.FetchPagesParallel(ctx, pageIDs))
		},
		"FetchPagesWithBlocksParallel": func(p *WorkerPool, ctx context.Context) <-chan struct{} {
			return drain(p.FetchPagesWithBlocksParallel(ctx, pageIDs))
		},
	}

	for name, fetch := range fetchers {
		t.Run(name, func(t *testing.T) {
			arrived := make(chan struct{}, len(pageIDs))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				arrived <- struct{}{}
				<-r.Context().Done()
			}))
			t.Cleanup(srv.Close)

			target, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatalf("parsing server URL: %v", err)
			}
			client := newClient("test-token", nil, &http.Client{Transport: redirectTransport{target: target}})
			client.limiter = NewRateLimiter(1000, 100)
			pool := NewWorkerPool(client, concurrency)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := fetch(pool, ctx)

			// Cancel only once every worker slot is blocked in a request, so
			// the dispatcher is waiting on the semaphore with IDs left over.
			for range concurrency {
				select {
				case <-arrived:
				case <-time.After(5 * time.Second):
					t.Fatal("workers never reached the server")
				}
			}
			cancel()

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("results channel was not closed after cancel")
			}
		})
	}
}

func drain[T any](results <-chan T) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range results {
		}
	}()
	return done
}
