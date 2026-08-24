.PHONY: build-low-memory test-focused migrate swagger

# Limit Go package-level parallelism on memory-constrained development machines.
build-low-memory:
	go build -p 1 ./...

# PACKAGE is required. Example: make test-focused PACKAGE=./internal/modules/blog TEST_ARGS='-run TestPostRating -count=1'
test-focused:
	go test -p 1 $(PACKAGE) $(TEST_ARGS)

migrate:
	go run ./cmd/migrate -env .env.dev

swagger:
	go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/start_server/main.go
