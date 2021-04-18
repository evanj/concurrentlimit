package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/evanj/concurrentlimit"
	"github.com/evanj/concurrentlimit/sleepymemory"
	"golang.org/x/net/netutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

const sleepHTTPKey = "sleep"
const wasteHTTPKey = "waste"

type server struct {
	logger         concurrentMaxLogger
	limiter        concurrentlimit.Limiter
	logAllRequests bool
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
		if err == concurrentlimit.ErrLimited {
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
		var sleepDuration time.Duration
		// try to parse as integer seconds first
		seconds, err := strconv.Atoi(sleepValue)
		if err == nil {
			// SUCCESS!
			sleepDuration = time.Duration(seconds) * time.Second
		} else {
			// fall back to parsing duration, and return that error if it fails
			sleepDuration, err = time.ParseDuration(sleepValue)
			if err != nil {
				return err
			}
		}
		req.SleepDuration = durationpb.New(sleepDuration)
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
	if err == concurrentlimit.ErrLimited {
		err = status.Error(codes.ResourceExhausted, err.Error())
	}
	return resp, err
}

func (s *server) sleepImplementation(ctx context.Context, request *sleepymemory.SleepRequest) (*sleepymemory.SleepResponse, error) {
	// limit concurrent requests
	end, err := s.limiter.Start()
	if err != nil {
		return nil, err
	}
	defer end()

	defer s.logger.start()()

	if s.logAllRequests {
		md, ok := metadata.FromIncomingContext(ctx)
		log.Printf("starting Sleep request=%s md=%v ok=%v", request.String(), md, ok)
	}

	wasteSlice := make([]byte, request.WasteBytes)
	// touch each page in the slice to ensure it is actually allocated
	const pageSize = 4096
	for i := 0; i < len(wasteSlice); i += pageSize {
		wasteSlice[i] = 0xff
	}

	var duration time.Duration
	if request.SleepDuration != nil {
		if err := request.SleepDuration.CheckValid(); err != nil {
			return nil, err
		}
		duration = request.SleepDuration.AsDuration()
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

func main() {
	httpAddr := flag.String("httpAddr", "localhost:8080", "Address to listen for HTTP requests")
	grpcAddr := flag.String("grpcAddr", "localhost:8081", "Address to listen for gRPC requests")
	concurrentRequests := flag.Int("concurrentRequests", 0, "Limits the number of concurrent requests")
	concurrentConnections := flag.Int("concurrentConnections", 0, "Limits the number of concurrent connections")
	grpcConcurrentStreams := flag.Int("grpcConcurrentStreams", 0, "Limits the number of concurrent connections")
	logAll := flag.Bool("logAll", false, "Log all requests")
	flag.Parse()

	s := &server{concurrentMaxLogger{}, concurrentlimit.NoLimit(), *logAll}
	if *concurrentRequests > 0 {
		log.Printf("limiting the server to %d concurrent requests", *concurrentRequests)
		s.limiter = concurrentlimit.New(*concurrentRequests)
	}

	mux := &http.ServeMux{}
	mux.HandleFunc("/", s.rawRootHandler)
	mux.HandleFunc("/stats", s.memstatsHandler)

	// copied from http/pprof
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

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
		err := http.Serve(httpListener, mux)
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
