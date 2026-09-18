.PHONY: test race bench latency run web lint tidy docker

test:
	go test ./...

race:
	go test -race ./...

bench:
	go test ./internal/graph/ -bench=. -benchmem -run='^$$'

# The 10ms router budget, enforced.
latency:
	go test ./internal/graph/ -run TestRouterLatencyBudget -v

run:
	go run ./cmd/aggregator

web:
	cd web && npm run dev

lint:
	go vet ./...

tidy:
	go mod tidy

docker:
	docker compose up --build
