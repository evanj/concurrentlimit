# Makefile for updating protocol buffer definitions and downloading required tools
BUILD_DIR:=build
PROTOC:=$(BUILD_DIR)/bin/protoc
PROTOC_GEN_GO:=$(BUILD_DIR)/protoc-gen-go

sleepymemory/sleepymemory.pb.go: sleepymemory/sleepymemory.proto $(PROTOC) $(PROTOC_GEN_GO)
	$(PROTOC) --plugin=$(PROTOC_GEN_GO) --go_out=paths=source_relative:. $<

# download protoc to a temporary tools directory
$(PROTOC): buildtools/getprotoc.go | $(BUILD_DIR)
	go run $< --outputDir=$(BUILD_DIR)

$(PROTOC_GEN_GO): | $(PROTOC_DIR)
	go build --mod=readonly -o $@ github.com/golang/protobuf/protoc-gen-go

$(BUILD_DIR):
	mkdir -p $@
