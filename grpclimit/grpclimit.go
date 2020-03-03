// Package grpclimit limits the number of concurrent requests and concurrent connections to a gRPC
// server to ensure that it does not run out of memory during overload scenarios.
package grpclimit

import (
	"context"
	"fmt"
	"time"

	"github.com/evanj/concurrentlimit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// ResourceExhausted seems slightly better than Unavailable, since
// is wrong. Other examples:
//
// https://github.com/grpc/grpc/blob/master/doc/statuscodes.md: "Server temporarily out of
// resources (e.g., Flow-control resource limits reached)"
//
// https://cloud.google.com/apis/design/errors: "Either out of resource quota or reaching rate
// limiting"
const rateLimitStatus = codes.ResourceExhausted

const idleConnectionTimeout = 10 * time.Minute
const keepaliveTimeout = time.Minute

// NewServer creates a grpc.Server and net.Listener that supports a limited number of concurrent
// requests. It sets the MaxConcurrentStreams option to the same value, which will cause
// requests to block on the client if a single client sends too many requests. You should also use
// Serve() with this server to protect against too many idle connections.
//
// NOTE: options must not contain any interceptors, since this function relies on adding our
// own interceptor to limit the requests. Use NewServerWithInterceptors if you need interceptors.
//
// TODO: Implement stream interceptors
func NewServer(
	addr string, requestLimit int, options ...grpc.ServerOption,
) (*grpc.Server, error) {
	return NewServerWithInterceptors(addr, requestLimit, nil, options...)
}

// NewServerWithInterceptors is a version of NewServer that permits customizing the interceptors.
// The passed in interceptor will be called after the operation limiter permits the request. See
// NewServer's documentation for the remaining details.
func NewServerWithInterceptors(
	addr string, requestLimit int, unaryInterceptor grpc.UnaryServerInterceptor,
	options ...grpc.ServerOption,
) (*grpc.Server, error) {
	if requestLimit <= 0 {
		return nil, fmt.Errorf("NewServer: requestLimit=%d must be > 0", requestLimit)
	}

	requestLimiter := concurrentlimit.New(requestLimit)
	limitedUnaryInterceptorChain := UnaryInterceptor(requestLimiter, unaryInterceptor)

	options = append(options, grpc.MaxConcurrentStreams(uint32(requestLimit)))
	options = append(options, grpc.UnaryInterceptor(limitedUnaryInterceptorChain))
	options = append(options, grpc.KeepaliveParams(keepalive.ServerParameters{
		MaxConnectionIdle: idleConnectionTimeout,
		Time:              keepaliveTimeout,
	}))
	server := grpc.NewServer(options...)

	return server, nil
}

// Serve listens on addr but only accepts a maximum of connectionLimit conenctions at one
// time to limit memory usage. New connections will block in the kernel. This returns when
// grpc.Server.Serve would normally return.
func Serve(server *grpc.Server, addr string, connectionLimit int) error {
	if connectionLimit <= 0 {
		return fmt.Errorf("NewServer: connectionLimit=%d must be >= 0", connectionLimit)
	}

	listener, err := concurrentlimit.Listen("tcp", addr, connectionLimit)
	if err != nil {
		return err
	}
	return server.Serve(listener)
}

// UnaryInterceptor returns a grpc.UnaryServerInterceptor that uses limiter to limit the
// concurrent requests. It will return codes.ResourceExhausted if the limiter rejects an operation.
// If next is not nil, it will be called to chain the request handlers. If it is nil, this will
// invoke the operation directly.
func UnaryInterceptor(limiter concurrentlimit.Limiter, next grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (interface{}, error) {
		end, err := limiter.Start()
		if err == concurrentlimit.ErrLimited {
			return nil, status.Error(rateLimitStatus, err.Error())
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
