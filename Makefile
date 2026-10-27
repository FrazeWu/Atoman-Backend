.PHONY: build-low-memory test test-focused test-postgres-up test-postgres-down migrate swagger swagger-check
.DEFAULT_GOAL := build-low-memory

test-postgres-up:
	docker compose -f docker-compose.test.yml up -d --wait --wait-timeout 60

test-postgres-down:
	docker compose -f docker-compose.test.yml down

# No exported DSN is needed. PACKAGE and TEST_ARGS can narrow the test run.
test:
	./scripts/test-isolated.sh $(or $(PACKAGE),./...) $(TEST_ARGS)

# Limit Go package-level parallelism on memory-constrained development machines.
build-low-memory:
	go build -p 1 ./...

# PACKAGE is required. Example: make test-focused PACKAGE=./internal/modules/blog TEST_ARGS='-run TestPostRating -count=1'
test-focused:
	@test -n "$(PACKAGE)" || { echo "PACKAGE is required" >&2; exit 2; }
	./scripts/test-isolated.sh $(PACKAGE) $(TEST_ARGS)

migrate:
	go run ./cmd/migrate -env .env.dev

swagger:
	go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/start_server/main.go --packageName docs

# Fail when docs/ no longer matches the Swagger annotations in the source (CI gate).
swagger-check:
	@before=$$(shasum docs/docs.go docs/swagger.json docs/swagger.yaml | shasum | awk '{print $$1}'); \
	$(MAKE) --no-print-directory swagger >/dev/null || { echo "swag generation failed"; exit 1; }; \
	after=$$(shasum docs/docs.go docs/swagger.json docs/swagger.yaml | shasum | awk '{print $$1}'); \
	if [ "$$before" != "$$after" ]; then \
		echo "swagger docs are out of date: run 'make swagger' and commit docs/"; \
		exit 1; \
	fi; \
	echo "swagger docs match the source annotations"
