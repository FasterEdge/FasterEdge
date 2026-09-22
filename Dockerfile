# syntax=docker/dockerfile:1.7
FROM golang:1.25-alpine AS build
ENV GOPROXY=https://goproxy.cn,direct \
    GOSUMDB=sum.golang.google.cn
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
WORKDIR /src/examples/demo
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fasteredge-demo .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -H -u 1000 app && mkdir -p /app && chown -R app:app /app
WORKDIR /app
COPY --from=build /out/fasteredge-demo /usr/local/bin/fasteredge-demo
USER app
ENTRYPOINT ["fasteredge-demo"]
