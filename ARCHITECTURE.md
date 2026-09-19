# Arquitetura

## 1. Visão geral

Este projeto implementa um processador distribuído de transações de apostas em Go.

A aplicação é projetada como um monólito modular que pode executar como múltiplas instâncias independentes.

O principal objetivo arquitetural é a correção financeira sob:

* processamento concorrente;
* entrega duplicada;
* interrupção de processo;
* falhas de banco de dados;
* redelivery do message broker;
* publicação assíncrona de eventos.

O sistema separa:

```text
Domain
Application
Infrastructure
Transport
Composition
```

O domínio não depende de HTTP, SQS, PostgreSQL, Keycloak ou Uber Fx.

---

# 2. Objetivos arquiteturais

A arquitetura prioriza:

1. Correção financeira.
2. Idempotência persistente.
3. Invariantes impostas pelo banco de dados.
4. Concorrência segura entre processos independentes.
5. Alterações atômicas do estado financeiro.
6. Processamento assíncrono durável.
7. Recuperabilidade após falhas.
8. Separação clara entre domínio e infraestrutura.
9. Reprodutibilidade em um ambiente Docker local.

A arquitetura evita intencionalmente serviços distribuídos desnecessários.

Múltiplas instâncias da aplicação fornecem o modelo de execução distribuída exigido pelo desafio.

---

# 3. Arquitetura de alto nível

```text
                         ┌──────────────────┐
                         │     Keycloak     │
                         │     OIDC IdP     │
                         └────────┬─────────┘
                                  │
                                  ▼
┌─────────────┐            ┌───────────────┐
│ HTTP Client │───────────►│ HTTP Adapter  │
└─────────────┘            └───────┬───────┘
                                   │
                                   ▼
                            ┌──────────────┐
                            │ Application   │
                            │ Use Cases     │
                            └──────┬───────┘
                                   │
                     ┌─────────────┼─────────────┐
                     │             │             │
                     ▼             ▼             ▼
                 PostgreSQL      Domain       Outbox
                     ▲                           │
                     │                           ▼
                     │                      Outbox Worker
                     │                           │
                     │                           ▼
                     │                          SQS
                     │                           ▲
                     │                           │
                     │                      SQS Consumer
                     │                           │
                     └──────── Inbox ────────────┘
```

HTTP e SQS convergem para o mesmo use case de processamento financeiro no nível da aplicação.

Adapters de transporte não duplicam regras de negócio financeiro.

---

# 4. Estrutura de packages

Estrutura proposta:

```text
cmd/
  api/

internal/
  domain/
    money/
    wallet/
    wager/
    ledger/

  application/
    wallet/
    wager/

  infrastructure/
    postgres/
    sqs/
    keycloak/
    config/

  transport/
    http/
    messaging/

  fx/

migrations/

tests/
  integration/
  concurrency/
  recovery/
```

A organização exata dos packages pode evoluir se a evidência da implementação demonstrar uma fronteira melhor.

As mudanças devem preservar a independência do domínio.

---

# 5. Camada de domínio

O domínio contém:

* entities;
* value objects;
* erros de domínio;
* transições de estado;
* invariantes financeiras;
* regras de negócio.

O domínio não deve importar:

* Uber Fx;
* packages HTTP;
* SDK do SQS;
* drivers do PostgreSQL;
* bibliotecas do Keycloak.

## Objetos de domínio

Conceitos primários do domínio:

```text
Money
Wallet
WagerTransaction
WalletLedgerEntry
```

A criação e a rehydration devem ser conceitos distintos.

Reidratar estado persistido não deve reaplicar operações financeiras nem emitir eventos.

---

# 6. Representação de Money

## Decisão

Use `int64` representando a menor unidade monetária.

Para o desafio atual:

```text
1 BRL = 100 cents
```

Example:

```text
"25.00" BRL -> 2500
```

O tipo de domínio ainda carrega currency.

A serialização externa permanece:

```json
{
  "amount": "25.00",
  "currency": "BRL"
}
```

## Justificativa

`int64` fornece aritmética exata e determinística para valores monetários fixos com duas casas decimais, sem erros de ponto flutuante.

Também evita introduzir uma biblioteca decimal, a menos que requisitos futuros exijam precisão além da escala fixa do desafio.

## Proteções obrigatórias

Parsing, adição, subtração e negação devem detectar overflow.

---

# 7. Persistência

## Decisão

Use PostgreSQL com `pgx` e SQL explícito.

## Justificativa

O desafio exige explicitamente que invariantes financeiras, transações, locks e constraints permaneçam visíveis e verificáveis.

SQL explícito torna fáceis de inspecionar:

* fronteiras transacionais;
* row locks;
* uniqueness constraints;
* check constraints;
* condições de update

As abstrações de repository devem permanecer focadas em operações de persistência, em vez de esconder semânticas transacionais importantes.

---

# 8. Fronteira da transação financeira

Uma operação financeira deve fazer commit atomicamente.

Para uma operação normal bem-sucedida, a transação do banco pode conter:

```text
BEGIN

lock da wallet
        ↓
validação da transação
        ↓
alteração do saldo da wallet
        ↓
criação do estado da wager transaction
        ↓
criação da entrada do ledger
        ↓
criação dos eventos de outbox

COMMIT
```

Nenhum evento externo é publicado antes de `COMMIT`.

A fronteira transacional deve ser explícita no código.

---

# 9. Estratégia de concorrência

## Decisão

Use row-level locking do PostgreSQL no escopo da wallet.

O mecanismo primário esperado é:

```sql
SELECT ...
FROM wallets
WHERE id = $1
FOR UPDATE;
```

O lock é adquirido dentro da mesma transação PostgreSQL que realiza a mutação financeira.

## Justificativa

A wallet é a raiz do aggregate financeiro e, portanto, a fronteira natural de coordenação.

O row-level locking:

* coordena processos independentes da aplicação;
* não depende da memória local do Go;
* evita lost updates;
* serializa operações para a mesma wallet;
* permite que wallets diferentes progridam concorrentemente.

Locks globais da aplicação são proibidos.

---

# 10. Invariante do saldo da Wallet

Um débito é permitido somente quando:

```text
balance >= debit
```

O banco deve participar da proteção dessa invariante.

A validação somente na aplicação é insuficiente porque múltiplos processos independentes podem executar concorrentemente.

A implementação deve usar ambos:

```text
validação da aplicação/domínio
+
proteção por transação/constraint do banco
```

para impedir saldos negativos.

---

# 11. Estratégia de idempotência

A idempotência é persistente.

O banco é a fonte de verdade.

A implementação deve persistir:

* chave de idempotência;
* identidade de negócio;
* hash do payload;
* estado do processamento;
* informações do resultado persistido.

A unicidade do banco deve proteger:

```text
(providerId, externalTransactionId)
```

e a identidade de idempotência aplicável.

O sistema deve distinguir:

```text
mesma chave + mesmo payload
mesma chave + payload diferente
mesma identidade de transação + chave diferente
```

Um replay deve retornar o resultado persistido original.

O saldo original retornado por um replay é o saldo observado quando a transação foi processada, não o saldo atual da wallet.

---

# 12. Hashing do payload

Use JSON canônico para hashing determinístico do payload.

Campos de negócio são incluídos.

Metadados específicos do transporte são excluídos.

A chave de idempotência é excluída.

A mesma implementação/regras de canonicalização deve ser compartilhada entre HTTP e SQS.

O algoritmo deve ser documentado e coberto por testes.

Decisão arquitetural da implementação atual, não um requisito atribuído a
CHALLENGE.md: a representação canônica é o JSON produzido a partir dos
campos de negócio ordenados `externalTransactionId`, `providerId`, `walletId`,
`playerId`, `gameId`, `roundId`, `referenceExternalTransactionId` (quando
presente), `kind` e o valor exato `{amount,currency}` de `money`. O ID interno
do request, a chave de idempotência, o hash fornecido pelo caller e os
metadados de transporte são excluídos. O hash do payload é o digest SHA-256
hexadecimal em minúsculas desse JSON canônico. Os aliases legados da
implementação estão registrados no conflict register do ADR-006 e não são
normativos.

---

# 13. Ledger

O ledger é append-only.

Uma entrada do ledger nunca é atualizada ou apagada.

As proteções do banco devem impedir mutações.

A relação de unicidade:

```text
(walletId, transactionId)
```

impede entradas financeiras duplicadas no ledger.

O saldo da wallet e a entrada do ledger fazem commit na mesma transação do banco.

O ledger é a trilha de auditoria autoritativa usada pela reconciliation.

---

# 14. Máquina de estados da transação

```text
             ┌─────────────────────┐
             │       PENDING       │
             └──────────┬──────────┘
                        │
                processamento necessário
                        │
             ┌──────────▼──────────┐
             │     PROCESSED       │
             └─────────────────────┘

             ┌─────────────────────┐
             │       PENDING       │
             └──────────┬──────────┘
                        │
                 referência ausente
                        │
             ┌──────────▼──────────┐
             │ PENDING_REFERENCE   │
             └──────────┬──────────┘
                        │
                 referência resolvida
                        │
                        ▼
processamento
                        │
                ┌───────┴────────┐
                ▼                ▼
           PROCESSED          REJECTED


PENDING / falha de processamento
        │
        ▼
FAILED
```

Estados terminais:

```text
PROCESSED
REJECTED
FAILED
```

Transações terminais não podem fazer nova transição.

---

# 15. Reversões

Reversões são resolvidas por meio de:

```text
(providerId, referenceExternalTransactionId)
```

A resolução da referência deve validar:

* provider;
* player;
* wallet;
* currency;
* round;
* estado da transação original;
* tipo da transação original;
* valor da reversão.

Reversões duplicadas são impedidas por constraints do banco e validação da aplicação.

---

# 16. Referências pendentes

Uma referência ausente não é tratada como falha de infraestrutura.

A transação é persistida de forma durável como:

```text
PENDING_REFERENCE
```

Um worker tenta novamente referências pendentes periodicamente.

O worker usa backoff exponencial e um limite de retry ou TTL configurável.

O trabalho pendente sobrevive a restarts da aplicação.

---

# 17. Padrão Inbox

O consumer SQS usa um inbox durável.

O inbox fornece deduplicação no nível da aplicação além de qualquer deduplicação FIFO do SQS.

Identidade:

```text
(consumerName, messageId)
```

O registro do inbox e o processamento financeiro devem compartilhar a mesma transação do banco quando a mensagem realizar uma operação financeira.

Uma mensagem é apagada do SQS somente depois do commit da transação durável.

---

# 18. Outbox transacional

Eventos de integração são persistidos na mesma transação PostgreSQL que as alterações de estado que os causaram.

O outbox fecha, portanto, a janela de falha entre:

```text
commit do banco de dados
```

e:

```text
publicação do evento
```

Um worker publica eventos pendentes de forma assíncrona.

Múltiplos workers podem operar concorrentemente.

Workers devem fazer claim seguro dos registros para que dois workers não sejam simultaneamente donos da mesma tentativa de publicação.

A publicação pode ser repetida após uma falha ambígua.

Portanto:

```text
eventId
```

deve permanecer estável entre retries e republicações.

Espera-se que os consumers usem event IDs para sua própria deduplicação.

---

# 19. Modelo de eventos

Eventos obrigatórios:

```text
WagerTransactionProcessed
WagerTransactionRejected
WalletBalanceChanged
WagerTransactionPendingReference
```

Eventos contêm:

```text
eventId
eventType
aggregateId
correlationId
causationId (opcional)
occurredAt
version
data
```

Payloads de eventos são snapshots imutáveis.

Money é serializado como strings decimais.

Timestamps usam UTC RFC 3339.

---

# 20. Equivalência HTTP / SQS

HTTP e SQS são mecanismos de transporte diferentes para o mesmo comando financeiro.

O fluxo é:

```text
HTTP
  ↓
HTTP adapter
  ↓
Comando da aplicação
  ↓
Use case financeiro
```

e:

```text
SQS
  ↓
Consumer
  ↓
Comando da aplicação
  ↓
Use case financeiro
```

Regras de negócio não devem ser implementadas de forma independente nos dois caminhos.

Isso garante comportamento financeiro equivalente independentemente do transporte.

---

# 21. Autenticação

Use Keycloak como provider OAuth 2.0/OIDC local.

A aplicação valida tokens emitidos pelo IdP configurado.

A identidade autenticada determina o provider autorizado.

A autorização do provider deve ser imposta antes do acesso a dados de transações com escopo de provider.

A aplicação não:

* armazena senhas de usuários;
* emite seus próprios tokens de autenticação.

---

# 22. Autorização

Operações com escopo de provider devem sempre impor:

```text
authenticated provider == requested provider
```

Isso se aplica a:

* criação de transação;
* recuperação de transação;
* replay de transação;
* lookup de transação externa.

Operações internas de wallet não são operações de provider.

Falhas de autorização não devem causar efeitos financeiros.

---

# 23. Uber Fx

Uber Fx é responsável pela composição e pelo lifecycle da aplicação.

Grafo de dependências esperado:

```text
Configuration
    ↓
Database / SQS / IdP clients
    ↓
Repositories
    ↓
Application services
    ↓
HTTP handlers / SQS consumers / workers
```

Use:

* `fx.Module`;
* `fx.Provide`;
* `fx.Invoke`;
* `fx.Lifecycle`.

O domínio permanece sem conhecimento de Fx.

---

# 24. Lifecycle e shutdown

O shutdown da aplicação segue:

```text
SIGTERM
  ↓
parar de aceitar trabalho novo
  ↓
parar o polling de mensagens
  ↓
finalizar ou liberar trabalho em andamento
  ↓
parar workers em background
  ↓
fechar dependências
  ↓
exit
```

Cancelamento e deadlines devem propagar por `context.Context`.

Workers não devem ser abandonados sem um mecanismo seguro de recovery.

---

# 25. Health Checks

## Liveness

```http
GET /health/live
```

Indica que o processo está vivo.

## Readiness

```http
GET /health/ready
```

Verifica as dependências obrigatórias, principalmente:

* PostgreSQL;
* SQS.

Uma indisponibilidade de dependência deve fazer a readiness falhar sem necessariamente encerrar o processo.

---

# 26. Observabilidade

Use logging JSON estruturado.

Identificadores importantes de correlação:

```text
correlationId
messageId
transactionId
walletId
providerId
```

Logs não devem conter payloads financeiros sensíveis completos ou credenciais.

Metrics incluem:

```text
status da transação
duplicatas de idempotência
contagem de retry
contagem de DLQ
conflitos de concorrência
atraso do outbox
latência de processamento
divergência de reconciliation
```

---

# 27. Estratégia de testes

Os testes são organizados em:

```text
unitários
integração
concorrência
recovery
```

Testes unitários verificam o comportamento do domínio.

Testes de integração verificam o comportamento da infraestrutura real.

Testes de concorrência verificam múltiplos processos independentes.

Testes de recovery verificam o comportamento de crash/restart.

O objetivo é provar as garantias do sistema, e não apenas aumentar a cobertura de código.

---

# 28. Verificação distribuída

A implementação deve ser exercitada com pelo menos três processos independentes da aplicação.

Independente significa:

* processo separado;
* memória Go separada;
* pool de conexões do banco separado.

Nenhuma garantia de correção pode depender de memória process-local.

---

# 29. Modelo de falhas

O sistema assume:

```text
at-least-once delivery
```

Portanto, processamento duplicado é esperado.

A arquitetura deve tolerar:

```text
request HTTP duplicado
mensagem SQS duplicada
crash de processo antes do commit
crash de processo depois do commit
crash de processo depois da publicação do evento
indisponibilidade temporária do banco
indisponibilidade temporária do SQS
chegada tardia da referência
```

O design trata retries e duplicatas como condições operacionais normais.

---

# 30. Infraestrutura local

Docker Compose fornece:

```text
PostgreSQL
LocalStack
Keycloak
dependências da aplicação
```

As versões exatas são fixadas ou documentadas explicitamente.

A configuração de infraestrutura deve ser reproduzível a partir de um checkout limpo.

---

# 31. Segurança

Secrets são fornecidos por variáveis de ambiente.

Secrets reais nunca devem ser commitados.

`.env.example` contém apenas valores locais seguros de exemplo.

A autorização do provider deve ser imposta na fronteira da aplicação.

Logs devem evitar credenciais e payloads sensíveis.

---

# 32. Trade-offs arquiteturais

A solução favorece intencionalmente:

```text
explicit SQL
+
transações de banco de dados
+
row-level locks
+
persistent idempotency
+
inbox/outbox
```

em vez de mecanismos de coordenação mais abstratos ou distribuídos.

O desafio é avaliado principalmente por correção e recuperabilidade.

Complexidade só deve ser introduzida quando proteger um requisito documentado.

---

# 33. Limitações conhecidas

Esta seção deve ser atualizada durante a implementação.

Toda limitação deve declarar:

* o que não está implementado;
* por quê;
* impacto;
* possível melhoria futura.

Não esconda requisitos incompletos.

---

# 34. Registro de decisões arquiteturais

Alterações arquiteturais descobertas durante a implementação devem ser registradas aqui.

Formato:

```text
## ADR-NNN — Título

Status:
Data:

Contexto:

Decisão:

Alternativas:

Consequências:
```

## ADR-006 — decisões de integração HTTP e OIDC do Loop 6

Status: APPROVED FOR LOOP 6 IMPLEMENTATION; PROVENANCE RECONCILED
Data: 2026-09-17

Contexto:

O Loop 6 precisa de decisões sobre transporte, identidade, lifecycle e
read-model antes que sua implementação possa ser revisada. A SPEC normativa
foi restaurada antes de a fonte primária completa do desafio estar disponível.
O `CHALLENGE.md` recuperado mostra agora que várias escolhas a seguir já eram
explícitas ou parcialmente explícitas na fonte primária, enquanto outros
valores foram selecionados posteriormente para a implementação do Loop 6.

Decisão:

As decisões a seguir são adotadas pela implementação atual. Sua proveniência
é registrada individualmente; nenhuma é apresentada retroativamente como um
requisito recuperado da SPEC anteriormente truncada.

* **EXPLÍCITO NO CHALLENGE:** HTTP usa JSON. Money é representado no wire como
  `{ "amount": "25.00", "currency": "BRL" }`.
* **DECISÃO HUMANA:** erros HTTP usam
  `{ "error": { "code": "...", "message": "..." } }`.
  `REJECTED` e `PENDING_REFERENCE` são resultados HTTP 200; a criação de
  wallet retorna 201 e uma wallet duplicada retorna 409 com
  `WALLET_ALREADY_EXISTS`.
* **EXPLÍCITO NO CHALLENGE:** requests externos de apostas usam o
  header `Idempotency-Key`. O hashing canônico do payload exclui a chave e os
  metadados de transporte.
* **PARCIALMENTE EXPLÍCITO:** `idempotentReplay` é retornado para resultados
  de POST de apostas repetidos; sua inferência exata de implementação é uma
  escolha do Loop 6.
* **PARCIALMENTE EXPLÍCITO:** a identidade do provider é autenticada e o
  isolamento de provider é obrigatório. O mapeamento concreto do claim
  `provider_id` é uma escolha posterior de implementação.
* **PARCIALMENTE EXPLÍCITO:** autorização de provider e interna é obrigatória
  e health é público. Os nomes concretos de role `provider` e `internal`, a
  audience `wagering-api` e a matriz por rota foram selecionados para o Loop 6.
* **PARCIALMENTE EXPLÍCITO:** leituras do ledger usam keyset pagination
  ordenada por `(timestamp, id)`. Default 50, máximo 100, cursor opaco e
  `limit+1` são escolhas do Loop 6 que implementam esse requisito; OFFSET não
  é usado.
* **EXPLÍCITO NO CHALLENGE:** health checks são públicos, liveness é
  obrigatória e readiness cobre PostgreSQL e SQS. **DECISÃO HUMANA:** a
  liveness do Loop 6 não faz check de dependências e sua parte de readiness
  SQS foi adiada para o Loop 7; essa não é a arquitetura final de readiness.
* **EXPLÍCITO NO CHALLENGE:** um IdP OAuth 2.0/OIDC externo, identidade
  autenticada, isolamento de provider e autorização adequada são obrigatórios.
  **DECISÃO HUMANA:** issuer, audience, política de validação de assinatura,
  política de `exp`/`nbf`, política de clock-skew, go-oidc, JWKS real, RS256 e
  a permissão para endpoints de rede distintos identificarem um realm são
  decisões concretas do adapter, não requisitos selecionados pela fonte
  primária.
* **DECISÃO HUMANA:** o shutdown HTTP usa um default configurável de dez
  segundos para drenar requests em andamento antes do fechamento das
  dependências.
* **EXPLÍCITO NO CHALLENGE:** a abertura interna de wallet exige o contrato
  documentado de saldo inicial; `0.00` cria uma wallet sem movimento
  financeiro. **PARCIALMENTE EXPLÍCITO:** reconciliation é read-only e sua
  resposta é definida; a fronteira de rota/autenticação interna é uma decisão
  do Loop 6.

A Proveniência:

A fonte primária recuperada é autoritativa para os itens marcados como
`EXPLÍCITO NO CHALLENGE` e para as partes explícitas dos itens marcados como
`PARCIALMENTE EXPLÍCITO`. Os valores restantes foram selecionados depois por
revisão humana para a implementação do Loop 6; eles não são requisitos
originalmente recuperados pela primeira restauração da SPEC. Os gaps abertos da
especificação relacionados permanecem preservados em SPEC.md nas partes
que continuam abertas. Este ADR registra as decisões de implementação
selecionadas e seu escopo para revisão.

Consequências:

Os adapters e a composição do Loop 6 podem ser revisados contra este ADR. Ele
não autoriza SQS, processamento de inbox ou workers de outbox, que permanecem
no escopo do Loop 7, e não altera o core financeiro congelado.

### Registro de conflitos da implementação do Loop 6

O desafio primário define os seguintes nomes e comportamentos HTTP contra os
quais a implementação atual deve ser reconciliada. Estes são registros de
conformance/proveniência, não novas decisões de implementação neste ADR:

* `externalTransactionId` versus o `externalId` da implementação;
* `kind` versus o `type` da implementação;
* `money` versus o `amount` da implementação;
* `status` versus o `state` da implementação;
* `initialBalance` versus o `openingBalance` da implementação;
* a identidade do provider deve ser autoritativa a partir da autenticação e
  não deve ser selecionada por um campo do request body;
* os campos da resposta de reconciliation devem seguir o contrato primário;
* readiness deve incluir PostgreSQL e SQS na arquitetura concluída, enquanto a
  implementação do Loop 6 atualmente cobre somente PostgreSQL.

Essas diferenças são trabalho de conformance para a mudança futura apropriada;
elas não autorizam alterar o core financeiro nem tratar o comportamento atual
do adapter como normativo.

## ADR-007 — política de consumer SQS e inbox do Loop 7

Status: IMPLEMENTED; REAL POSTGRESQL/LOCALSTACK EXECUTION VERIFIED
Data: 2026-09-18

Contexto:

O Loop 7 integra a fila FIFO de apostas ao use case financeiro existente. O
inbox deve sobreviver a restarts do processo, e uma mensagem não pode ser
apagada antes do commit da transação PostgreSQL que registra seu efeito.

Decisão:

* O envelope aceito é JSON estrito com `messageId`, `type`, `occurredAt` e
  `data`. Campos desconhecidos, campos obrigatórios ausentes, timestamps não
  RFC3339, OPENING e money inválido são falhas de entrada
  malformed/permanent. O consumer não faz acknowledgment para que a redrive
  policy configurada do SQS mova a mensagem para a DLQ após cinco receives.
* `data` é traduzido para o mesmo `financial.Command` processado por HTTP.
  O command ID é gerado pelo consumer; a chave de idempotência financeira é
  `data.idempotencyKey`.
* A aplicação calcula o digest SHA-256 em minúsculas do JSON canônico de
  negócio. A chave de idempotência, o command ID e os
  metadados de transporte são excluídos. Esse digest é armazenado no inbox e
  comparado em cada redelivery.
* A identidade do inbox é `(wager-transaction-consumer, messageId)`, imposta
  pela primary key existente do PostgreSQL. A inserção no inbox, o
  processamento financeiro, as alterações de ledger/state, as event rows e a
  conclusão do inbox usam a mesma transação explícita. Uma rejeição de negócio
  commitada recebe acknowledgment como um sucesso. Um rollback de transação
  não deixa uma inbox row concluída.
* Uma row de inbox concluída com o mesmo hash invoca apenas o replay financeiro
  replay e então recebe acknowledgment. Um mismatch de hash nunca é
  processado e permanece para redrive da DLQ. Uma row incompleta sofre retry
  com segurança; a idempotência financeira continua sendo a segunda proteção
  durável.
* O consumer usa um poll loop sequencial por processo. Múltiplos processos
  podem consumir concorrentemente; locking de wallet do PostgreSQL e
  constraints de identidade financeira continuam sendo os mecanismos de
  correção. Long polling tem default de dez segundos, visibility de trinta
  segundos e a visibility de retry é
  `min(5s * 2^(receiveCount-1), visibility-1s)`.
* O contexto de `OnStart` do Fx limita somente o startup. O consumer possui um
  contexto de execução explicitamente cancelável; `OnStop` possui seu
  cancelamento e aguarda a goroutine de polling antes que as dependências HTTP
  ou PostgreSQL sejam paradas.
* Producers usam o ID da wallet como `MessageGroupId` e o message ID do envelope
  como `MessageDeduplicationId`. A deduplicação FIFO apenas reduz tráfego do
  broker; ela não é usada como dependência da correção financeira.
* A deleção do SQS ocorre somente depois que `ProcessMessage` retorna com
  sucesso. Falhas de delete deixam a mensagem elegível para redelivery. Erros transitórios de
  processamento alteram a visibility com backoff. Shutdown cancela o polling
  e o trabalho de banco em andamento e então libera o receipt handle para
  redelivery.
* Readiness verifica descoberta da fila e atributos da fila além do
  PostgreSQL. Queue clients usam as credenciais AWS e o endpoint configurados;
  os defaults do LocalStack continuam sendo credenciais locais seguras de
  teste.
* A autorização do broker é responsabilidade da infraestrutura. O deployment
  concede ao consumer somente ações de descoberta/readiness da fila, receive,
  delete e alteração de visibility na fila principal. Producers/testes e
  provisioners usam roles separadas de least privilege; o consumer não tem
  acesso à DLQ, a menos que uma responsabilidade operacional o exija
  explicitamente. A policy concreta de AWS IAM e a limitação do LocalStack
  estão registradas em
  `infra/aws/sqs-access-policy.md`.

Consequências:

O caminho da mensagem é equivalente ao HTTP na fronteira da aplicação e não
duplica regras financeiras. Uma race de unicidade da identidade da transação em
uma wallet diferente faz rollback do inbox e de toda escrita financeira na
mesma transação SQL e retorna `ErrIdempotencyConflict`; ela nunca é resolvida
por uma transação financeira independente seguida de uma conclusão separada do
inbox. A mensagem consequentemente permanece sem acknowledgment e segue a
política de falha permanente/redrive. Testes de integração condicionais não
são evidência por si só; a execução real é reportada separadamente no
Integration Evidence Gate.

## ADR-008 — worker de pending references do Loop 8

Status: COMPLETED — CHECKPOINTED
Data: 2026-09-18

Decisão:

* Uma reversão cuja referência não vazia ainda não está presente faz commit
  `PENDING_REFERENCE`. Um campo de referência ausente é uma rejeição terminal
  com `REFERENCE_REQUIRED`; não é retryable porque nenhuma identidade futura
  pode ser resolvida.
* O trabalho pendente armazena `reference_attempts`,
  `reference_next_attempt_at` e um
  `failure_code` opcional na wager transaction. A política de retry tem
  default de dez tentativas com backoff exponencial base de um segundo e é
  carregada de `REFERENCE_MAX_ATTEMPTS`, `REFERENCE_BACKOFF` e
  `REFERENCE_POLL_INTERVAL`.
* O reference worker do Fx faz claim de uma row vencida por vez com
  PostgreSQL
  `FOR UPDATE SKIP LOCKED`. Toda resolução, rejeição terminal, lock da wallet,
  entrada do ledger, resultado da transação e event row fazem commit na mesma
  transação SQL. O worker possui um contexto de lifecycle explicitamente
  cancelável e reconstrói todo o estado a partir do PostgreSQL após restart.
* Uma referência compatível processada resolve a reversão. Uma referência
  pendente sofre retry até exhaustion. Uma referência terminal não bem-sucedida é
  rejeitada com `REFERENCE_NOT_SUCCESSFUL`; uma referência ausente ou não
  resolvida após exhaustion é rejeitada com o código estável
  `REFERENCE_NOT_FOUND` ou `REFERENCE_NOT_RESOLVED`. Mismatches de referência
  usam `REFERENCE_INVALID`.
* Eventos de pending reference e de rejeição são persistidos
  transacionalmente. Este loop não publica outbox rows; a publicação continua
  no escopo do Loop 9.
* A coordenação da identidade da referência usa um advisory lock do PostgreSQL
  com escopo de transação, derivado de `(providerId, externalTransactionId)`.
  O caminho normal de criação de transação e o worker de pending references
  adquirem o mesmo lock. Isso serializa a confirmação da referência contra uma
  decisão terminal `REFERENCE_NOT_FOUND` entre instâncias independentes sem
  introduzir um lock global de wallet/provider. A pending row recebe claim
  antes do reference lock; o caminho de criação da referência não faz claim de
  pending rows, portanto a ordem dos locks não forma ciclo.

## ADR-009 — publisher de outbox transacional

Status: COMPLETED — CHECKPOINTED
Data: 2026-09-18

Decisão:

* Rows do outbox são publicadas de forma assíncrona na fila SQS FIFO
  configurada por `SQS_EVENT_QUEUE`, com default `wager-events.fifo`. O
  envelope do evento é construído a partir da row persistida e contém eventId,
  eventType, aggregateId, correlationId, causationId opcional, occurredAt,
  version e o snapshot JSON imutável de data.
* Claims PostgreSQL usam `FOR UPDATE SKIP LOCKED`, um lease `claimed_at` e um
  `claim_token` persistido. Um publisher só pode marcar ou fazer retry do claim
  token que possui, portanto um publisher abandonado não pode sobrescrever um
  claim recuperado.
* Claims incrementam attempts antes da publicação. Uma publicação falha retorna
  a row retorna a PENDING com backoff exponencial; atingir o máximo configurado
  muda seu estado para FAILED, mantendo-a de forma durável e visível ao
  operador. Uma publicação bem-sucedida muda o estado para PUBLISHED. Uma
  publicação ambígua sofre retry com o mesmo eventId e
  SQS MessageDeduplicationId.
* Rows do outbox recebem um ordering ID durável do banco quando são criadas. Uma
  row só é elegível quando toda row anterior do mesmo aggregate está em
  PUBLISHED. Isso impede que um evento posterior ultrapasse um predecessor
  pending, claimed, retryable ou FAILED. Um predecessor FAILED, portanto,
  bloqueia eventos posteriores desse aggregate até que um operador o repare ou
  republique; isso preserva a garantia de ordering em vez de publicar
  silenciosamente um histórico incompleto do aggregate. Aggregates diferentes
  continuam elegíveis em paralelo.
* MessageGroupId é o ID do aggregate. Ele preserva a ordenação do broker somente
  depois que o protocolo de claim do banco estabeleceu a ordem de publicação;
  o SQS FIFO não reordena mensagens enviadas por publishers concorrentes. Esse
  destino, a policy de ordering e o schedule são decisões arquiteturais, não
  requisitos de domínio recuperados do desafio.
* O worker possui um contexto explícito de lifecycle e para antes que o
  PostgreSQL seja fechado. Ataques controlados de crash de processo e de falha
  são verificados no ADR-011.

## ADR-010 — observabilidade do Loop 10

Status: COMPLETED — CHECKPOINTED
Data: 2026-09-18

Decisão:

* O processo usa o handler JSON `slog` da standard library. Requests HTTP
  recebem ou geram um `X-Correlation-ID`, que é devolvido na response e
  propagado pelos logs do request. Valores externos só são preservados quando
  não vazios, têm no máximo 128 bytes ASCII e contêm `[A-Za-z0-9._:-]`; valores
  inválidos são substituídos por um ID gerado. Logs de mensagens SQS usam o
  `messageId` do envelope como identificador de correlação quando não existe
  correlação de transporte separada.
* Logs estruturados incluem os identificadores disponíveis em cada fronteira:
  correlation ID, message ID, transaction ID, wallet ID e provider ID. Eles
  não registram credenciais, tokens ou payloads financeiros completos.
* O registry interno de metrics expõe texto compatível com Prometheus em
  `GET /metrics`. Counters cobrem status de processamento, duplicatas
  separadas por `sqs_inbox` e `http_idempotency`, retries por um conjunto
  limitado de components, candidatos a redrive por receive exhaustion,
  conflitos de concorrência explicitamente classificáveis, conflitos de
  idempotência e divergência de reconciliation. Ele também expõe a latência de
  processamento e a idade do evento de outbox pending/claimed mais antigo. IDs
  não são labels de metrics.
* `wager_sqs_redrive_candidate_total` é incrementada quando uma mensagem com
  falha atinge o `SQS_MAX_RECEIVE_COUNT` configurado; a aplicação não observa
  a posterior inserção na DLQ pelo broker, portanto o broker continua sendo a
  autoridade sobre o redrive efetivo. A métrica não é uma alegação de IAM nem
  de exactly-once. Conflitos de idempotência sequenciais são reportados
  separadamente e não implicam conflito de concorrência. O protocolo atual de
  locking de wallet não expõe um sinal independente de conflito de
  concorrência, portanto esse counter permanece em zero até que um conflito de
  serialização, versão ou claim classificável seja observado.
* Os diagnósticos de lifecycle do Fx usam seu event logger no-op suportado para
  que o stream operacional permaneça somente JSON; registros de lifecycle da
  aplicação continuam usando o handler JSON `slog`. Isso suprime o stream
  textual `[Fx]` do Fx e não altera os lifecycle hooks.
* Os endpoints existentes `/health/live` e `/health/ready` permanecem
  separados. O contrato de readiness continua exigindo PostgreSQL e SQS;
  metrics não são usadas como dependência de readiness.

Consequências:

Observabilidade é process-local e diagnóstica; não é um state store financeiro.
Counters zeram no restart do processo, enquanto o estado financeiro e do
outbox continua respaldado pelo PostgreSQL. OpenTelemetry e dashboards
continuam opcionais e fora deste loop. Failure injection está documentada e
verificada no ADR-011.

## ADR-011 — failure engineering do Loop 11

Status: COMPLETED — CHECKPOINTED
Data: 2026-09-18

Decisão:

* Testes de falha usam PostgreSQL e LocalStack reais sempre que a fronteira
  atacada exige isso. Um trigger PostgreSQL limitado a uma identidade de
  correlação exclusiva do teste injeta um erro na inserção final do outbox;
  como as escritas de wallet, transaction e ledger já foram tentadas, isso
  prova o rollback da transação SQL completa, e não um atalho de validação. Um
  processo filho separado é encerrado enquanto esse trigger mantém um advisory
  lock transacional, provando o mesmo comportamento de rollback para um crash
  real de processo antes do commit.
* Janelas de crash do consumer e do outbox usam processos filhos de teste
  isolados. O processo filho do consumer só é encerrado depois da conclusão
  durável do inbox e antes de `DeleteMessage`; um consumer reiniciado faz
  redelivery com segurança por meio do inbox durável. O processo filho do
  outbox só é encerrado depois que `SendEvent` real tem sucesso e antes de
  `MarkPublished`; o lease recovery republica a mesma identidade e payload de
  evento persistidos.
* Falhas temporárias de dependência são injetadas na fronteira do client
  PostgreSQL/SQS e o recovery é então executado contra o serviço real. Isso
  demonstra rollback transacional e retry/recovery durável sem alterar código
  de produção nem tratar a deduplicação FIFO do broker como idempotência
  financeira.

Consequências:

Estes são experimentos controlados de falha de processo e de dependência, não
simulações de perda de energia do host ou kill de container. A implementação
continua afirmando entrega de eventos at-least-once: um evento pode ser enviado
mais de uma vez após uma falha ambígua do publisher, sempre com seu event ID
estável. As constraints existentes de inbox durável, idempotência financeira,
ledger e outbox continuam sendo os mecanismos de correção; o harness de falha
não adiciona failpoint de produção.

---

# 35. Status atual

Status da arquitetura:

```text
LOOP 12 IMPLEMENTED — PENDING HUMAN REVIEW
```

O status da implementação não deve ser inferido deste documento.

Somente comportamento verificado deve ser descrito como implementado.
