# Concurrent request/connection limits for Go servers

Each connection and request that a server is processing takes memory. If you have too many concurrent connections or requests, your server can run out of memory and crash. To make servers robust, it is a good idea to limit the amount of concurrent work that it accepts. The code in this repository tests how much memory HTTP and gRPC connections/requests take, and allows you to limit them.


## Running the server with limited memory and Docker

docker build . --tag=sleepyserver
docker run -p 127.0.0.1:8080:8080 -p 127.0.0.1:8081:8081 --rm -ti --memory=128m --memory-swap=128m sleepyserver

## To monitor in another terminal:

* `docker stats`
* `curl http://localhost:8080/stats`

## High memory per request

This client makes requests that use 1 MiB/request.

```
ulimit -n 10000
# HTTP
go run ./loadclient/main.go --httpTarget=http://localhost:8080/ --concurrent=80 --sleep=3s --waste=1048576 --duration=2m
# gRPC
go run ./loadclient/main.go --grpcTarget=localhost:8081 --concurrent=80 --sleep=3s --waste=1048576 --duration=2m
```



This reliably blows up the server very quickly. Adding the concurrent rate limiter --concurrentRequests=40 fixes it.


## Low memory per request (lots of idle requests)

This client makes requests that basically do nothing except use idle connections.

```
ulimit -n 10000
# HTTP
go run ./loadclient/main.go --httpTarget=http://localhost:8080/ --concurrent=5000 --sleep=20s --duration=2m
# gRPC
go run ./loadclient/main.go --grpcTarget=localhost:8081 --concurrent=5000 --sleep=20s --duration=2m
```

With HTTP and a docker memory limit of 128 MiB, on my machine 3000 concurrent connections seems to "work" but is dangerously close to the limit. Running the test a few times in a row seems to kill it. It seems like closing and re-opening connections causes an increase in memory usage. The gRPC test fails at a lower connection count (around 1000), so those connections are MUCH more memory expensive than HTTP connections.

* 3000-3100 works but unreliably
* 3200 works for a while but dies
* 3500 connections dies after a few minutes
* 3800 connections reliably dies

Using a concurrent request limit does NOT solve the problem, even with --concurrentRequests=40: There are simply too many connections and too much goroutine/connection overhead. To fix this, we need to reject new connections using --concurrentConnections=80.

