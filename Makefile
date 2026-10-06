.PHONY: postgres-up postgres-down migrate-up migrate-down migrate-force migrate-version test vet

ENV_FILE ?= .env.example
DATABASE_URL ?= postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable
MIGRATIONS_DIR ?= file://migrations

postgres-up:
	docker compose --env-file $(ENV_FILE) up -d postgres

postgres-down:
	docker compose --env-file $(ENV_FILE) down

migrate-up:
	migrate -path migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path migrations -database "$(DATABASE_URL)" down 1

migrate-version:
	migrate -path migrations -database "$(DATABASE_URL)" version

test:
	go test ./...

vet:
	go vet ./...
