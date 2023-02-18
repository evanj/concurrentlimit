package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/evanj/concurrentlimit/sleepymemory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

const grpcConnectTimeout = 30 * time.Second

func sendRequestsGoroutine(
	done <-chan struct{}, totalRequestsChan chan<- int, sender requestSender,
	req *sleepymemory.SleepRequest,
) {
	// create a new sender for each goroutine
	sender = sender.clone()

	requestCount := 0
sendLoop:
	for {
		// if done is closed, break out of the loop
		select {
		case <-done:
			break sendLoop
		default:
		}

		err := sender.send(req)
		if err != nil {
			if err == errRetry || err == context.DeadlineExceeded {
				// TODO: exponential backoff?
				time.Sleep(time.Second)
				continue
			}
			panic(err)
		}

		requestCount++
	}
	totalRequestsChan <- requestCount
}

var errRetry = errors.New("retriable error")

type requestSender interface {
	clone() requestSender
	send(req *sleepymemory.SleepRequest) error
}

type httpSender struct {
	client  *http.Client
	baseURL string
}

func newHTTPSender(baseURL string) *httpSender {
	return &httpSender{
		// create a separate client and transport so each goroutine uses a separate connection
		&http.Client{Transport: &http.Transport{}},
		baseURL,
	}
}

func (h *httpSender) clone() requestSender {
	return newHTTPSender(h.baseURL)
}

func (h *httpSender) send(req *sleepymemory.SleepRequest) error {
	reqURL := fmt.Sprintf("%s?sleep=%d&waste=%d",
		h.baseURL, req.SleepDuration.Seconds, req.WasteBytes)

	resp, err := h.client.Get(reqURL)
	if err != nil {
		// docker's proxy is pretty unhappy with how we hit this with tons of connections concurrently
		// we also get "operation timed out" when we are limiting the number of connections
		if strings.Contains(err.Error(), "connection reset by peer") ||
			strings.Contains(err.Error(), "write: broken pipe") ||
			strings.Contains(err.Error(), "operation timed out") {
			return errRetry
		}
		return err
	}
	defer resp.Body.Close()

	// drain the body so the connection can be reused by keep alives
	_, err = io.Copy(io.Discard, resp.Body)
	if err != nil {
		return err
	}
	err = resp.Body.Close()
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		// we were rate limited! Try again later
		return errRetry
	} else if resp.StatusCode != http.StatusOK {
		return errors.New("expected status ok: " + resp.Status)
	}
	return nil
}

type grpcSender struct {
	addr   string
	client sleepymemory.SleeperClient
}

func newGRPCSender(addr string) *grpcSender {
	return &grpcSender{addr: addr}
}

func (g *grpcSender) clone() requestSender {
	// copies the client: if it was used it will share a connection
	cloned := *g
	return &cloned
}

func (g *grpcSender) send(req *sleepymemory.SleepRequest) error {
	if g.client == nil {
		dialCtx, cancel := context.WithTimeout(context.Background(), grpcConnectTimeout)
		conn, err := grpc.DialContext(dialCtx, g.addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock())
		cancel()
		if err != nil {
			return err
		}
		g.client = sleepymemory.NewSleeperClient(conn)
	}

	_, err := g.client.Sleep(context.Background(), req)
	if status.Code(err) == codes.ResourceExhausted {
		err = errRetry
	}
	return err
}

func main() {
	httpTarget := flag.String("httpTarget", "", "HTTP address to send requests to")
	grpcTarget := flag.String("grpcTarget", "", "HTTP address to send requests to")
	duration := flag.Duration("duration", time.Minute, "Duration to run the test")
	concurrent := flag.Int("concurrent", 1, "Number of concurrent client goroutines")
	sleep := flag.Duration("sleep", 0, "Time for the server to sleep handling a request")
	waste := flag.Int("waste", 0, "Bytes of memory the server should waste while handling a request")
	shareGRPC := flag.Bool("shareGRPC", false, "If set, the gRPC goroutines will share a single client")
	flag.Parse()

	req := &sleepymemory.SleepRequest{
		SleepDuration: durationpb.New(*sleep),
		WasteBytes:    int64(*waste),
	}

	var sender requestSender
	if *httpTarget != "" {
		log.Printf("sending HTTP requests to %s ...", *httpTarget)
		sender = newHTTPSender(*httpTarget)
	} else if *grpcTarget != "" {
		log.Printf("sending gRPC requests to %s ...", *grpcTarget)
		sender = newGRPCSender(*grpcTarget)
		if *shareGRPC {
			// make a request to create the client before we clone it so it will be shared
			log.Printf("sharing a single gRPC connection ...")
			err := sender.send(req)
			if err != nil {
				panic(err)
			}
		}
	} else {
		panic("specify --httpTarget or --grpcTarget")
	}

	log.Printf("sending requests for %s using %d client goroutines ...",
		duration.String(), *concurrent)
	done := make(chan struct{})
	totalRequestsChan := make(chan int)
	for i := 0; i < *concurrent; i++ {
		go sendRequestsGoroutine(done, totalRequestsChan, sender, req)
	}

	time.Sleep(*duration)
	close(done)

	totalRequests := 0
	for i := 0; i < *concurrent; i++ {
		totalRequests += <-totalRequestsChan
	}
	close(totalRequestsChan)

	log.Printf("sent %d requests in %s using %d clients = %.3f reqs/sec",
		totalRequests, duration.String(), *concurrent, float64(totalRequests)/duration.Seconds())
}
