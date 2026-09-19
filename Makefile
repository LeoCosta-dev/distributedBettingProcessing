# Interface operacional explícita do projeto.
#
# O Compose é detectado nesta ordem: podman compose, depois docker compose.
# É possível selecionar explicitamente o runtime ou sobrescrever o comando:
#   make CONTAINER_RUNTIME=podman up
#   make CONTAINER_RUNTIME=docker up
#   make COMPOSE='podman compose' up

SHELL := /bin/sh
.SHELLFLAGS := -eu -c
.DEFAULT_GOAL := help

GO ?= go
GOFMT ?= gofmt
CONTAINER_RUNTIME ?= auto

ifeq ($(CONTAINER_RUNTIME),podman)
COMPOSE ?= podman compose
else ifeq ($(CONTAINER_RUNTIME),docker)
COMPOSE ?= docker compose
else ifeq ($(CONTAINER_RUNTIME),auto)
COMPOSE ?= $(shell \
	if command -v podman >/dev/null 2>&1 && podman compose version >/dev/null 2>&1; then \
		printf '%s' 'podman compose'; \
	elif command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then \
		printf '%s' 'docker compose'; \
	fi)
else
COMPOSE :=
endif

POSTGRES_USER ?= wagering
POSTGRES_DB ?= wagering
POSTGRES_PORT ?= 5432
LOCALSTACK_PORT ?= 4566
KEYCLOAK_PORT ?= 8082
HTTP_PORT ?= 8080

MIGRATE_DATABASE_URL ?= $(if $(DATABASE_URL),$(DATABASE_URL),postgres://$(POSTGRES_USER):wagering@localhost:$(POSTGRES_PORT)/$(POSTGRES_DB)?sslmode=disable)
HOST_AWS_ENDPOINT_URL ?= http://localhost:$(LOCALSTACK_PORT)
HOST_OIDC_ISSUER ?= http://localhost:$(KEYCLOAK_PORT)/realms/wagering
HOST_OIDC_JWKS_URL ?= $(HOST_OIDC_ISSUER)/protocol/openid-connect/certs
HOST_KEYCLOAK_TOKEN_URL ?= $(HOST_OIDC_ISSUER)/protocol/openid-connect/token

INTEGRATION_ENV = \
	DATABASE_URL='$(MIGRATE_DATABASE_URL)' \
	AWS_ENDPOINT_URL='$(HOST_AWS_ENDPOINT_URL)' \
	AWS_REGION='us-east-1' \
	AWS_ACCESS_KEY_ID='test' \
	AWS_SECRET_ACCESS_KEY='test' \
	OIDC_ISSUER='$(HOST_OIDC_ISSUER)' \
	OIDC_JWKS_URL='$(HOST_OIDC_JWKS_URL)' \
	OAUTH_AUDIENCE='wagering-api' \
	OAUTH_CLIENT_ID='wagering-api' \
	OAUTH_CLIENT_SECRET='change-me' \
	KEYCLOAK_TOKEN_URL='$(HOST_KEYCLOAK_TOKEN_URL)' \
	KEYCLOAK_TEST_USERNAME='provider-alpha' \
	KEYCLOAK_TEST_PASSWORD='dev-only-pass'

INTEGRATION_PACKAGES = \
	./internal/application/financial \
	./internal/application/query \
	./internal/application/outbox \
	./internal/composition \
	./internal/infrastructure/keycloak \
	./internal/infrastructure/postgres \
	./internal/transport/http \
	./internal/transport/messaging

.PHONY: help up down bootstrap migrate-up migrate-down run health ready \
	test test-race vet check fmt-check fmt integration failure-tests logs \
	build clean clean-all require-compose require-go require-gofmt \
	require-curl require-migrate migrate-check install-go-tools doctor \
	require-schema wait-infra

help:
	@printf '%s\n' \
		'make up             sobe PostgreSQL, LocalStack e Keycloak' \
		'make down           para a stack sem remover volumes' \
		'make bootstrap      aguarda PostgreSQL e aplica migrations' \
		'make doctor         verifica as dependências sem instalar nada' \
		'make install-go-tools instala ferramentas auxiliares Go' \
		'make migrate-check  verifica migrate e pergunta antes de instalar' \
		'make migrate-up     aplica migrations com a CLI migrate' \
		'make migrate-down   reverte uma migration com a CLI migrate' \
		'make run            inicia a API no host' \
		'make health         consulta /health/live' \
		'make ready          consulta /health/ready' \
		'make build          compila todos os pacotes Go' \
		'make test           executa go test -count=1 ./...' \
		'make test-race      executa go test -race -count=1 ./...' \
		'make vet            executa go vet ./...' \
		'make check          executa gofmt, testes, vet e git diff --check' \
		'make fmt            formata os arquivos Go' \
		'make integration    executa a matriz real após migrations aplicadas' \
		'make failure-tests  executa os testes reais do Loop 11 após migrations' \
		'make logs           acompanha logs dos serviços Compose' \
		'make clean          remove somente o cache de testes Go' \
		'make clean-all      remove volumes, somente com CONFIRM=yes'

require-compose:
	@if [ -z '$(COMPOSE)' ]; then \
		echo 'Nenhum provider Compose disponível. Use CONTAINER_RUNTIME=podman ou CONTAINER_RUNTIME=docker após configurar o runtime.' >&2; \
		exit 1; \
	fi; \
	set -- $(COMPOSE); \
	if ! command -v "$$1" >/dev/null 2>&1; then \
		echo "Comando do provider Compose não encontrado: $$1. Verifique CONTAINER_RUNTIME/COMPOSE." >&2; \
		exit 1; \
	fi

require-go:
	@command -v '$(GO)' >/dev/null 2>&1 || { echo "Go não encontrado: $(GO). Use, por exemplo, make GO=/caminho/para/go test" >&2; exit 1; }

require-gofmt:
	@command -v '$(GOFMT)' >/dev/null 2>&1 || { echo "gofmt não encontrado: $(GOFMT). Use, por exemplo, make GOFMT=/caminho/para/gofmt check" >&2; exit 1; }

require-curl:
	@command -v curl >/dev/null 2>&1 || { echo 'curl é necessário para health e ready.' >&2; exit 1; }

doctor:
	@set -eu; \
	status=0; \
	check_bin() { \
		label="$$1"; command_name="$$2"; \
		if command -v "$$command_name" >/dev/null 2>&1; then \
			printf 'PASS    %s (%s)\n' "$$label" "$$(command -v "$$command_name")"; \
		else \
			printf 'MISSING %s\n' "$$label"; status=1; \
		fi; \
	}; \
	check_bin 'go' '$(GO)'; \
	check_bin 'gofmt' '$(GOFMT)'; \
	check_bin 'make' 'make'; \
	check_bin 'curl' 'curl'; \
	check_bin 'migrate' 'migrate'; \
	compose_ok=0; \
	if [ -n '$(COMPOSE)' ]; then \
		set -- $(COMPOSE); \
		if command -v "$$1" >/dev/null 2>&1 && "$$@" version >/dev/null 2>&1; then \
			printf 'PASS    Compose (%s)\n' '$(COMPOSE)'; compose_ok=1; \
		fi; \
	fi; \
	if [ $$compose_ok -eq 0 ]; then printf 'MISSING Compose (Podman Compose ou Docker Compose)\n'; status=1; fi; \
	exit $$status

install-go-tools: require-go require-gofmt
	@set -eu; \
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest; \
	gobin="$$(go env GOBIN)"; \
	gopath="$$(go env GOPATH)"; \
	if [ -n "$$gobin" ] && [ -x "$$gobin/migrate" ]; then migrate_bin="$$gobin/migrate"; \
	elif [ -x "$$gopath/bin/migrate" ]; then migrate_bin="$$gopath/bin/migrate"; \
	else echo 'A instalação terminou, mas o binário migrate não foi encontrado em GOBIN/GOPATH/bin.' >&2; exit 1; fi; \
	"$$migrate_bin" -version; \
	if command -v migrate >/dev/null 2>&1; then \
		echo "migrate disponível no PATH: $$(command -v migrate)"; \
	else \
		echo "migrate instalado em $$migrate_bin, mas não está no PATH."; \
		echo 'Execute: export PATH="$$(go env GOPATH)/bin:$$PATH"'; \
	fi

migrate-check:
	@set -eu; \
	if command -v migrate >/dev/null 2>&1; then \
		echo "migrate encontrado em $$(command -v migrate)"; \
		exit 0; \
	fi; \
	echo 'A CLI migrate não está disponível no PATH.' >&2; \
	echo 'As migrations são versionadas no repositório, mas o runner não é embutido no projeto.' >&2; \
	if [ ! -t 0 ] || [ ! -t 1 ]; then \
		echo 'Ambiente não interativo: nenhuma instalação foi iniciada.' >&2; \
		echo 'Execute make install-go-tools após confirmar que go e gofmt estão no PATH.' >&2; \
		echo "Instalação manual: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest" >&2; \
		echo 'Se necessário, inclua $$(go env GOPATH)/bin no PATH.' >&2; \
		exit 1; \
	fi; \
	printf 'Deseja instalar agora? [y/N] '; \
	answer=''; \
	if IFS= read -r answer; then :; fi; \
	case "$$answer" in \
		y|Y) \
			$(MAKE) install-go-tools; \
			exit 0; \
			;; \
		*) \
			echo 'Nenhuma instalação foi iniciada.' >&2; \
			echo 'Execute make install-go-tools ou instale manualmente:' >&2; \
			echo "go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest" >&2; \
			echo 'Se necessário, inclua $$(go env GOPATH)/bin no PATH.' >&2; \
			exit 1 \
			;; \
	esac

require-migrate: migrate-check

require-schema: require-compose
	@if ! schema="$$( $(COMPOSE) exec -T postgres psql -U '$(POSTGRES_USER)' -d '$(POSTGRES_DB)' -Atc "SELECT CASE WHEN to_regclass('public.wallets') IS NOT NULL AND to_regclass('public.outbox') IS NOT NULL THEN 'ready' ELSE 'missing' END" 2>/dev/null )"; then \
		echo 'Não foi possível consultar o schema PostgreSQL. Execute make up e make bootstrap.' >&2; \
		exit 1; \
	fi; \
	if [ "$$schema" != 'ready' ]; then \
		echo 'As migrations não estão aplicadas no PostgreSQL. Execute make bootstrap antes da integração.' >&2; \
		exit 1; \
	fi

wait-infra: require-compose require-curl
	@i=1; ready=0; \
	while [ $$i -le 60 ]; do \
		if $(COMPOSE) exec -T postgres pg_isready -U '$(POSTGRES_USER)' -d '$(POSTGRES_DB)' >/dev/null 2>&1 \
			&& curl -fsS '$(HOST_AWS_ENDPOINT_URL)/_localstack/health' >/dev/null 2>&1 \
			&& curl -fsS '$(HOST_OIDC_ISSUER)/.well-known/openid-configuration' >/dev/null 2>&1; then ready=1; break; fi; \
		i=$$((i + 1)); sleep 1; \
	done; \
	if [ $$ready -ne 1 ]; then echo 'PostgreSQL, LocalStack e Keycloak não ficaram prontos dentro do timeout.' >&2; exit 1; fi

up: require-compose
	@$(COMPOSE) up -d postgres localstack keycloak

down: require-compose
	@$(COMPOSE) down

bootstrap: require-compose require-migrate
	@$(MAKE) wait-infra
	@echo 'LocalStack cria as filas e Keycloak importa o realm automaticamente pelo Compose.'
	@$(MAKE) migrate-up

migrate-up: require-migrate
	@set -eu; \
	if command -v migrate >/dev/null 2>&1; then migrate_bin="$$(command -v migrate)"; \
	else gopath="$$(go env GOPATH)"; gobin="$$(go env GOBIN)"; \
		if [ -n "$$gobin" ] && [ -x "$$gobin/migrate" ]; then migrate_bin="$$gobin/migrate"; \
		elif [ -x "$$gopath/bin/migrate" ]; then migrate_bin="$$gopath/bin/migrate"; \
		else echo 'migrate não foi encontrado no PATH nem em GOBIN/GOPATH/bin.' >&2; exit 1; fi; \
	fi; \
	DATABASE_URL='$(MIGRATE_DATABASE_URL)' "$$migrate_bin" -path migrations -database "$(MIGRATE_DATABASE_URL)" up

migrate-down: require-migrate
	@set -eu; \
	if command -v migrate >/dev/null 2>&1; then migrate_bin="$$(command -v migrate)"; \
	else gopath="$$(go env GOPATH)"; gobin="$$(go env GOBIN)"; \
		if [ -n "$$gobin" ] && [ -x "$$gobin/migrate" ]; then migrate_bin="$$gobin/migrate"; \
		elif [ -x "$$gopath/bin/migrate" ]; then migrate_bin="$$gopath/bin/migrate"; \
		else echo 'migrate não foi encontrado no PATH nem em GOBIN/GOPATH/bin.' >&2; exit 1; fi; \
	fi; \
	DATABASE_URL='$(MIGRATE_DATABASE_URL)' "$$migrate_bin" -path migrations -database "$(MIGRATE_DATABASE_URL)" down 1

run: require-go
	@DATABASE_URL='$(MIGRATE_DATABASE_URL)' \
	HTTP_ADDR=':$(HTTP_PORT)' \
	AWS_ENDPOINT_URL='$(HOST_AWS_ENDPOINT_URL)' \
	AWS_REGION='us-east-1' \
	AWS_ACCESS_KEY_ID='test' \
	AWS_SECRET_ACCESS_KEY='test' \
	KEYCLOAK_REALM='wagering' \
	OIDC_ISSUER='$(HOST_OIDC_ISSUER)' \
	OIDC_JWKS_URL='$(HOST_OIDC_JWKS_URL)' \
	OAUTH_AUDIENCE='wagering-api' \
	$(GO) run ./cmd/api

health: require-curl
	@curl -fsS 'http://localhost:$(HTTP_PORT)/health/live'

ready: require-curl
	@curl -fsS 'http://localhost:$(HTTP_PORT)/health/ready'

build: require-go
	@$(GO) build ./...

test: require-go
	@$(GO) test -count=1 ./...

test-race: require-go
	@$(GO) test -race -count=1 ./...

vet: require-go
	@$(GO) vet ./...

fmt-check: require-gofmt
	@files="$$($(GOFMT) -l .)"; \
	if [ -n "$$files" ]; then \
		echo 'Arquivos Go não formatados:' >&2; \
		echo "$$files" >&2; \
		exit 1; \
	fi

check: fmt-check test vet
	@git diff --check

fmt: require-gofmt
	@$(GOFMT) -w $$(find . -name '*.go' -not -path './vendor/*' -print)

integration: require-compose require-go
	@$(COMPOSE) up -d postgres localstack keycloak
	@$(MAKE) wait-infra
	@$(MAKE) require-schema
	@$(INTEGRATION_ENV) RUN_KEYCLOAK_INTEGRATION=1 RUN_OUTBOX_PRODUCTION_INTEGRATION=1 RUN_OUTBOX_FX_INTEGRATION=1 $(GO) test -count=1 -v $(INTEGRATION_PACKAGES)

failure-tests: require-compose require-go
	@$(COMPOSE) up -d postgres localstack keycloak
	@$(MAKE) wait-infra
	@$(MAKE) require-schema
	@$(INTEGRATION_ENV) RUN_OUTBOX_PRODUCTION_INTEGRATION=1 $(GO) test -count=1 -v -run '^TestLoop11' ./internal/application/financial ./internal/application/outbox ./internal/transport/messaging

logs: require-compose
	@$(COMPOSE) logs -f

clean: require-go
	@$(GO) clean -testcache

clean-all: require-compose
	@if [ '$(CONFIRM)' != 'yes' ]; then \
		echo 'clean-all remove volumes PostgreSQL/LocalStack. Confirme com: make clean-all CONFIRM=yes' >&2; \
		exit 1; \
	fi
	@$(COMPOSE) down -v
