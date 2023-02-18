# Makefile for updating protocol buffer definitions and downloading required tools
BUILD_DIR:=build
PROTOC:=$(BUILD_DIR)/bin/protoc
PROTOC_GEN_GO:=$(BUILD_DIR)/protoc-gen-go
PROTOC_GEN_GO_GRPC:=$(BUILD_DIR)/protoc-gen-go-grpc

sleepymemory/sleepymemory.pb.go: sleepymemory/sleepymemory.proto $(PROTOC) $(PROTOC_GEN_GO) $(PROTOC_GEN_GO_GRPC)
		$(PROTOC) --plugin=$(PROTOC_GEN_GO) --plugin=$(PROTOC_GEN_GO_GRPC) \
		--go_out=paths=source_relative:. \
		--go-grpc_out=paths=source_relative:. \
		$<

# download protoc to a temporary tools directory
$(PROTOC): $(BUILD_DIR)/getprotoc | $(BUILD_DIR)
	$(BUILD_DIR)/getprotoc --outputDir=$(BUILD_DIR)

$(BUILD_DIR)/getprotoc: | $(BUILD_DIR)
	GOBIN=$(realpath $(BUILD_DIR)) go install github.com/evanj/hacks/getprotoc@latest

# go install uses the version of protoc-gen-go specified by go.mod ... I think
$(PROTOC_GEN_GO): go.mod | $(BUILD_DIR)
	GOBIN=$(realpath $(BUILD_DIR)) go install google.golang.org/protobuf/cmd/protoc-gen-go

# manually specified version since we don't import this from code anywhere
# TODO: Import this from some tool so it gets updated with go get?
$(PROTOC_GEN_GO_GRPC): go.mod | $(BUILD_DIR)
	GOBIN=$(realpath $(BUILD_DIR)) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.2.0

$(BUILD_DIR):
	mkdir -p $@

clean:
	$(RM) -r $(BUILD_DIR)

docker:
	docker build . --tag=gcr.io/networkping/sleepyserver:$(shell date '+%Y%m%d')-$(shell git rev-parse --short=10 HEAD)
