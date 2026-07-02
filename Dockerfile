# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags='-s -w' -o /out/scc-api ./cmd/api

FROM alpine:3.22
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S scc \
    && adduser -S -G scc -H -h /app scc

COPY --from=build /out/scc-api /app/scc-api

USER scc:scc
EXPOSE 8080
ENTRYPOINT ["/app/scc-api"]
