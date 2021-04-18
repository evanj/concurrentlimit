package concurrentlimit

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestNoLimit(t *testing.T) {
	limiter := NoLimit()

	endFuncs := []func(){}
	for i := 0; i < 10000; i++ {
		end, err := limiter.Start()
		if err != nil {
			t.Fatal("NoLimit should never return an error")
		}
		endFuncs = append(endFuncs, end)
	}

	// calling all the end functions should work
	for _, end := range endFuncs {
		end()
	}
}

func TestLimiterRace(t *testing.T) {
	const permitted = 100
	limiter := New(permitted)

	// start the limiter in separate goroutines so hopefully the race detector can find bugs
	var wg sync.WaitGroup
	endFuncs := make(chan func(), permitted)
	for i := 0; i < permitted; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			end, err := limiter.Start()
			if err != nil {
				t.Error("Limiter must allow the first N calls", err)
			}
			endFuncs <- end
		}()
	}
	wg.Wait()

	// the next calls must fail
	for i := 0; i < 5; i++ {
		end, err := limiter.Start()
		if !(end == nil && err == ErrLimited) {
			t.Fatalf("Limiter must block calls after the first N calls: %p %#v", end, err)
		}
	}

	// Call one end function: the next Start call should work
	end := <-endFuncs
	end()
	end, err := limiter.Start()
	if !(end != nil && err == nil) {
		t.Fatal("The next call must succeed after end is called")
	}
	endFuncs <- end
	close(endFuncs)

	// calling all the end functions should work
	for end := range endFuncs {
		end()
	}
}

// Block HTTP requests until unblock is closed
type blockForConcurrent struct {
	unblock chan struct{}
}

func (b *blockForConcurrent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	<-b.unblock
}

func TestHTTP(t *testing.T) {
	// set up a rate limited HTTP server
	const permitted = 3

	// pick a random port that should be available
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	err = listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	httpAddr := "localhost:" + strconv.Itoa(port)

	// start the server
	handler := &blockForConcurrent{make(chan struct{})}
	testServer := &http.Server{
		Addr:    httpAddr,
		Handler: handler,
	}
	go func() {
		// must allow more connections than requests, otherwise it waits for the connection to close
		err := ListenAndServe(testServer, permitted, permitted*2)
		if err != http.ErrServerClosed {
			t.Error("expected HTTP server to be shutdown; err:", err)
		}
	}()
	defer testServer.Shutdown(context.Background())

	responses := make(chan int)
	for i := 0; i < permitted+1; i++ {
		go func() {
			const attempts = 3
			for i := 0; i < attempts; i++ {
				resp, err := http.Get("http://" + httpAddr)
				if err != nil {
					var syscallErr syscall.Errno
					if errors.As(err, &syscallErr) && syscallErr == syscall.ECONNREFUSED {
						// race with the server starting up: try again
						time.Sleep(10 * time.Millisecond)
						continue
					}
					close(responses)
					t.Error(err)

				}
				resp.Body.Close()
				responses <- resp.StatusCode
				return
			}
			t.Error("failed after too many attempts")
		}()
	}

	okCount := 0
	rateLimitedCount := 0
	for i := 0; i < permitted+1; i++ {
		response := <-responses
		if i == 0 {
			// unblock the handlers on the first response, no matter what it is
			close(handler.unblock)
		}

		if response == http.StatusOK {
			okCount++
		} else if response == http.StatusTooManyRequests {
			rateLimitedCount++
		} else {
			t.Fatal("unexpected HTTP status code:", response)
		}
	}

	if !(okCount == permitted && rateLimitedCount == 1) {
		t.Error("unexpected OK and rate limited response counts:", okCount, rateLimitedCount)
	}
}
