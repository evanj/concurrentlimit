package grpclimit

import (
	"context"
	"log"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/evanj/concurrentlimit/sleepymemory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type blockSleeper struct {
	sleepymemory.UnimplementedSleeperServer
	unblock chan struct{}
}

func (b *blockSleeper) Sleep(
	ctx context.Context, request *sleepymemory.SleepRequest,
) (*sleepymemory.SleepResponse, error) {
	<-b.unblock

	return &sleepymemory.SleepResponse{}, nil
}

func TestGRPC(t *testing.T) {
	const permitted = 3

	// pick a random port that should be avaliable
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	err = listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	grpcAddr := "localhost:" + strconv.Itoa(port)

	// start the server
	grpcServer, err := NewServer(grpcAddr, permitted)
	if err != nil {
		panic(err)
	}
	handler := &blockSleeper{unblock: make(chan struct{})}
	sleepymemory.RegisterSleeperServer(grpcServer, handler)
	go func() {
		err = Serve(grpcServer, grpcAddr, permitted*2)
		if err != nil {
			t.Error(err)
		}
	}()
	defer grpcServer.GracefulStop()

	// make permitted + 1 requests on child goroutines
	responses := make(chan codes.Code)
	for i := 0; i < permitted+1; i++ {
		go func() {
			// need separate clients per goroutine otherwise the client back pressure prevents rejection
			// Sleep for at least 2 seconds due to gRPC's default connection backoff:
			// https://github.com/grpc/grpc/blob/master/doc/connection-backoff.md
			dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			conn, err := grpc.DialContext(dialCtx, grpcAddr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithBlock())
			cancel()
			if err != nil {
				log.Println("Dial:", err)
				close(responses)
				t.Error(err)
			}
			defer conn.Close()
			client := sleepymemory.NewSleeperClient(conn)

			_, err = client.Sleep(context.Background(), &sleepymemory.SleepRequest{})
			responses <- status.Code(err)
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
		if response == codes.OK {
			okCount++
		} else if response == codes.ResourceExhausted {
			rateLimitedCount++
		} else {
			t.Fatal("status code:", response)
		}
	}

	if !(okCount == permitted && rateLimitedCount == 1) {
		t.Error("unexpected OK and rate limited response counts:", okCount, rateLimitedCount)
	}
}
