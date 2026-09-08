# Запускать из корня репозитория: cd ~/RioNexGate && make dev
ROOT := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
DOCKER_COMPOSE := $(ROOT)scripts/docker-compose.sh

.PHONY: build up down dev dev-cores dev-local docker-doctor test clean migrate init logs docker-check

init:
	cp -n backend/config.example.yaml backend/config.yaml 2>/dev/null || true
	cp -n .env.example .env 2>/dev/null || true
	mkdir -p data/xray data/sing-box data/awg data/nginx/ssl backups
	@echo "Panel URL after make dev: http://localhost:$${HTTP_PORT:-8888}"

docker-check:
	@$(ROOT)scripts/docker-check.sh

docker-doctor:
	@$(ROOT)scripts/docker-doctor.sh

build: docker-check
	$(DOCKER_COMPOSE) build

up: docker-check
	$(DOCKER_COMPOSE) up -d

down: docker-check
	$(DOCKER_COMPOSE) down

dev: docker-check
	$(DOCKER_COMPOSE) up --build

dev-cores: docker-check
	$(DOCKER_COMPOSE) --profile cores up -d xray-core

dev-cores-singbox: docker-check
	$(DOCKER_COMPOSE) --profile cores up -d sing-box

dev-local:
	@echo "Local development without Docker:"
	@echo "  Terminal 1: make -C backend dev    # API http://localhost:8080"
	@echo "  Terminal 2: cd frontend && npm run dev   # UI http://localhost:5173"
	@echo ""
	@echo "Migrate DB first (once): make -C backend migrate"

test:
	cd backend && CGO_ENABLED=1 go test ./...
	cd frontend && npm test --if-present

test-e2e:
	cd e2e && npm ci && npx playwright install chromium && npm test

migrate: docker-check
	$(DOCKER_COMPOSE) exec backend ./rionexgate migrate

logs: docker-check
	$(DOCKER_COMPOSE) logs -f

clean: docker-check
	$(DOCKER_COMPOSE) down -v
	rm -rf data/
