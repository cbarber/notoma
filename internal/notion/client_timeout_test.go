package notion

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// redirectTransport sends every request to target, since notionapi does not
// let callers override its base URL.
type redirectTransport struct {
	target *url.URL
}

func (rt redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = rt.target.Scheme
	req.URL.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestClientTimesOutStalledRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing server URL: %v", err)
	}

	const timeout = 200 * time.Millisecond
	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: redirectTransport{target: target},
	}
	c := newClient("test-token", nil, httpClient)

	start := time.Now()
	_, err = c.GetCurrentUser(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("request took %v, want it bounded by the %v timeout", elapsed, timeout)
	}
}
