package main

import (
	"context"
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
	"google.golang.org/grpc"
)

const sleepHTTPKey = "sleep"
const wasteHTTPKey = "waste"

type server struct {
	requests concurrentMaxLogger
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	_, err := s.Sleep(r.Context(), req)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	fmt.Fprintf(w, "slept for %s (pass ?sleep=x)\nwasted %d bytes (pass ?waste=y)\n",
		req.SleepDuration.String(), req.WasteBytes)
	return nil
}

func (s *server) Sleep(ctx context.Context, request *sleepymemory.SleepRequest) (*sleepymemory.SleepResponse, error) {
	defer s.requests.start()()

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
	time.Sleep(duration)

	return &sleepymemory.SleepResponse{}, nil
}

type concurrentMaxLogger struct {
	mu      sync.Mutex
	current int
	max     int
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
	flag.Parse()

	s := &server{}
	http.HandleFunc("/", s.rawRootHandler)
	http.HandleFunc("/stats", s.memstatsHandler)
	log.Printf("listening on http://%s ...", *httpAddr)
	go func() {
		err := http.ListenAndServe(*httpAddr, nil)
		if err != nil {
			panic(err)
		}
	}()

	log.Printf("listening for gRPC on grpcAddr=%s ...", *grpcAddr)
	grpcListener, err := net.Listen("tcp", *grpcAddr)
	if err != nil {
		panic(err)
	}
	grpcServer := grpc.NewServer()
	sleepymemory.RegisterSleeperServer(grpcServer, s)
	err = grpcServer.Serve(grpcListener)
	if err != nil {
		panic(err)
	}
}
