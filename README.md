# distributedBettingProcessing

Backend challenge implementation for distributed wagering transaction processing.

## Status

Loop 6 HTTP is checkpointed and the Loop 7 SQS/inbox corrections have real
PostgreSQL and LocalStack execution evidence. Loop 7 is pending final human
review.

See:

* `SPEC.md` — functional and technical requirements;
* `ARCHITECTURE.md` — architecture and engineering decisions;
* `TASKS.md` — implementation loops and verification state;
* `AGENTS.md` — engineering rules for AI-assisted development.

## Current delivery status

Loop 7 includes the durable SQS inbox consumer and PostgreSQL/SQS readiness.
Pending-reference workers, transactional outbox, observability and failure
engineering remain pending. Its SQS integration tests are conditional for the
default local gate, but a skipped test is not evidence; use an available
compatible runtime to execute PostgreSQL and LocalStack when the integration
gate applies.

The complete challenge delivery requirements are not claimed as implemented
yet.

## Stack

* Go
* Uber Fx
* PostgreSQL
* AWS SQS
* LocalStack
* Keycloak
* a Compose-compatible runtime (for example Docker Compose or Podman Compose)

## Development

The checked-in Compose definition can be run with a compatible provider already
available in the environment. The absence of a particular runtime executable
does not by itself establish that integration is unavailable.

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
