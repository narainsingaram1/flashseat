.PHONY: up down test test-go test-web smoke contention
up:
	docker compose up --build
down:
	docker compose down
test: test-go test-web
test-go:
	cd apps/api && go test ./...
test-web:
	npm test
smoke:
	k6 run load-tests/scenarios/journey.js
contention:
	k6 run load-tests/scenarios/same-seat-contention.js
