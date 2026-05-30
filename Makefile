.PHONY: build run start stop test clean

build:
	go build -o timelapse.exe .

run: build
	./timelapse.exe

start: build
	./timelapse.exe >> timelapse.log 2>&1 &

stop:
	-taskkill //F //IM timelapse.exe 2>/dev/null; true

test:
	go test -v .

clean:
	rm -f timelapse.exe

# Linux binary via Docker
bin/timelapse:
	@docker build . --target bin \
	--output bin/
