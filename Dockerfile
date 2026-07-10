# syntax=docker/dockerfile:1

FROM golang:1.25-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS build
WORKDIR /src

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags='-s -w' -o /out/scc-api ./cmd/api \
    && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags='-s -w' -o /out/scc-migrate ./cmd/migrate

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S scc \
    && adduser -S -G scc -H -h /app scc

COPY --from=build /out/scc-api /app/scc-api
COPY --from=build /out/scc-migrate /app/scc-migrate

USER scc:scc
EXPOSE 8080
ENTRYPOINT ["/app/scc-api"]
