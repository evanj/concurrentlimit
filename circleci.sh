#!/bin/bash
# Runs checks on CircleCI

set -euf -o pipefail

# echo commands
set -x

# Ensure protocol buffer definitions are up to date
make

# Run tests
go test -race ./...

# go test only checks some vet warnings; check all
go vet ./...

# cd /tmp to not change go.mod/go.sum for golint TODO: Use tools.go:
# https://github.com/golang/go/wiki/Modules#how-can-i-track-tool-dependencies-for-a-module
(cd /tmp && go get golang.org/x/lint/golint)
golint --set_exit_status ./...

diff -u <(echo -n) <(gofmt -d .)

# require that we use go mod tidy. TODO: there must be an easier way?
go mod tidy
CHANGED=$(git status --porcelain --untracked-files=no)
if [ -n "${CHANGED}" ]; then
    echo "ERROR files were changed:" > /dev/stderr
    echo "$CHANGED" > /dev/stderr
    exit 10
fi
