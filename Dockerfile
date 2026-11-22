# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags "-s -w" -o /out/orcaterm2api ./cmd/server

FROM alpine:3.20
RUN adduser -D -H -u 10001 app \
 && mkdir -p /data/auths && chown -R app:app /data
USER app
WORKDIR /data
COPY --from=build /out/orcaterm2api /usr/local/bin/orcaterm2api
ENV TZ=Asia/Shanghai
EXPOSE 7864
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/orcaterm2api", "-config", "/data/config.json"]
