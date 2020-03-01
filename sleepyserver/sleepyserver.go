package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/evanj/concurrentlimit/sleepymemory"
	"github.com/golang/protobuf/ptypes"
	"golang.org/x/net/netutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const sleepHTTPKey = "sleep"
const wasteHTTPKey = "waste"

type server struct {
	logger  concurrentMaxLogger
	limiter limiter
}

func (s *server) rawRootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "only GET is supported", http.StatusMethodNotAllowed)
		return
	}

	err := s.rootHandler(w, r)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if err == errLimited {
			statusCode = http.StatusTooManyRequests
		}
		http.Error(w, err.Error(), statusCode)
	}
}

func humanBytes(bytes uint64) string {
	megabytes := float64(bytes) / float64(1024*1024)
	return fmt.Sprintf("%.1f", megabytes)
}

func (s *server) memstatsHandler(w http.ResponseWriter, r *http.Request) {
	stats := &runtime.MemStats{}
	runtime.ReadMemStats(stats)

	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	fmt.Fprintf(w, "total bytes of memory obtained from the OS Sys=%d %s\n",
		stats.Sys, humanBytes(stats.Sys))
	fmt.Fprintf(w, "bytes of allocated heap objects HeapAlloc=%d %s\n",
		stats.HeapAlloc, humanBytes(stats.HeapAlloc))
}

func (s *server) rootHandler(w http.ResponseWriter, r *http.Request) error {
	req := &sleepymemory.SleepRequest{}

	sleepValue := r.FormValue(sleepHTTPKey)
	if sleepValue != "" {
		// parse as integer seconds first
		seconds, err := strconv.Atoi(sleepValue)
		if err != nil {
			// fall back to parsing duration, and return that error if it fails
			duration, err := time.ParseDuration(sleepValue)
			if err != nil {
				return err
			}

			req.SleepDuration = ptypes.DurationProto(duration)
		} else {
			req.SleepDuration = ptypes.DurationProto(time.Duration(seconds) * time.Second)
		}
	}

	wasteValue := r.FormValue(wasteHTTPKey)
	if wasteValue != "" {
		bytes, err := strconv.Atoi(wasteValue)
		if err != nil {
			return err
		}
		req.WasteBytes = int64(bytes)
	}

	resp, err := s.sleepImplementation(r.Context(), req)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	fmt.Fprintf(w, "slept for %s (pass ?sleep=x)\nwasted %d bytes (pass ?waste=y)\nignored response=%d\n",
		req.SleepDuration.String(), req.WasteBytes, resp.Ignored)
	return nil
}

func (s *server) Sleep(ctx context.Context, request *sleepymemory.SleepRequest) (*sleepymemory.SleepResponse, error) {
	resp, err := s.sleepImplementation(ctx, request)
	if err == errLimited {
		err = status.Error(codes.ResourceExhausted, err.Error())
	}
	return resp, err
}

func (s *server) sleepImplementation(ctx context.Context, request *sleepymemory.SleepRequest) (*sleepymemory.SleepResponse, error) {
	// limit concurrent requests
	end, err := s.limiter.start()
	if err != nil {
		return nil, err
	}
	defer end()

	defer s.logger.start()()

	wasteSlice := make([]byte, request.WasteBytes)
	// touch each page in the slice to ensure it is actually allocated
	const pageSize = 4096
	for i := 0; i < len(wasteSlice); i += pageSize {
		wasteSlice[i] = 0xff
	}

	var duration time.Duration
	if request.SleepDuration != nil {
		var err error
		duration, err = ptypes.Duration(request.SleepDuration)
		if err != nil {
			return nil, err
		}
	}
	// TODO: use ctx for cancellation
	time.Sleep(duration)

	// read some of the memory and return it so it doesn't get garbage collected
	total := 0
	for i := 0; i < len(wasteSlice); i += 10 * pageSize {
		total += int(wasteSlice[i])
	}

	return &sleepymemory.SleepResponse{Ignored: int64(total)}, nil
}

type concurrentMaxLogger struct {
	mu      sync.Mutex
	max     int
	current int
}

// start records the start of a concurrent request that is terminated when func is called.
func (c *concurrentMaxLogger) start() func() {
	c.mu.Lock()
	c.current++
	if c.current > c.max {
		c.max = c.current
		log.Printf("new max requests=%d", c.max)
	}
	c.mu.Unlock()

	return c.end
}

func (c *concurrentMaxLogger) end() {
	c.mu.Lock()
	c.current--
	if c.current < 0 {
		panic("bug: mismatched calls to startRequest/endRequest")
	}
	c.mu.Unlock()
}

var errLimited = errors.New("Limiter.start: exceeded limit of concurrent requests")

type concurrentLimiter struct {
	mu      sync.Mutex
	max     int
	current int
}

// start records the start of a concurrent request that is terminated when func is called.
func (c *concurrentLimiter) start() (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	next := c.current + 1
	if next > c.max {
		return nil, errLimited
	}
	c.current = next

	return c.end, nil
}

func (c *concurrentLimiter) end() {
	c.mu.Lock()
	c.current--
	if c.current < 0 {
		panic("bug: mismatched calls to start/end")
	}
	c.mu.Unlock()
}

type limiter interface {
	start() (func(), error)
}

type nilLimiter struct{}

func doNothing() {}

func (n *nilLimiter) start() (func(), error) {
	return doNothing, nil
}

func main() {
	httpAddr := flag.String("httpAddr", "localhost:8080", "Address to listen for HTTP requests")
	grpcAddr := flag.String("grpcAddr", "localhost:8081", "Address to listen for gRPC requests")
	concurrentRequests := flag.Int("concurrentRequests", 0, "Limits the number of concurrent requests")
	concurrentConnections := flag.Int("concurrentConnections", 0, "Limits the number of concurrent connections")
	grpcConcurrentStreams := flag.Int("grpcConcurrentStreams", 0, "Limits the number of concurrent connections")
	flag.Parse()

	s := &server{concurrentMaxLogger{}, &nilLimiter{}}
	if *concurrentRequests > 0 {
		log.Printf("limiting the server to %d concurrent requests", *concurrentRequests)
		s.limiter = &concurrentLimiter{sync.Mutex{}, *concurrentRequests, 0}
	}

	http.HandleFunc("/", s.rawRootHandler)
	http.HandleFunc("/stats", s.memstatsHandler)
	log.Printf("listening for HTTP on http://%s ...", *httpAddr)
	httpListener, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		panic(err)
	}
	if *concurrentConnections > 0 {
		log.Printf("limiting the HTTP server to %d concurrent connections", *concurrentConnections)
		httpListener = netutil.LimitListener(httpListener, *concurrentConnections)
	}

	go func() {
		err := http.Serve(httpListener, nil)
		if err != nil {
			panic(err)
		}
	}()

	log.Printf("listening for gRPC on grpcAddr=%s ...", *grpcAddr)
	grpcListener, err := net.Listen("tcp", *grpcAddr)
	if err != nil {
		panic(err)
	}
	if *concurrentConnections > 0 {
		log.Printf("limiting the gRPC server to %d concurrent connections", *concurrentConnections)
		grpcListener = netutil.LimitListener(grpcListener, *concurrentConnections)
	}

	options := []grpc.ServerOption{}
	if *grpcConcurrentStreams > 0 {
		log.Printf("setting grpc MaxConcurrentStreams=%d", *grpcConcurrentStreams)
		options = append(options, grpc.MaxConcurrentStreams(uint32(*grpcConcurrentStreams)))
	}
	grpcServer := grpc.NewServer(options...)
	sleepymemory.RegisterSleeperServer(grpcServer, s)
	err = grpcServer.Serve(grpcListener)
	if err != nil {
		panic(err)
	}
}
