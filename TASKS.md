# Tarefas de implementação

Este arquivo registra o estado de trabalho da implementação.

O projeto segue um loop iterativo de engenharia:

```text
SPEC
  ↓
PLANEJAR
  ↓
IMPLEMENTAR
  ↓
VERIFICAR
  ↓
ENCONTRAR GAPS
  ↓
CORRIGIR
  ↓
DOCUMENTAR
  ↓
PRÓXIMO LOOP
```

---

# Loop 0 — Fundação do projeto

## Objetivo

Estabelecer a estrutura do repositório e o ambiente local de desenvolvimento.

### Tarefas

* [x] Criar estrutura do projeto
* [x] Inicializar módulo Go
* [x] Configurar versão do Go
* [x] Criar Compose
* [x] Configure PostgreSQL
* [x] Configure LocalStack
* [x] Configure Keycloak
* [x] Criar `.env.example`
* [x] Criar Makefile
* [x] Criar estrutura de migrations
* [x] Verificar startup limpo

### Verificação

* [x] `docker compose up --build`
* [x] PostgreSQL acessível
* [x] LocalStack acessível
* [x] Keycloak acessível
* [x] aplicação inicia
* [x] aplicação encerra de forma limpa

---

# Loop 1 — Fundação do domínio

## Objetivo

Implementar o comportamento financeiro exato do domínio sem dependências de infraestrutura.

### Tarefas

* [x] Money value object
* [x] Money parsing
* [x] Money serialization
* [x] Money arithmetic
* [x] Currency validation
* [x] Overflow protection
* [x] Aggregate Wallet
* [x] Criação de Wallet
* [x] Débito de Wallet
* [x] Crédito de Wallet
* [x] WagerTransaction
* [x] WalletLedgerEntry
* [x] Domain errors
* [x] máquina de estados da transação

### Verificação

* [x] Money unit tests
* [x] Testes das invariantes de Wallet
* [x] Testes das transições de transação
* [x] Testes das regras de operação
* [x] `go test ./...`

---

# Loop 2 — Fundação do banco de dados

## Objetivo

Persistir o modelo financeiro com invariantes impostas pelo banco de dados.

### Tarefas

* [x] tabela wallets
* [x] tabela wager_transactions
* [x] tabela wallet_ledger_entries
* [x] tabela inbox
* [x] tabela outbox
* [x] constraints
* [x] índices únicos
* [x] proteção do ledger imutável
* [x] migrations
* [x] rollback de migration
* [x] repositories pgx

### Verificação

* [x] migrations aplicadas
* [x] rollback de migrations
* [x] constraints de unicidade verificadas
* [x] mutação do ledger rejeitada
* [x] saldo inválido rejeitado
* [x] testes de integração passam

---

# Loop 3 — Processamento financeiro

## Objetivo

Implementar todas as operações financeiras de forma atômica.

### Tarefas

* [x] abertura de wallet
* [x] BET
* [x] WIN
* [x] LOSS
* [x] REFUND
* [x] ROLLBACK
* [x] rejeição por saldo insuficiente
* [x] validação de referência
* [x] proteção de reversão
* [x] saldo + transação + ledger atômicos

### Verificação

* [x] testes de integração financeira
* [x] reconstrução do ledger
* [x] reconciliation
* [x] regras de valor zero
* [x] testes de reversão

> `FAILED` permanece reservado para falha permanente de infraestrutura registrada;
> no Loop 3, falhas de infraestrutura abortam a transação. O registro/recovery
> desse estado depende dos loops de messaging e recovery.

---

# Loop 4 — Idempotência

## Objetivo

Garantir idempotência persistente entre instâncias e restarts.

### Tarefas

* [x] Idempotency-Key
* [x] payload canônico
* [x] hash do payload
* [x] detecção de duplicatas
* [x] mesma chave + mesmo payload
* [x] mesma chave + payload diferente
* [x] mesma transação + chave diferente
* [x] resultado original persistido
* [x] comportamento de replay

### Verificação

* [x] 50 concurrent duplicate requests
* [x] restart da aplicação
* [x] replay após restart
* [x] replay entre instâncias
* [x] testes de conflito

> Registros anteriores ao Loop 4 que não possuem hash canônico compatível e
> resultado persistido não são reprocessados; a tentativa é recusada com
> `ErrReplayUnavailable` para preservar a segurança financeira.

---

# Loop 5 — Concorrência

## Objetivo

Provar corretude entre processos independentes da aplicação.

### Tarefas

* [x] locking da linha da wallet
* [x] fronteiras de transação
* [x] proteção contra débitos concorrentes
* [x] prevenção de lost update
* [x] paralelismo entre wallets independentes

### Verificação

* [x] dois BETs de 80 BRL contra 100 BRL
* [x] exatamente um débito bem-sucedido
* [x] saldo final de 20 BRL
* [x] três instâncias independentes
* [x] wallets diferentes executam concorrentemente
* [x] `go test -race`

> A integração do Loop 5 inicia três executáveis independentes do binário de
> testes, cada um com memória e pool PostgreSQL próprios, e coordena somente
> uma barreira externa de início.

---

# Loop 6 — HTTP

## Objetivo

Expor os casos de uso financeiros por HTTP.

Status atual: COMPLETE — APPROVED

### Tarefas

* [x] HTTP server
* [x] middleware de autenticação
* [x] middleware de autorização
* [x] POST /wallets
* [x] GET /wallets/:walletId
* [x] GET /wallets/:walletId/ledger
* [x] POST /wagering/transactions
* [x] GET /wagering/transactions/:transactionId
* [x] GET /providers/:providerId/wagering/transactions/:externalTransactionId
* [x] POST /wallets/:walletId/reconciliation
* [x] GET /health/live
* [x] GET /health/ready
* [x] HTTP error contract

### Verificação

* [x] testes de autenticação
* [x] testes de isolamento de autorização
* [x] invalid input tests
* [x] replay tests
* [x] HTTP integration tests

### Conformidade pós-Loop 6 (antes do Loop 7)

Estes itens não marcados registram o trabalho de reconciliação do contrato com
a fonte primária. Eles não reabrem o checkpoint aprovado do Loop 6 e não estão
concluídos neste documento.

* [ ] POST-LOOP-6 HTTP CONTRACT CONFORMANCE
  * [x] reconciliar nomes de campos de request/response com o challenge primário
  * [x] confirmar que providerId é autoritativo a partir da identidade autenticada
  * [x] reconciliar o contrato de response de reconciliation
  * [x] ajustar readiness para cobrir PostgreSQL e SQS no Loop 7

Readiness agora inclui PostgreSQL e SQS por meio da composição do Loop 7 e foi
exercitada contra as dependências locais em execução. A imposição da policy
AWS/IAM continua sendo responsabilidade da verificação de deployment.

---

# Loop 7 — SQS e Inbox

## Objetivo

Processar operações financeiras por messaging at-least-once.

### Tarefas

* [x] fila FIFO
* [x] DLQ
* [x] configuração de redrive
* [x] consumer SQS
* [x] envelope da mensagem
* [x] validação da mensagem
* [x] inbox
* [x] inbox uniqueness
* [x] SQS → application command
* [x] retry
* [x] visibility timeout
* [x] shutdown gracioso do consumer
* [x] nomes das filas e contrato FIFO/DLQ
* [x] envelope da mensagem e campos de dados do comando
* [x] identidade por messageId e hash do payload
* [x] fronteira transacional e unicidade da inbox
* [x] delete somente após commit
* [x] acknowledgement de rejeição de negócio
* [x] retry e backoff transitórios
* [x] tratamento de mensagens malformed
* [x] DLQ e limites de attempts
* [x] SIGTERM interrompe polling e finaliza/libera trabalho em andamento
* [x] policy de MessageGroupId e MessageDeduplicationId
* [x] equivalência do application use case HTTP/SQS
* [x] configuração das credenciais do broker
* [x] policy mínima de autorização do broker documentada
* [ ] verificação da imposição da policy de broker AWS/IAM (responsabilidade de deployment; LocalStack não a comprova)
* [x] readiness de PostgreSQL e SQS
* [x] execução de integração real com PostgreSQL + LocalStack

### Verificação

* [x] mensagem duplicada
* [x] redelivery da mensagem
* [x] commit antes de delete
* [x] acknowledgement de rejeição de negócio
* [x] retry de falha transitória
* [x] comportamento de DLQ com execução de broker real
* [x] recovery após restart
* [x] comportamento de visibility e mensagem malformed
* [x] equivalência HTTP/SQS
* [x] consumer Fx sobrevive à conclusão do contexto de `OnStart` e interrompe o polling
* [x] readiness real: PostgreSQL/SQS UP, cada dependência DOWN e recovery
* [x] entrega duplicada por dois consumers SQS reais e pools PostgreSQL separados

> Testes condicionais continuam condicionais no gate local padrão, mas um teste
> skipped não é evidência de integração. O Integration Evidence Gate em
> `LOOPING.md` exige inspeção dos runtimes compatíveis instalados e execução
> real explícita de PostgreSQL/LocalStack antes que este checkpoint possa ser
> marcado como verificado. A rodada de correção do Loop 7 usou o ambiente
> instalado de Podman e podman-compose; os testes reais cobrem topologia
> FIFO/redrive, processamento do consumer, replay da inbox durável, redelivery
> após falha de delete, DLQ, lifecycle do Fx e recovery da readiness
> PostgreSQL/SQS. LocalStack não verifica a imposição da policy AWS IAM.

---

# Loop 8 — Referências pendentes

## Objetivo

Recuperar reversões cuja transação referenciada chega posteriormente.

### Tarefas

* [x] PENDING_REFERENCE
* [x] worker de referência
* [x] backoff exponencial
* [x] limite de retry / TTL
* [x] resolução de referência
* [x] rejeição por expiração da referência
* [x] backoff exponencial reiniciável
* [x] failureCode estável de referência não encontrada
* [x] comportamento de referência pendente/sem sucesso

### Verificação

* [x] reversão antes da referência
* [x] referência chega posteriormente
* [x] restart da aplicação enquanto pendente
* [x] exhaustion de retry
* [x] evento de referência pendente
* [x] race de referência no exhaustion com pools PostgreSQL independentes
* [x] múltiplos workers de referências pendentes com pools PostgreSQL independentes

> O Loop 8 usa como padrão dez attempts com backoff exponencial de um segundo.
> `REFERENCE_REQUIRED` rejeita imediatamente uma referência vazia; uma
> referência ausente e não vazia torna-se `PENDING_REFERENCE`. Referências
> pendentes sobrevivem ao restart por meio do PostgreSQL. O Loop 9 continua
> responsável por publicar as outbox rows transacionais resultantes.

---

# Loop 9 — Outbox transacional

## Objetivo

Garantir publicação durável de eventos após o commit financeiro.

### Tarefas

* [x] modelo de outbox
* [x] construtores de eventos
* [x] payload imutável de evento
* [x] event IDs
* [x] worker de outbox
* [x] claiming de records
* [x] múltiplos publishers
* [x] retries
* [x] backoff
* [x] recovery de trabalho abandonado
* [x] destino do evento
* [x] criação atômica do evento com o estado financeiro
* [x] triggers de evento obrigatórios e snapshots imutáveis
* [x] event IDs estáveis entre republicações
* [x] janelas de falha de publicação/confirmação
* [x] ordenação durável de publicação por aggregate

### Verificação

* [x] outbox recovery
* [x] dois publishers competem
* [x] retry de publicação
* [x] publicação duplicada mantém o event ID
* [x] todos os eventos obrigatórios verificados
* [x] ordenação no mesmo aggregate e paralelismo entre aggregates independentes

> O Loop 9 publica envelopes de evento versionados e imutáveis no destino FIFO
> `wager-events.fifo`. Os claims no PostgreSQL usam lease e claim token
> transaction-scoped; claims abandonados podem ser recuperados por outro worker.
> A ambiguidade de publicação pode resultar em entregas repetidas com o mesmo
> eventId. A injeção de process-crash é verificada pelos ataques controlados de
> processos-filhos documentados no Loop 11.

---

# Loop 10 — Observabilidade

## Objetivo

Tornar o processamento distribuído diagnosticável.

### Tarefas

* [x] logging JSON
* [x] correlation ID
* [x] transaction ID
* [x] wallet ID
* [x] provider ID
* [x] message ID
* [x] métricas de processamento
* [x] métricas de duplicatas
* [x] métricas de retry
* [x] métricas de receive-exhaustion/redrive-candidate (a inserção na DLQ do
  broker não é observada pela aplicação)
* [x] outbox lag
* [x] divergência de reconciliation

### Verificação

* [x] logs contêm os identificadores obrigatórios
* [x] dados sensíveis não são registrados
* [x] métricas expostas
* [x] health checks verificados
* [x] caminhos de métricas HTTP/SQS/pending-reference/outbox exercitados sem
  incrementos diretos no collector
* [x] segurança da entrada de correlation e logging operacional JSON-only do Fx verificados
* [x] receive-exhaustion documentado como redrive candidate, não como inserção de DLQ confirmada
* [x] duplicatas de idempotência são distintas de redelivery da inbox SQS e
  conflitos sequenciais de idempotência são distintos de conflitos de concorrência

> As integrações direcionadas de observabilidade do Loop 10 foram executadas
> com PostgreSQL, LocalStack, Keycloak e Fx reais. As integrações de ordenação
> do Loop 9 usam schemas PostgreSQL e filas FIFO isolados, para que pacotes
> concorrentes não introduzam outbox rows não relacionadas nem consumam as
> mensagens do cenário; o comando agregado de integração real passa com essa
> correção do test harness.

---

# Loop 11 — Failure Engineering

## Objetivo

Atacar a implementação e encontrar gaps de corretude.

### Cenários

* [x] duplicate HTTP
* [x] duplicate SQS
* [x] mesma operação HTTP + SQS
* [x] escritas concorrentes na wallet
* [x] process-crash antes do commit
* [x] process-crash após o commit
* [x] crash do consumer antes do delete SQS
* [x] crash do publisher de outbox
* [x] PostgreSQL temporary outage
* [x] SQS temporary outage
* [x] referência pendente durante restart
* [x] múltiplas instâncias
* [x] replay após restart
* [x] 50 requests duplicadas produzem um movimento financeiro
* [x] cenário de wallet concorrente 100/80/80
* [x] três processos independentes da aplicação
* [x] crash do consumer após o commit e antes do delete
* [x] dois publishers de outbox
* [x] cenários de reversão/referência tardias
* [x] mesma operação HTTP/SQS

### Verificação

Para cada falha encontrada:

```text
Falha
  ↓
Reprodução
  ↓
Causa-raiz
  ↓
Correção
  ↓
Teste de regressão
  ↓
Documentação
```

> O Loop 11 adiciona testes controlados de process-crash usando processos-filhos
> de teste isolados. Uma falha do PostgreSQL e um processo encerrado antes do
> commit fazem rollback das escritas de wallet, transação, ledger e outbox. Um
> consumer encerrado após a conclusão durável da inbox, mas antes do delete SQS,
> faz redelivery com segurança; um publisher encerrado após um send SQS
> bem-sucedido, mas antes de `MarkPublished`, recupera o mesmo evento durável de
> outbox após sua lease. Falhas temporárias das dependências PostgreSQL e SQS
> são injetadas em suas fronteiras de client/SQL e seguidas de recovery real
> contra PostgreSQL e LocalStack. Os testes não alegam simulação de perda de
> energia do host ou container.

---

# Loop 12 — Gate final de qualidade

### Código

* [x] `gofmt`
* [x] `go test ./...`
* [x] `go test -race ./...`
* [x] `go vet ./...`

### Infraestrutura

* [x] startup limpo do Compose
* [x] migrations reproduzíveis
* [x] provisionamento do Keycloak reproduzível
* [x] filas reproduzíveis
* [x] identidades de teste reproduzíveis

### Documentação

* [x] README completo
* [x] ARCHITECTURE completo
* [x] `.env.example` completo
* [x] limitações documentadas
* [x] decisões arquiteturais documentadas

### Verificação final

* [x] checkout limpo
* [x] suíte completa de testes
* [x] cenário de concorrência
* [x] cenário de duplicata
* [x] cenário de recovery
* [x] reconciliation
* [x] equivalência HTTP/SQS
* [x] integração real de PostgreSQL, SQS e IdP
* [x] migrations up/down
* [x] startup limpo do Compose
* [x] exemplos autenticados e identidades de teste
* [x] simulações multi-instância e de falha
* [x] gates de gofmt, testes, race e vet

---

# Loop atual

```text
Loop 12 — Gate final de qualidade
```

# Status atual

```text
IMPLEMENTED — PENDING HUMAN REVIEW
```

# Regras

Não marque uma tarefa como concluída apenas porque o código existe.

Uma tarefa só está concluída depois que sua verificação relevante é bem-sucedida.

Quando uma verificação revelar um defeito:

1. mantenha a tarefa aberta;
2. documente a falha;
3. corrija a causa-raiz;
4. adicione cobertura de regressão;
5. execute novamente a verificação;
6. então marque a tarefa como concluída.
