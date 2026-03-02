# syntax = docker/dockerfile:experimental
FROM golang:1.22-alpine AS build-env
ADD . /src
RUN cd /src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o app

# final stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build-env /src/app /
COPY --from=build-env /src/config /config
ENTRYPOINT ["/app"]
