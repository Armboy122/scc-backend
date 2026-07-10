.PHONY: migrate-build migrate-validate migrate-check migrate-status migrate-up test-minio-rotation

migrate-build:
	go build -trimpath -o bin/scc-migrate ./cmd/migrate

migrate-validate:
	go run ./cmd/migrate validate

migrate-check:
	go run ./cmd/migrate check

migrate-status:
	go run ./cmd/migrate status

migrate-up:
	go run ./cmd/migrate up

test-minio-rotation:
	bash scripts/test-minio-service-account-rotation.sh
