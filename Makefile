.PHONY: up down reset test test-go test-integration test-web

up:
	docker compose up --build

down:
	docker compose down

# Drop all data, including recorded runs, and start fresh.
reset:
	docker compose down -v

test: test-go test-web

test-go:
	cd apps/api && go test -race ./... && cd ../loadgen && go test ./...

# Requires PostgreSQL (with migrations) and Redis: `docker compose up postgres redis`.
test-integration:
	cd apps/api && DATABASE_URL=postgres://flashseat:flashseat@localhost:5432/flashseat?sslmode=disable REDIS_ADDR=localhost:6379 \
		go test -race -tags integration ./internal/store/ ./internal/inventory/

test-web:
	npm run typecheck && npm run lint && npm run build && npm test
