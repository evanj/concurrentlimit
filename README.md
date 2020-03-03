# Concurrent request/connection limits for Go servers

Each connection and request that a server is processing takes memory. If you have too many, your server will run out of memory and crash. To make servers robust, you must limit the amount of concurrent work that it accepts, so it at least serves some requests during overload scenarios, rather than serving none. This package provides some APIs to make this easier, and some tools I used to test this.

For a really robust server, you should do the following, in rough priority order:

* Limit concurrent executing requests to limit memory.
* Limit concurrent connections to limit memory. In particular, Go gRPC connections are very expensive (~180 kiB versus about ~40 kiB per HTTP server connection).
* Close connections/requests that are too slow or idle, since they are wasting resources.
* Make clients well-behaved so they reduce their request rate on error, or stop entirely (exponential backoff, back pressure, circuit breakers). The gRPC `MaxConcurrentStreams` setting can help here.


# Possible future improvements to this code

* *gRPC streaming requests*: The `grpclimit` package currently only limits unary requests.

* *Faster implementation*: This uses a single sync.Mutex. It works well for ~10000 requests/second on 8 CPUs, but can be a bottleneck for extremely low-latency requests or high-CPU servers. Some sort of sharded counter, or something crazy like https://github.com/jonhoo/drwmutex would be more efficient.

* *Blocking/queuing*: This package currently rejects requests when over the limit. It probably would be better to queue requests for some period of time. This can cause fewer retries when there are short overload bursts. It also means that poorly behaved clients that retry too quickly will retry less often, which may ultimately be better. There are also choices here about LIFO versus FIFO, drop head versus drop tail. See my previous investigation: https://www.evanjones.ca/prevent-server-overload.html

* *Multiple buckets of limits*: Health checks, statistics, or other cheap requests should have much higher limits than expensive requests. It is possible this should be configurable.

* *Aggressively close idle connections on overload*: This package sets idle timeouts on connections to attempt to avoid lots of idle clients starving busy clients. It would be nice if this policy triggered on overload. If we are at the connection limit, we should aggressively close idle connections. If we are not, then we should not care.


## Running the server with limited memory and Docker

```
docker build . --tag=sleepyserver
docker run -p 127.0.0.1:8080:8080 -p 127.0.0.1:8081:8081 --rm -ti --memory=128m --memory-swap=128m sleepyserver
```

## To monitor in another terminal:

* `docker stats`
* `curl http://localhost:8080/stats`

## High memory per request

This client makes requests that use 1 MiB/request. Using 80 concurrent clients reliably blows up the server very quickly. Adding the concurrent rate limiter --concurrentRequests=40 fixes it.

```
ulimit -n 10000
# HTTP
go run ./loadclient/main.go --httpTarget=http://localhost:8080/ --concurrent=80 --sleep=3s --waste=1048576 --duration=2m
# gRPC
go run ./loadclient/main.go --grpcTarget=localhost:8081 --concurrent=80 --sleep=3s --waste=1048576 --duration=2m
```


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


## gRPC MaxConcurrentStreams

This limits the number of concurrent streams *per-client connection*, so this doesn't fix overload by itself. For example, setting it to 40, and using the "high memory" client above still blows through the limit. With the `--shareGRPC` client, this will protect it. With this option, the server communicates the limit back to the client, which means the client will block and slow down its rate of requests (back-pressure). It is still useful, but does not protect the server's resources appropriately from "worst case" scenarios.
