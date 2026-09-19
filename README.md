# distributedBettingProcessing

Implementação do desafio de backend para processamento distribuído de
transações de apostas. O sistema usa Go, Uber Fx, PostgreSQL, SQS por meio do
LocalStack e Keycloak. A correção financeira é respaldada pelo PostgreSQL:
dinheiro exato, locking por wallet, idempotência durável, ledger append-only,
inbox transacional e outbox transacional não são delegados à semântica de
entrega FIFO.

## Status

Os Loops 7–11 estão com checkpoint. O Loop 12 concluiu a validação final e está
pendente de Human Review. A implementação versionada cobre o escopo do
desafio; as limitações documentadas abaixo são fronteiras deliberadas, não
alegações de entrega exactly-once ou de teste de perda física de energia.

Consulte:

* `SPEC.md` — requisitos funcionais e técnicos;
* `ARCHITECTURE.md` — decisões e limitações arquiteturais;
* `TASKS.md` — estado dos loops e das verificações;
* `AGENTS.md` — regras de engenharia.

## Pré-requisitos

* Go 1.27.1 (versão declarada em `go.mod` e `dockerfile`);
* um runtime compatível com Compose, como Docker Compose ou Podman Compose;
* a CLI `migrate` para os comandos do `Makefile`;
* `curl` e `jq` para os exemplos abaixo.

Docker não é requisito do protocolo: use o provider Compose compatível já
instalado no ambiente. Por exemplo, substitua `docker compose` abaixo por
`podman compose` quando esse for o provider disponível. Um teste condicional
em `SKIPPED` não deve ser tratado como evidência de que uma dependência está
funcionando.

## Bootstrap local

Copie a configuração local segura e inicie a infraestrutura:

```bash
cp .env.example .env
docker compose up -d postgres localstack keycloak
```

O bootstrap do Compose importa o realm `wagering` do Keycloak e cria:

* `wager-transactions.fifo`;
* `wager-transactions-dlq.fifo`, com a redrive policy da fila principal;
* `wager-events.fifo`.

Para executar migrations a partir do host, use uma URL de PostgreSQL acessível
do host em vez do hostname da rede do Compose presente em `.env.example`:

```bash
export DATABASE_URL='postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
make migrate-up
```

Aplique as migrations antes de iniciar a aplicação contra um banco vazio. As
migrations versionadas podem ser revertidas, uma etapa controlada por vez:

```bash
make migrate-down
make migrate-up
```

`000003_opening_transaction.down.sql` recusa intencionalmente remover o
suporte a `OPENING` enquanto existirem transações de abertura, pois isso
apagaria sua origem financeira. Faça qualquer rollback de produção por meio de
um procedimento explícito de manutenção; não apague dados financeiros para
fazer uma migration reverter.

Depois de aplicar as migrations, inicie a stack completa:

```bash
docker compose up --build -d app
curl -fsS http://localhost:8080/health/ready
```

A readiness esperada é PostgreSQL e SQS ambos `UP`. Liveness e readiness são
endpoints distintos; metrics não substituem nenhum dos dois health checks.

## Identidades OIDC locais e chamadas autenticadas

A importação do realm é somente para desenvolvimento local. Ela provisiona as
seguintes identidades de teste, todas com a senha `dev-only-pass`:

| Identidade | Role | Claim do provider |
| --- | --- | --- |
| `wallet-admin` | `internal` | none |
| `provider-alpha` | `provider` | `provider-alpha` |
| `provider-beta` | `provider` | `provider-beta` |

O client é `wagering-api`, com o secret de exemplo `change-me`. Esses são
valores locais seguros de `.env.example`, não credenciais de deployment.

Obtenha tokens locais sem imprimi-los:

```bash
TOKEN_URL='http://localhost:8082/realms/wagering/protocol/openid-connect/token'
token() {
  curl -fsS -X POST "$TOKEN_URL" \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode 'grant_type=password' \
    --data-urlencode 'client_id=wagering-api' \
    --data-urlencode 'client_secret=change-me' \
    --data-urlencode "username=$1" \
    --data-urlencode 'password=dev-only-pass' | jq -er '.access_token'
}
INTERNAL_TOKEN="$(token wallet-admin)"
PROVIDER_TOKEN="$(token provider-alpha)"
```

Abra uma wallet usando a identidade interna:

```bash
curl -fsS -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"playerId":"player-001","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

Use o `id` retornado como `WALLET_ID` e envie uma operação autenticada de
provider. O provider no request body deve corresponder ao claim do token.

```bash
curl -fsS -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" \
  -H 'Idempotency-Key: provider-alpha:bet-001' \
  -H 'Content-Type: application/json' \
  --data '{"providerId":"provider-alpha","externalTransactionId":"bet-001","playerId":"player-001","walletId":"'"$WALLET_ID"'","roundId":"round-001","gameId":"game-001","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}'
```

Repita o mesmo request com o mesmo `Idempotency-Key` para receber o resultado
persistido com `idempotentReplay: true`; nenhum segundo movimento será
aplicado. As business routes exigem um token real do Keycloak. A abertura de
wallet é somente interna, enquanto as operações com escopo de provider são
restritas ao provider autenticado.

## Operações e diagnósticos

```bash
curl -fsS http://localhost:8080/health/live
curl -fsS http://localhost:8080/health/ready
curl -fsS http://localhost:8080/metrics
```

`X-Correlation-ID` é gerado quando ausente. Um valor externo só é preservado
quando é um identificador ASCII seguro e limitado; uma entrada inválida é
substituída. Os logs da aplicação são registros JSON com os identificadores
de correlação `messageId`, `transactionId`, `walletId` e `providerId` disponíveis. Logs e
labels de metrics excluem credenciais, payloads financeiros completos e IDs
de negócio. As metrics são process-local e zeradas no restart; são
diagnósticas, não um banco financeiro cluster-wide. A métrica
`wager_sqs_redrive_candidate_total` conta uma mensagem que esgotou o orçamento
de receives da aplicação, não uma entrada na DLQ observada pelo broker.

## Verificação

Os Quality Gates padrão são:

```bash
gofmt -l .
go test ./...
go test -race -count=1 ./...
go vet ./...
git diff --check
```

Para a matriz de integração real a partir do host, inicie as dependências do
Compose e use explicitamente os endpoints do host. Os testes condicionais
devem ser habilitados de forma deliberada:

```bash
DATABASE_URL='postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable' \
AWS_ENDPOINT_URL='http://localhost:4566' \
AWS_REGION='us-east-1' AWS_ACCESS_KEY_ID='test' AWS_SECRET_ACCESS_KEY='test' \
OIDC_ISSUER='http://localhost:8082/realms/wagering' \
OIDC_JWKS_URL='http://localhost:8082/realms/wagering/protocol/openid-connect/certs' \
OAUTH_AUDIENCE='wagering-api' OAUTH_CLIENT_ID='wagering-api' \
OAUTH_CLIENT_SECRET='change-me' \
KEYCLOAK_TOKEN_URL='http://localhost:8082/realms/wagering/protocol/openid-connect/token' \
KEYCLOAK_TEST_USERNAME='provider-alpha' KEYCLOAK_TEST_PASSWORD='dev-only-pass' \
RUN_KEYCLOAK_INTEGRATION=1 RUN_OUTBOX_PRODUCTION_INTEGRATION=1 \
RUN_OUTBOX_FX_INTEGRATION=1 \
go test -count=1 -v \
  ./internal/application/financial ./internal/application/query \
  ./internal/application/outbox ./internal/composition \
  ./internal/infrastructure/keycloak ./internal/infrastructure/postgres \
  ./internal/transport/http ./internal/transport/messaging
```

Esse comando exercita transações e constraints do PostgreSQL, verificação do
Keycloak, SQS FIFO/DLQ, equivalência HTTP/SQS, recovery de inbox/outbox,
pending references, múltiplos pools/processos, lifecycle do Fx e os cenários
controlados de falha. As integrações de ordering do Loop 9 usam schemas
isolados do PostgreSQL e filas FIFO exclusivas, portanto a execução agregada
não depende de rows ou mensagens não relacionadas.

Exemplos focados para os cenários adversariais exigidos:

```bash
DATABASE_URL="$DATABASE_URL" go test -count=1 -run 'TestConcurrentBetsAcrossThreeOSProcesses|TestPersistentIdempotencyAcrossReplayRestartAndInstances' -v ./internal/application/financial
DATABASE_URL="$DATABASE_URL" AWS_ENDPOINT_URL='http://localhost:4566' go test -count=1 -run 'TestLoop11' -v ./internal/application/financial ./internal/application/outbox ./internal/transport/messaging
```

## Fronteiras de entrega

O outbox garante publicação at-least-once após o commit do banco. Um envio
ambíguo pode republicar o mesmo payload imutável com o mesmo `eventId` estável;
consumidores downstream devem deduplicá-lo. Os testes de falha controlada
usam PostgreSQL/LocalStack reais e `SIGKILL` de subprocessos em fronteiras
selecionadas. Eles não alegam durabilidade contra perda de energia do host ou
kill físico de container. O LocalStack valida o comportamento do broker, mas
não prova enforcement de AWS IAM; o IAM de deployment continua sendo uma
responsabilidade operacional. Nenhuma validação do Loop 12 introduz publisher
além do Loop 9 ou altera código financeiro de produção.

## Licença

Este projeto é fornecido exclusivamente para avaliação técnica, recrutamento,
entrevistas e portfólio.

Uso em produção, comercial, redistribuição e uso derivado não são autorizados
sem autorização prévia por escrito do titular dos direitos autorais. Consulte
[LICENSE](LICENSE).
