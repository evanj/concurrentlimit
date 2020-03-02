// Package grpclimit limits the number of concurrent requests and concurrent connections to a gRPC
// server to ensure that it does not run out of memory during overload scenarios.
package grpclimit

import (
	"context"
	"fmt"
	"net"

	"github.com/evanj/concurrentlimit"
	"golang.org/x/net/netutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// LimitedServer contains a grpc.Server and a net.Listener configured to limit concurrent work to
// prevent running out of memory in overload situations.
type LimitedServer struct {
	Server   *grpc.Server
	Listener net.Listener
}

// Serve is a convenience function that grpc.Server.Serve with the contained net.Listener.
func (l *LimitedServer) Serve() error {
	return l.Server.Serve(l.Listener)
}

// NewServer creates a grpc.Server and net.Listener that supports a limited number of concurrent
// requests and connections. It sets the MaxConcurrentStreams option to concurrentRequests, which
// will cause requests to block on the client if a single client sends too many requests.
//
// NOTE: options must not contain any interceptors, since this function relies on adding our
// own interceptor to limit the requests. Use NewServerWithInterceptors if you need interceptors.
//
// TODO: Implement stream interceptors
func NewServer(
	addr string, requestLimit int, connectionLimit int, options ...grpc.ServerOption,
) (*LimitedServer, error) {
	return NewServerWithInterceptors(addr, requestLimit, connectionLimit, nil, options...)
}

// NewServerWithInterceptors is a version of NewServer that permits customizing the interceptors.
// The passed in interceptor will be called after the operation limiter permits the request. See
// NewServer's documentation for the remaining details.
func NewServerWithInterceptors(
	addr string, requestLimit int, connectionLimit int, unaryInterceptor grpc.UnaryServerInterceptor,
	options ...grpc.ServerOption,
) (*LimitedServer, error) {
	if requestLimit <= 0 {
		return nil, fmt.Errorf("NewServer: requestLimit=%d must be > 0", requestLimit)
	}
	if connectionLimit < requestLimit {
		return nil, fmt.Errorf("NewServer: connectionLimit=%d must be >= requestLimit=%d",
			connectionLimit, requestLimit)
	}

	unlimitedListener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	limitedListener := netutil.LimitListener(unlimitedListener, connectionLimit)

	requestLimiter := concurrentlimit.New(requestLimit)
	limitedUnaryInterceptorChain := UnaryLimitInterceptor(requestLimiter, unaryInterceptor)

	options = append(options, grpc.MaxConcurrentStreams(uint32(requestLimit)))
	options = append(options, grpc.UnaryInterceptor(limitedUnaryInterceptorChain))
	server := grpc.NewServer(options...)

	return &LimitedServer{server, limitedListener}, nil
}

// UnaryLimitInterceptor returns a grpc.UnaryServerInterceptor that uses limiter to limit the
// concurrent requests. It will return codes.ResourcesExceeded if the limiter rejects an operation.
// If next is not nil, it will be called to chain the request handlers. If it is nil, this will
// invoke the operation directly.
func UnaryLimitInterceptor(limiter concurrentlimit.Limiter, next grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (interface{}, error) {
		end, err := limiter.Start()
		if err == concurrentlimit.ErrLimited {
			return nil, status.Error(codes.ResourceExhausted, err.Error())
		}
		if err != nil {
			return nil, err
		}
		defer end()

		if next != nil {
			return next(ctx, req, info, handler)
		}
		return handler(ctx, req)
	}
}
