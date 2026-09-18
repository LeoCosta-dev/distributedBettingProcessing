# distributedBettingProcessing

Backend challenge implementation for distributed wagering transaction processing.

## Status

Loop 9 is checkpointed with real PostgreSQL and LocalStack evidence. Loop 10
is implemented and pending human review.

See:

* `SPEC.md` — functional and technical requirements;
* `ARCHITECTURE.md` — architecture and engineering decisions;
* `TASKS.md` — implementation loops and verification state;
* `AGENTS.md` — engineering rules for AI-assisted development.

## Current delivery status

Loop 7 includes the durable SQS inbox consumer and PostgreSQL/SQS readiness.
Loop 8 adds durable pending-reference resolution with restartable retries.
Loop 9 adds transactional outbox publication with recoverable claims and stable
event IDs. Loop 10 adds structured diagnostics and Prometheus-compatible
metrics; failure engineering remains pending. Its SQS integration tests are conditional for the
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

## Observability

The process emits JSON logs. HTTP requests accept or generate
`X-Correlation-ID` (bounded to a safe ASCII implementation format), and
`GET /metrics` exposes low-cardinality processing, separately classified
duplicate/retry, receive-exhaustion redrive-candidate, outbox-lag and
reconciliation metrics. The application does not claim to observe broker DLQ
insertion directly. Targeted observability integrations use the real
PostgreSQL, LocalStack, Keycloak and Fx stack. Loop 9 ordering integrations
use isolated PostgreSQL schemas and FIFO queues, and the aggregate integration
command passes with that test-harness correction.
