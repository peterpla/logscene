BINARY   := timelapse-v2
SRC      := .
LOGDIR   := ./logs

.PHONY: build run stop test test-local logs tidy clean

build:
	go build -o $(BINARY) $(SRC)

run: build
	./$(BINARY)

test:
	go test ./... -v

test-local:
	TEST_STORAGE=local go test ./... -v

logs:
	powershell -NoProfile -Command "Get-ChildItem $(LOGDIR) -Filter timelapse-*.log | Sort-Object LastWriteTime -Descending | Select-Object -First 1 | ForEach-Object { Get-Content $$_.FullName -Wait }"

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
	rm -rf $(LOGDIR)
