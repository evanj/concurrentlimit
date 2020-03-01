# Concurrent request/connection limits for Go servers

Each connection and request that a server is processing takes memory. If you have too many concurrent connections or requests, your server can run out of memory and crash. To make servers robust, it is a good idea to limit the amount of concurrent work that it accepts. The code in this repository tests how much memory HTTP and gRPC connections/requests take, and allows you to limit them.


# Running the server with limited memory and Docker

docker build . --tag=sleepyserver
docker run -p 127.0.0.1:8080:8080 -p 127.0.0.1:8081:8081 --rm -ti --memory=128m --memory-swap=128m sleepyserver
