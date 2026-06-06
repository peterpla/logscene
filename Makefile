BINARY    := logscene.exe
SRC       := .
LOGDIR    := $(LOGSCENE_LOGDIR)
VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILDDATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -X main.Version=$(VERSION) -X main.BuildDate=$(BUILDDATE)

.PHONY: build run stop test test-local logs tidy clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(SRC)

run: build
	./$(BINARY)

test:
	go test ./... -v

test-local:
	TEST_STORAGE=local go test ./... -v

logs:
	powershell -NoProfile -Command "Get-ChildItem $(LOGDIR) -Filter logscene-*.log | Sort-Object LastWriteTime -Descending | Select-Object -First 1 | ForEach-Object { Get-Content $$_.FullName -Wait }"

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
