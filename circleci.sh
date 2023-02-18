#!/bin/bash
# Runs checks on CircleCI

set -euf -o pipefail

# echo commands
set -x

# Ensure protocol buffer definitions are up to date
make

# Run tests
go test -count=2 -shuffle=on -race ./...

# go test only checks some vet warnings; check all
go vet ./...

go install honnef.co/go/tools/cmd/staticcheck@latest
staticcheck --checks=all ./...

go fmt ./...
# require that we use go mod tidy. TODO: there must be an easier way?
go mod tidy
CHANGED=$(git status --porcelain --untracked-files=no)
if [ -n "${CHANGED}" ]; then
    echo "ERROR files were changed:" > /dev/stderr
    echo "$CHANGED" > /dev/stderr
    exit 10
fi
