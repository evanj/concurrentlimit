# Go build image: separate downloading dependencies from build for incremental builds
FROM golang:1.16.3-buster AS go_dep_downloader
WORKDIR concurrentlimit
COPY go.mod .
COPY go.sum .
RUN go mod download -x

# Go build image: separate downloading dependencies from build for incremental builds
FROM go_dep_downloader AS go_builder
COPY . .
RUN CGO_ENABLED=0 go install -v ./sleepyserver

FROM gcr.io/distroless/static-debian10:nonroot AS sleepyserver
COPY --from=go_builder /go/bin/sleepyserver /
ENTRYPOINT ["/sleepyserver"]
CMD ["--httpAddr=:8080", "--grpcAddr=:8081"]
EXPOSE 8080
EXPOSE 8081
