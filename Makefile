.PHONY: help test tidy lint update-lint vulncheck modernize modernize-fix fmt vet check build clean

.DEFAULT_GOAL := help

## help: Show this help message
help:
	@echo "Available targets:"
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'

## test: Run the test suite
test:
	@echo "Running tests..."
	@go test -race ./...

tidy:
	@echo "Tidying Go modules..."
	@go mod tidy

## lint: Run golangci-lint with auto-fix enabled
lint:
	@echo "Running golangci-lint..."
	@go tool -modfile=tools/golangci-lint/go.mod golangci-lint run --fix ./...

## update-lint: Update golangci-lint to latest version
update-lint:
	@echo "Updating golangci-lint..."
	@go get -tool -modfile=tools/golangci-lint/go.mod github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

## vulncheck: Check for known vulnerabilities in dependencies
vulncheck:
	@echo "Running govulncheck..."
	@go tool -modfile=tools/govulncheck/go.mod govulncheck ./...

## modernize: Report code that could use newer Go language/stdlib features
modernize:
	@echo "Running modernize..."
	@go tool -modfile=tools/modernize/go.mod modernize ./...

## modernize-fix: Apply modernize's suggested fixes
modernize-fix:
	@echo "Applying modernize fixes..."
	@go tool -modfile=tools/modernize/go.mod modernize -fix ./...

## fmt: Format code
fmt:
	@echo "Formatting code..."
	@go fmt ./...

## vet: Run go vet
vet:
	@echo "Running go vet..."
	@go vet ./...

## check: Run all checks (format, vet, lint, vulncheck, test)
check: tidy fmt vet lint vulncheck test
	@echo "All checks completed"

## build: Build the example and pdfpasswd binaries
build:
	@go build ./...

## clean: Remove build artifacts
clean:
	@rm -rf bin/
