# syntax = docker/dockerfile:1-experimental
# NOTE: Last edited 2020-07-17 (v1 era, Google Cloud Run / GCS deployment).
# Not actively maintained since then — buyer beware.

FROM golang:1.14.5-alpine AS build
RUN mkdir /src
WORKDIR /src
ENV CGO_ENABLED=0
COPY . .
# the line below fails with:
# "failed to solve with frontend dockerfile.v0: failed to solve with frontend gateway.v0:
# rpc error: code = Unknown desc = failed to build LLB:
# executor failed running [/bin/sh -c go mod download]: runc did not terminate sucessfully"
RUN go mod download
RUN --mount=type=cache,target=/root/.cache/go-build \
    GOOS=linux GOARCH=amd64 \
    go build -o /out/timelapse .

FROM scratch AS bin
COPY --from=build /out/timelapse /
ENTRYPOINT ["timelapse"]