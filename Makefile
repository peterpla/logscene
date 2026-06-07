BINARY    := logscene.exe
SRC       := .
LOGDIR    := $(LOGSCENE_LOGDIR)
WINSCP       := C:/Program Files (x86)/WinSCP/winscp.com
WP_HOST      := sftp.wp.com
WP_USER      := peter368925f397-hbcyk.wordpress.com
WP_STAGE_USER := staging-0cf9-peter368925f397-hbcyk.wordpress.com
ifeq ($(OS),Windows_NT)
    VERSION   := $(shell git describe --tags --always --dirty)
    BUILDDATE := $(shell powershell -NoProfile -Command "(Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')")
else
    VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
    BUILDDATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
endif
LDFLAGS   := -X main.Version=$(VERSION) -X main.BuildDate=$(BUILDDATE)

.PHONY: build run stop test test-local logs tidy clean deploy-wp deploy-staging

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

deploy-wp:
	"$(WINSCP)" /command \
		"option batch abort" \
		"option confirm off" \
		"open sftp://$(WP_USER):$(WP_SFTP_PASSWORD)@$(WP_HOST)/" \
		"option batch continue" \
		"mkdir wp-content/plugins/logscene" \
		"option batch abort" \
		"synchronize remote -delete wp-plugin/logscene wp-content/plugins/logscene" \
		"exit"

deploy-staging:
	"$(WINSCP)" /command \
		"option batch abort" \
		"option confirm off" \
		"open sftp://$(WP_STAGE_USER):$(WP_STAGE_PASSWORD)@$(WP_HOST)/" \
		"option batch continue" \
		"mkdir wp-content/plugins/logscene" \
		"option batch abort" \
		"synchronize remote -delete wp-plugin/logscene wp-content/plugins/logscene" \
		"exit"
