FROM golang:1.25-alpine AS base
WORKDIR /src
ENV CGO_ENABLED=0

FROM base AS deps
COPY go.* ./
RUN go mod download

FROM deps AS test
ENV CGO_ENABLED=1
RUN apk add --no-cache gcc musl-dev sqlite-dev
COPY . .
CMD ["go", "test", "./...", "-v", "-cover"]

FROM deps AS build
COPY . .
RUN GOOS=linux go build -o /app/api ./cmd/api

FROM alpine:3.22 AS run
RUN apk add --no-cache ca-certificates wget \
    && adduser -D -H -u 10001 appuser
COPY --from=build /app/api /api
USER appuser
EXPOSE 8080
ENTRYPOINT ["/api"]
