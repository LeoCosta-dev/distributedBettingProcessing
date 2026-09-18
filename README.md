# distributedBettingProcessing

Backend challenge implementation for distributed wagering transaction processing.

## Status

Loop 6 HTTP is checkpointed. The complete distributed processing delivery is
still in progress.

See:

* `SPEC.md` — functional and technical requirements;
* `ARCHITECTURE.md` — architecture and engineering decisions;
* `TASKS.md` — implementation loops and verification state;
* `AGENTS.md` — engineering rules for AI-assisted development.

## Current delivery status

Loop 6 HTTP is checkpointed after review. SQS/inbox processing,
pending-reference workers, transactional outbox, observability and failure
engineering remain pending.

The complete challenge delivery requirements are not claimed as implemented
yet.

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

## License

This project is provided exclusively for technical evaluation,
recruitment, interview, and portfolio review purposes.

Production, commercial, redistribution, and derivative use is not
authorized without prior written permission from the copyright holder.

See [LICENSE](LICENSE).
