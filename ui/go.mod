// This empty module keeps the Go toolchain out of the UI tree: npm packages such as
// flatted ship Go sources under node_modules, and without a module boundary here
// `go test ./...`, `go vet ./...` and golangci-lint would build and lint them.
module github.com/migalsp/costdeck-operator/ui

go 1.26.6
