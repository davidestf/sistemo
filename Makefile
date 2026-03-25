.PHONY: build dashboard test clean lint

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

# Development: run dashboard dev server with API proxy
dev-dashboard:
	cd frontend/dashboard && npm run dev -- --port 5173
