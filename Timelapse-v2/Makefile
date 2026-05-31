BINARY   := timelapse-v2
SRC      := .
LOGDIR   := ./logs

.PHONY: build run stop test clean tidy

build:
	go build -o $(BINARY) $(SRC)

run: build
	./$(BINARY)

test:
	go test ./... -v

test-local:
	TEST_STORAGE=local go test ./... -v

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
	rm -rf $(LOGDIR)
