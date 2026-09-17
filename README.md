# distributedBettingProcessing

Backend challenge implementation for distributed wagering transaction processing.

## Status

Work in progress.

See:

* `SPEC.md` — functional and technical requirements;
* `ARCHITECTURE.md` — architecture and engineering decisions;
* `TASKS.md` — implementation loops and verification state;
* `AGENTS.md` — engineering rules for AI-assisted development.

## Stack

* Go
* Uber Fx
* PostgreSQL
* AWS SQS
* LocalStack
* Keycloak
* Docker Compose

## Development

The complete setup and execution instructions will be documented after the infrastructure and application flows are implemented.

## Verification

The final solution must provide reproducible commands for:

```bash
docker compose up --build
go test ./...
go test -race ./...
go vet ./...
```

## Architecture

See `ARCHITECTURE.md`.

## Requirements

See `SPEC.md`.

## Implementation Progress

See `TASKS.md`.
