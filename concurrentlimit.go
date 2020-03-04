package concurrentlimit

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/netutil"
)

// ErrLimited is returned by Limiter when the concurrent operation limit is exceeded.
var ErrLimited = errors.New("exceeded max concurrent operations limit")

// This should be set longer than what upstream clients/load balancers will use to avoid
// a "connection race" where the client sends a request at the same time the server is closing
// it. This can cause errors that may not be retriable. This is the value recommended by Google
// Cloud: https://cloud.google.com/load-balancing/docs/https#timeouts_and_retries
const httpIdleTimeout = 620 * time.Second
const httpReadHeaderTimeout = time.Minute

// Limiter limits the number of concurrent operations that can be processed.
type Limiter interface {
	// Start begins a new operation. It returns a completion function that must be called when the
	// operation completes, or it returns ErrLimited if no more concurrent operations are allowed.
	// This should be called as:
	//
	// end, err := limiter.Start()
	// if err != nil {
	//     // Handle ErrLimited
	// defer end()
	Start() (func(), error)
}

// NoLimit returns a Limiter that permits an unlimited number of operations.
func NoLimit() Limiter {
	return &nilLimiter{}
}

type nilLimiter struct{}

func doNothing() {}

func (n *nilLimiter) Start() (func(), error) {
	return doNothing, nil
}

// New returns a Limiter that will only permit limit concurrent operations. It will panic if
// limit is < 0.
func New(limit int) Limiter {
	if limit <= 0 {
		panic(fmt.Sprintf("limit must be > 0: %d", limit))
	}
	return &syncLimiter{sync.Mutex{}, limit, 0}
}

type syncLimiter struct {
	mu      sync.Mutex
	max     int
	current int
}

func (s *syncLimiter) Start() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.current + 1
	if next > s.max {
		return nil, ErrLimited
	}
	s.current = next

	// TODO: Return a closure that can only be called once? More expensive but harder to abuse.
	// Maybe think about a "debug mode" that enables this sort of check?
	return s.end, nil
}

func (s *syncLimiter) end() {
	s.mu.Lock()
	s.current--
	if s.current < 0 {
		panic("bug: mismatched calls to start/end")
	}
	s.mu.Unlock()
}

// ListenAndServe listens for HTTP requests with a limited number of concurrent requests
// and connections. This helps avoid running out of memory during overload situations.
// Both requestLimit and connectionLimit must be > 0, and connectionLimit must be
// >= requestLimit. A reasonable defalt is to set the connectionLimit to double the request limit,
// which assumes that processing each request requires more memory than a raw connection, and that
// keeping some idle connections is useful. This modifies srv.Handler with another handler that
// implements the limit.
//
// This also sets the server's ReadHeaderTimeout and IdleTimeout to a reasonable default if they
// are not set, which is an attempt to avoid idle or slow connections using all connections.
func ListenAndServe(srv *http.Server, requestLimit int, connectionLimit int) error {
	limitedListener, err := limitListenerForServer(srv, requestLimit, connectionLimit)
	if err != nil {
		return err
	}

	return srv.Serve(limitedListener)
}

func limitListenerForServer(srv *http.Server, requestLimit int, connectionLimit int) (net.Listener, error) {
	if requestLimit <= 0 {
		return nil, fmt.Errorf("ListenAndServe: requestLimit=%d must be > 0", requestLimit)
	}
	if connectionLimit < requestLimit {
		return nil, fmt.Errorf("ListenAndServe: connectionLimit=%d must be >= requestLimit=%d",
			connectionLimit, requestLimit)
	}

	// prevent idle/slow connections using all available connections. See also:
	// https://blog.gopheracademy.com/advent-2016/exposing-go-on-the-internet/
	if srv.ReadHeaderTimeout <= 0 {
		srv.ReadHeaderTimeout = httpReadHeaderTimeout
	}
	if srv.IdleTimeout <= 0 {
		srv.IdleTimeout = httpIdleTimeout
	}

	// configure the request limit
	limiter := New(requestLimit)
	srv.Handler = Handler(limiter, srv.Handler)

	return Listen("tcp", srv.Addr, connectionLimit)
}

// ListenAndServeTLS listens for HTTP requests with a limited number of concurrent requests
// and connections. See the documentation for ListenAndServe for details.
func ListenAndServeTLS(
	srv *http.Server, certFile string, keyFile string, requestLimit int, connectionLimit int,
) error {
	limitedListener, err := limitListenerForServer(srv, requestLimit, connectionLimit)
	if err != nil {
		return err
	}

	return srv.ServeTLS(limitedListener, certFile, keyFile)
}

// Listen wraps net.Listen with netutil.LimitListener to limit concurrent connections.
func Listen(network string, address string, connectionLimit int) (net.Listener, error) {
	unlimitedListener, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}
	return netutil.LimitListener(unlimitedListener, connectionLimit), nil
}

// Handler returns an http.Handler that uses limiter to only permit a limited number of concurrent
// requests to be processed.
func Handler(limiter Limiter, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		end, err := limiter.Start()
		if err == ErrLimited {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		if err != nil {
			// this should not happen, but if it does return a very generic 500 error
			log.Println("concurrentlimit.Handler BUG: unexpected error: " + err.Error())
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		// permitted: start the operation and end it
		handler.ServeHTTP(w, r)
		end()
	})
}
