.PHONY: build dashboard test clean lint fmt vet coverage vulncheck

# Build the dashboard frontend and embed it in the Go binary
dashboard:
	cd frontend/dashboard && npm ci && npm run build
	rm -rf internal/agent/api/dashboard_dist
	cp -r frontend/dashboard/dist internal/agent/api/dashboard_dist

# Build the release binary (includes embedded dashboard)
build: dashboard
	CGO_ENABLED=0 go build -ldflags="-s -w" -o sistemo ./cmd/sistemo

# Build without dashboard (faster, for backend-only development)
build-quick:
	go build -o sistemo ./cmd/sistemo

# Run all tests with race detection
test:
	go test -race -count=1 ./...

# Run linter
lint:
	golangci-lint run

# Clean build artifacts
clean:
	rm -rf frontend/dashboard/dist internal/agent/api/dashboard_dist sistemo

# Format Go code
fmt:
	gofmt -w .

# Run go vet
vet:
	go vet ./...

# Run tests with coverage report
coverage:
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

# Check for known vulnerabilities
vulncheck:
	govulncheck ./...

# Development: run dashboard dev server with API proxy
dev-dashboard:
	cd frontend/dashboard && npm run dev -- --port 5173
