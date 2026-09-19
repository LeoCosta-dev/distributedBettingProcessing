# Desafio de Backend — Processamento Distribuído de Apostas

## 0. Escopo normativo e proveniência da restauração

Este documento é a especificação funcional e técnica normativa do processador
distribuído de transações de apostas.

SPEC.md foi originalmente truncada antes do planejamento dos loops de
implementação. Sua primeira restauração foi feita sem acesso à fonte primária
completa do desafio e, por isso, preservou algumas decisões como gaps. A fonte
primária completa foi posteriormente recuperada localmente como CHALLENGE.md.
Esta reconciliação restaura os requisitos explícitos nessa fonte e preserva
somente decisões que o desafio realmente deixa em aberto.

A precedência de fontes usada nesta reconciliação é:

1. CHALLENGE.md, a fonte normativa primária;
2. esta SPEC.md, como especificação interna derivada;
3. ARCHITECTURE.md, para decisões e interpretações arquiteturais;
4. TASKS.md, como plano incremental de execução;
5. implementação, migrations e testes, somente como evidência auxiliar e
   nunca como fonte para inventar requisitos.

As seções anteriores a Gaps abertos da especificação contêm requisitos normativos
explícitos e derivados restaurados da fonte primária. Um gap não é um
requisito: ele registra uma decisão que o desafio ainda deixa em aberto.
Decisões humanas selecionadas posteriormente para um loop não devem ser
representadas como se tivessem sido recuperadas de CHALLENGE.md.

A especificação não infere a conclusão da implementação a partir deste
documento. O status dos loops e o estado de revisão humana continuam sendo
controlados por TASKS.md e pelo processo de looping.

---

## 1. Objetivo

Implementar um serviço backend em Go capaz de processar operações financeiras
de apostas a partir de HTTP e SQS em um ambiente distribuído.

O sistema deve preservar a correção financeira quando múltiplas instâncias da
aplicação processarem operações concorrentemente e quando ocorrerem falhas
entre etapas do processamento.

O sistema deve suportar:

* criação de wallet;
* wagering transactions;
* idempotência persistente;
* operações concorrentes de wallet;
* processamento assíncrono de SQS;
* inbox transacional;
* outbox transacional;
* referências pendentes de transações;
* reversões;
* reconciliation;
* autenticação e autorização OAuth 2.0/OIDC;
* recovery após interrupção de processo;
* health e estado de processamento observáveis.

---

## 2. Invariantes não negociáveis

Estas invariantes devem sempre ser preservadas.

### 2.1 Money

* Valores monetários nunca devem usar float32 ou float64.
* Valores monetários externos usam strings com exatamente duas casas decimais.
* Currency usa códigos ISO 4217.
* Entradas financeiras externas não podem ser negativas.
* Valores vazios, NaN e Infinity são rejeitados.
* Notação científica é rejeitada.
* Escala decimal excessiva é rejeitada.
* Valores inválidos são rejeitados em vez de arredondados silenciosamente.
* Aritmética entre currencies diferentes é rejeitada.
* Overflow de inteiro deve ser detectado quando `int64` for usado.
* A persistência deve preservar o amount e a currency exatos.
* Cálculos internos podem conter valores negativos temporariamente.
* Saldos de wallets nunca podem ficar negativos.

### 2.2 Wallet e ledger

* Uma wallet é a raiz do aggregate financeiro.
* Toda mutação financeira efetiva de saldo tem exatamente uma entrada
  correspondente no ledger, commitada atomicamente com a atualização do saldo
  e o estado da transação.
* Entradas do ledger são append-only e imutáveis.
* Correções criam novas entradas financeiras; entradas anteriores do ledger
  nunca são alteradas ou apagadas.
* A concorrência da wallet é coordenada no escopo da wallet.
* Wallets independentes devem poder progredir concorrentemente.

### 2.3 Idempotência e entrega

* A idempotência deve sobreviver a restarts de processo e não pode depender de
  memória process-local ou da deduplicação FIFO do SQS.
* O sistema distingue:
  1. a mesma chave de idempotência com o mesmo payload de negócio;
  2. a mesma chave de idempotência com payload de negócio diferente;
  3. a mesma identidade de transação externa com uma chave de idempotência diferente.
* Um replay bem-sucedido retorna o resultado persistido do processamento
  original.
* Um replay nunca reconstrói sua resposta a partir do saldo atual da wallet.
* Entrega at-least-once, mensagens duplicadas e requests duplicados são
  condições normais de operação.

### 2.4 Atomicidade

Alterações financeiras usam transações PostgreSQL explícitas. A fronteira
transacional deve estar visível na implementação da aplicação ou do repository.

Quando aplicável, os itens a seguir devem fazer commit atomicamente:

* estado da wager transaction;
* saldo da wallet;
* entrada do ledger;
* conclusão da inbox;
* criação do evento de outbox.

Invariantes financeiras que o PostgreSQL pode impor também devem ser
protegidas por constraints de banco, unicidade, foreign keys, indexes ou
triggers. Isso inclui saldos de wallet não negativos, unicidade de identidade,
relações de currency e imutabilidade do ledger.

Nenhum evento de integração é publicado antes do commit da transação de origem.

### 2.5 Separação de responsabilidades

HTTP e SQS traduzem a entrada de transporte para o mesmo comando e use case
financeiro da aplicação. Adapters de transporte não devem duplicar regras
financeiras nem ignorar a validação de domínio.

---

## 3. Contrato de Money

A representação externa é:

~~~json
{
  "amount": "25.00",
  "currency": "BRL"
}
~~~

Money carrega amount exato e sua currency. A representação interna selecionada
é int64 na menor unidade monetária. Para o desafio atual:

~~~text
1 BRL = 100 cents
"25.00" BRL = 2500
~~~

O domínio deve suportar:

* construção a partir de uma string decimal;
* zero por currency;
* adição;
* subtração;
* negação;
* comparação;
* serialização e desserialização usando a representação externa.

Parsing, adição, subtração e negação devem detectar overflow de inteiro.
Parsing externo deve rejeitar valores negativos, notação científica,
representações numéricas inválidas e escala excessiva. Entrada inválida não
deve ser arredondada silenciosamente.

Aritmética entre currencies incompatíveis deve falhar. Os cenários principais
podem operar somente com BRL, desde que o domínio continue carregando currency
e os testes cubram o comportamento de currencies incompatíveis.

A aritmética interna pode representar temporariamente um valor intermediário
negativo, mas o saldo de uma wallet nunca pode ser negativo.

---

## 4. Wallet

Uma wallet é identificada exclusivamente por:

~~~text
(playerId, currency)
~~~

Uma wallet contém:

* id;
* playerId;
* currency;
* balance;
* version;
* createdAt;
* updatedAt.

A versão inicial é 1. A versão aumenta somente quando o saldo da wallet muda.
Uma operação de valor zero não altera a versão.

Debit deve verificar que o amount não é negativo, possui a currency da wallet
e não excede o saldo atual. Credit deve verificar que o amount não é negativo,
possui a currency da wallet e não causa overflow. Uma alteração de saldo
bem-sucedida e diferente de zero deve atualizar a wallet e criar sua entrada
no ledger na mesma transação do banco.

A row da wallet é a fronteira de coordenação da concorrência financeira. Uma
solução deve coordenar operações na mesma wallet entre processos independentes
e não deve usar lock process-local como garantia financeira.

A criação e a rehydration da wallet são operações separadas. Uma abertura
positiva cria uma transação interna OPENING em PROCESSED, uma entrada CREDIT no
ledger e os eventos de outbox WagerTransactionProcessed e
WalletBalanceChanged no mesmo commit. Uma abertura zero cria a wallet sem
transação OPENING, entrada no ledger ou esses eventos financeiros. Uma abertura
duplicada `(playerId, currency)` é rejeitada como conflito.

---

## 5. Wager Transactions

### 5.1 Tipos

Os tipos de transação suportados são:

~~~text
OPENING
BET
WIN
LOSS
REFUND
ROLLBACK
~~~

As operações externas são:

~~~text
BET
WIN
LOSS
REFUND
ROLLBACK
~~~

OPENING é reservado para criação interna de wallet. Um request externo
contendo OPENING deve ser rejeitado e não deve criar uma transação de provider.

OPENING exige identidade interna estável, wallet, player, currency, value,
state e timestamps. Provider externo, ID externo, chave de idempotência, hash
do payload, round, game e campos de referência não se aplicam à sua origem
interna. O modelo de persistência deve distinguir operações internas e
externas e impedir crédito inicial duplicado.

### 5.2 Identidade e campos

Uma wagering transaction contém, quando aplicável:

* ID interno da transação;
* ID externo da transação;
* providerId;
* walletId;
* playerId;
* gameId;
* roundId;
* kind da transação;
* amount e currency exatos de money;
* state;
* chave de idempotência;
* hash canônico do payload;
* resultado persistido do processamento;
* `referenceExternalTransactionId` para operações de reversão.

Quando aplicável, também persiste a referência interna resolvida e um
failureCode estável. O resultado persistido é exatamente o resultado retornado
ao provider, incluindo o snapshot original do saldo.

A identidade externa da transação é `(providerId, externalTransactionId)` e
deve ser única. A identidade de idempotência aplicável também é única no banco.
As currencies da transação e da wallet devem coincidir.

Player, wallet, provider e identidade de negócio da transação devem ser
validados antes da aplicação de uma mutação financeira.

### 5.3 Regras das operações

A matriz normativa de operações é:

| Type | Movement | Comportamento obrigatório |
| --- | --- | --- |
| BET | DEBIT | Valor positivo e saldo suficiente. Saldo insuficiente é um resultado REJECTED durável, sem mutação de wallet ou ledger. |
| WIN | CREDIT | Valor positivo; pode referenciar um BET da mesma round. |
| LOSS | None | Amount exatamente `0.00`; sem entrada no ledger e sem alteração da versão da wallet. Um LOSS processado emite WagerTransactionProcessed, mas não WalletBalanceChanged. |
| REFUND | CREDIT | Valor positivo; retorna o valor integral de um BET processado. A referência é obrigatória. |
| ROLLBACK | Oposto ao original | Valor positivo; reverte integralmente um BET, WIN ou REFUND processado. A referência é obrigatória. |

Referências de REFUND e ROLLBACK são resolvidas por `(providerId,
referenceExternalTransactionId)`. A operação e a referência devem concordar em
provider, player, wallet, currency e round. O amount da reversão deve ser igual
ao amount referenciado; reversões parciais não são suportadas. Uma referência
não pode receber duas reversões bem-sucedidas do mesmo tipo. A interação entre
tipos de reversão distintos permanece aberta. Uma reversão que debitaria mais
que o saldo disponível é rejeitada e auditada com failureCode distinto de
saldo insuficiente em BET.

### 5.4 Estados da transação

Os estados são:

~~~text
PENDING
PROCESSED
REJECTED
FAILED
PENDING_REFERENCE
~~~

A máquina de estados permite:

~~~text
PENDING -> PROCESSED
PENDING -> REJECTED
PENDING -> FAILED
PENDING -> PENDING_REFERENCE
PENDING_REFERENCE -> PROCESSED
PENDING_REFERENCE -> REJECTED
~~~

PROCESSED, REJECTED e FAILED são estados terminais. Transações terminais não
podem fazer nova transição. PENDING_REFERENCE é trabalho durável e retryable e
não pode transicionar diretamente para FAILED sem uma policy explícita de
falha.

FAILED é reservado para um resultado de processamento de infraestrutura que
falhou permanentemente e foi registrado de forma durável. No slice de
processamento financeiro, uma falha de infraestrutura aborta a transação
PostgreSQL; o registro e o recovery desse estado pertencem ao comportamento de
messaging e recovery.

### 5.5 Referências e PENDING_REFERENCE

REFUND e ROLLBACK resolvem sua referência usando:

~~~text
(providerId, referenceExternalTransactionId)
~~~

A validação da referência deve verificar:

* provider;
* player;
* wallet;
* currency;
* round;
* estado da transação original;
* tipo da transação original;
* amount da reversão.

A referência ausente não é uma falha de infraestrutura. A reversão é
armazenada de forma durável como PENDING_REFERENCE, sem alterar wallet ou
ledger, e um evento de pending reference é criado transacionalmente. A
resolução posterior processa a reversão ou a rejeita.

Um reference worker faz retry com backoff exponencial, inclusive após restart.
A policy deve escolher uma quantidade máxima de attempts ou TTL. Exhaustion
produz um resultado REJECTED com failureCode estável de referência não
encontrada e um evento de rejeição. O comportamento quando a transação
referenciada ainda está pending ou terminou sem sucesso deve ser definido pela
policy de pending references.

O sistema não deve permitir duas reversões bem-sucedidas do mesmo tipo
aplicável. A interação e a cardinalidade entre tipos de reversão distintos não
estão determinadas; consulte Gaps abertos da especificação.

---

## 6. Abertura de Wallet

A abertura de wallet é uma operação interna da aplicação. Ela cria uma wallet
com o player, currency e saldo inicial não negativo solicitados, sujeita à
invariante de unicidade `(playerId, currency)`.

Para um saldo inicial positivo, a operação cria atomicamente com a criação da
wallet uma transação interna OPENING em PROCESSED, uma entrada CREDIT no ledger
e os eventos correspondentes WagerTransactionProcessed e WalletBalanceChanged.
A versão da abertura é 1. Uma abertura zero cria somente o estado da wallet e
nenhuma transação OPENING, entrada no ledger ou evento financeiro. Uma abertura
duplicada para o mesmo `(playerId, currency)` é um conflito.

A operação interna de abertura não deve ser exposta como operação de apostas de
provider. Callers externos não podem enviar OPENING pelo fluxo de wagering
transactions.

---

## 7. Ledger

O ledger é a trilha de auditoria autoritativa e append-only da reconciliation.

Cada entrada do ledger contém id, walletId, transactionId, direction, value,
balanceBefore, balanceAfter e timestamp de criação. Direction é DEBIT ou
CREDIT, e value e balances carregam a currency da wallet. Entradas do ledger
são imutáveis e não podem ser atualizadas ou apagadas. Correções criam novas
entradas financeiras em vez de modificar entradas anteriores. A relação
(walletId, transactionId) deve impedir mais de uma entrada correspondente para
a mesma wallet transaction.

A construção do ledger valida a equação do movimento:

~~~text
CREDIT: balanceAfter = balanceBefore + value
DEBIT:  balanceAfter = balanceBefore - value
~~~

O banco impõe unicidade e proteção contra edição ou deleção de entradas. LOSS e
operações rejeitadas não criam entradas no ledger.

Toda mutação efetiva de saldo cria exatamente uma entrada correspondente no
ledger, commitada atomicamente com a atualização do saldo e o estado da
transação.


---

## 8. Idempotência

### 8.1 Identidades persistentes

O banco é a fonte de verdade da idempotência. Ele persiste:

* a chave de idempotência;
* a identidade de negócio;
* o hash canônico do payload;
* o estado de processamento;
* informações do resultado persistido.

O Idempotency-Key é obrigatório para processamento financeiro externo. A
unicidade do banco protege pelo menos:

~~~text
(providerId, externalTransactionId)
~~~

e a identidade de idempotência aplicável, cujo escopo e campos exatos não são
selecionados aqui; consulte Gaps abertos da especificação.

Tentativas concorrentes que disputem as identidades externas ou de idempotência
especificadas devem resultar no vencedor persistido ou em um conflito de
idempotência classificável; um erro de unique constraint não deve resultar em
uma segunda mutação financeira.

### 8.2 Payload canônico e hash

O payload canônico deve usar ordenação determinística das chaves JSON. A chave
de idempotência, o hash fornecido pelo caller e os metadados de transporte são
excluídos. O algoritmo, o conjunto completo de campos de negócio, a ordenação
exata e as regras de normalização não são selecionados por esta especificação;
devem ser documentados pela arquitetura escolhida e compartilhados por HTTP e
SQS antes da execução do application command. O serviço calcula o hash; um
hash fornecido pelo caller não é autoritativo.

### 8.3 Replay e conflitos

* Mesma chave de idempotência e mesmo payload de negócio: retorne o resultado
  persistido original sem aplicar a operação novamente.
* Mesma chave de idempotência e payload de negócio diferente: retorne um
  conflito de idempotência classificável sem efeitos financeiros.
* Mesma identidade de transação externa e chave de idempotência diferente:
  retorne um conflito de idempotência classificável sem efeitos financeiros.

A resposta de replay usa o resultado persistido do processamento original e
não deve reexecutar o movimento financeiro nem reconstruir a resposta a partir
do saldo atual da wallet. Sua representação persistida exata e o comportamento
para cada estado de transação permanecem abertos; consulte Gaps abertos da
especificação.

O tratamento de um registro existente sem hash canônico compatível ou sem
resultado persistido válido não é selecionado por esta especificação; consulte
GAP-IDEMPOTENCY-001. Nenhum comportamento para esse edge case é restaurado
aqui a partir da fonte primária.

---

## 9. Processamento financeiro e transações PostgreSQL

Uma operação financeira normal usa uma transação PostgreSQL explícita. Quando
aplicável, essa transação contém coordenação da wallet, validação, estado da
wager transaction, saldo da wallet, entrada do ledger, conclusão do inbox e
criação do evento de outbox. Esta especificação não seleciona uma ordem para
essas etapas.

O row lock da wallet é adquirido dentro da mesma transação da mutação.
Validação da aplicação/domínio e constraints do banco devem proteger ambas a
invariante de saldo não negativo.

Todos os efeitos que compõem a operação devem permanecer na mesma transação
PostgreSQL e somente se tornar confirmados quando essa transação fizer commit.

Para uma rejeição de negócio, o registro da transação e o evento de rejeição
são persistidos atomicamente, enquanto wallet e ledger permanecem inalterados.
Para uma referência ausente, o registro da transação e o evento de pending
reference são persistidos atomicamente, enquanto wallet e ledger permanecem
inalterados.

Se a transação não fizer commit, nenhum de seus estados financeiros, entrada do
ledger, conclusão do inbox ou registros do outbox pode ser tratado como
commitado. Nenhum evento externo pode ser publicado antes do commit.

Todo I/O recebe context.Context, e cancelamento e timeouts devem ser
respeitados. Erros de negócio devem ser tipados ou classificáveis com errors.Is
ou errors.As; falhas de validação de negócio não devem usar panic.

---

## 10. Contrato HTTP conhecido por esta especificação

HTTP e SQS são adapters de transporte para o mesmo use case financeiro da
aplicação. O inventário de method/path a seguir é explicitamente conhecido:

| Method | Path | Propósito conhecido |
| --- | --- | --- |
| POST | /wallets | Operação de abertura/criação de Wallet |
| GET | /wallets/:walletId | Consulta de Wallet |
| GET | /wallets/:walletId/ledger | Consulta do ledger da Wallet |
| POST | /wagering/transactions | Processamento de transação financeira |
| GET | /wagering/transactions/:transactionId | Consulta de transação |
| GET | /providers/:providerId/wagering/transactions/:externalTransactionId | Consulta/replay de transação externa com escopo de provider |
| POST | /wallets/:walletId/reconciliation | Operação de reconciliation da Wallet |
| GET | /health/live | Liveness do processo |
| GET | /health/ready | Readiness das dependências |

O adapter traduz um request para o application command e não deve reimplementar
regras de transação, referência, idempotência ou autorização.

### 10.1 Contratos wire primários

O request primário de abertura de wallet é:

~~~json
{
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "initialBalance": { "amount": "1000.00", "currency": "BRL" }
}
~~~

A response documentada contém `id`, `playerId`, `balance` e `version`. Uma
abertura positiva cria OPENING e seu ledger/events no mesmo commit; uma abertura
zero não cria movimento financeiro; uma abertura duplicada `(playerId,
currency)` é um conflito.

O request primário de wagering usa estes nomes:

~~~json
{
  "providerId": "provider-a",
  "externalTransactionId": "transaction-123",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "roundId": "round-987",
  "gameId": "fortune-chimp",
  "kind": "BET",
  "money": { "amount": "25.00", "currency": "BRL" }
}
~~~

O header `Idempotency-Key` é obrigatório. A response processada documentada usa
`transactionId`, `status`, `balance` e `idempotentReplay`. Para requests de
reversão, `referenceExternalTransactionId` é adicionado ao body. A identidade
autenticada é autoritativa para autorização do provider; um providerId enviado
por um caller nunca pode substituí-la.

A response primária de reconciliation contém `walletId`, `storedBalance`,
`calculatedBalance`, `difference`, `consistent` e `checkedEntries`.
`difference` é o saldo armazenado menos o saldo reconstruído do ledger.
Reconciliation é read-only e divergências são reportadas na response, nos logs
estruturados e em uma metric.

Endpoints de negócio exigem autenticação OAuth 2.0/OIDC real e autorização de
provider. Dados de transações com escopo de provider, incluindo replays, devem
ser isolados por provider antes de qualquer acesso a dados ou efeito financeiro.

Esta seção registra os schemas e nomes explicitamente fornecidos pela fonte
primária. Intencionalmente não atribui status codes HTTP, error envelope ou um
claim exato de token; esses itens permanecem como gaps abaixo. Os nomes
primários dos campos não devem ser substituídos por aliases específicos da
implementação.

---

## 11. Autenticação e autorização

Keycloak é o identity provider OAuth 2.0/OIDC local configurado. A aplicação
valida tokens emitidos pelo IdP configurado e usa a identidade autenticada para
determinar o providerId autorizado.

A autorização deve ser imposta antes do acesso com escopo de provider a:

* criação de transação;
* consulta de transação;
* replay de transação;
* consulta de transação externa;
* quaisquer outros dados de wagering com escopo de provider.

Um provider autenticado não deve acessar transações de outro provider,
incluindo responses de replay. A abertura interna de wallet não é uma operação
de provider.

O sistema não deve armazenar senhas de usuários nem emitir tokens de
autenticação customizados no lugar do IdP.

O claim, role, scope, audience e policy de endpoint exatos não são selecionados
intencionalmente aqui; consulte Gaps abertos da especificação.

---

## 12. Processamento SQS

O processamento SQS assume entrega at-least-once. A infraestrutura local
provisiona as filas FIFO `wager-transactions.fifo` e
`wager-transactions-dlq.fifo` com redrive configuration. A deduplicação FIFO
não é o mecanismo de idempotência financeira.

O envelope de mensagem solicitado contém `messageId`, `type`, `occurredAt` e
`data`. O `data` de wagering contém providerId, externalTransactionId,
idempotencyKey, playerId, walletId, roundId, gameId, kind e o objeto money
`{amount,currency}`. `data.idempotencyKey` é a chave de idempotência
financeira. O consumer usa o messageId do envelope como identidade durável da
mensagem e valida o hash da mensagem em cada redelivery.

O consumer SQS deve:

* validar o envelope da mensagem;
* traduzi-lo para o mesmo comando financeiro da aplicação usado por HTTP;
* usar um registro durável de inbox;
* executar o comando financeiro e a conclusão do inbox na transação aplicável
  do banco;
* apagar a mensagem SQS somente depois do commit dessa transação;
* fazer acknowledgment/delete de uma rejeição de negócio commitada de forma
  durável;
* manter falhas transitórias retryable por meio de visibility timeout e
  redelivery;
* suportar graceful shutdown.

Uma entrega duplicada não deve executar a operação financeira duas vezes. Uma
mensagem redelivered após o commit, mas antes da deleção no SQS, deve ser
reconhecida pelo inbox durável e/ou pelos registros de idempotência financeira.

Rejeições de negócio são terminais e podem ser apagadas após o commit durável.
Falhas transitórias usam retry com backoff. Falhas permanentes ou attempts
esgotados chegam à DLQ. A implementação deve documentar o tratamento de
mensagens malformed, limites de attempts, visibility timeout, MessageGroupId e
MessageDeduplicationId. Em SIGTERM, o polling para e o trabalho em andamento
termina dentro do deadline ou libera a visibility para redelivery seguro.

---

## 13. Inbox

Registros de inbox são registros PostgreSQL duráveis. Eles incluem identidade
da mensagem, identidade do consumer, hash do payload e informações de receipt e
conclusão. Sua identidade é:

~~~text
(consumerName, messageId)
~~~

O banco impõe unicidade para essa identidade. O estado exato de processamento
interrompido, o comportamento de hash-mismatch e a representação de
claim/recovery permanecem abertos; consulte Gaps abertos da especificação.

A conclusão do inbox e a operação financeira causada pela mensagem compartilham
a mesma transação do banco. Uma mensagem duplicada não deve fazer a operação
financeira executar novamente, e uma transação não commitada não deve deixar
um registro de inbox concluído.

---

## 14. Outbox transacional

Eventos de integração são criados na mesma transação PostgreSQL que as
alterações de estado que descrevem. A publicação do outbox é assíncrona e
começa somente após o commit.

O outbox armazena identidade estável do evento, aggregate, tipo do evento,
snapshot imutável do payload, instante de ocorrência, attempts, próximo horário
de envio e estado de publicação. O publisher deve sobreviver às janelas entre
commit e publicação e entre publicação e confirmação, inclusive permitindo que
outro publisher recupere trabalho abandonado.

O worker do outbox deve suportar:

* múltiplos workers ou instâncias da aplicação;
* claiming seguro de records;
* retries;
* backoff;
* recovery de trabalho abandonado;
* event IDs estáveis entre retries e republicação.

A publicação pode ser repetida após uma falha ambígua. Espera-se que os
consumers usem o event ID estável para sua própria deduplicação. Um worker de
outbox não deve perder um evento commitado apenas porque um processo parou
entre o commit e a publicação.

---

## 15. Modelo de eventos

Os tipos de evento obrigatórios são:

~~~text
WagerTransactionProcessed
WagerTransactionRejected
WalletBalanceChanged
WagerTransactionPendingReference
~~~

Os gatilhos dos eventos são:

| Evento | Gatilho |
| --- | --- |
| WagerTransactionProcessed | Conclusão bem-sucedida de uma operação, incluindo LOSS. |
| WagerTransactionRejected | Rejeição definitiva de negócio. |
| WalletBalanceChanged | Alteração efetiva do saldo da wallet. |
| WagerTransactionPendingReference | Espera durável por uma referência ausente. |

Eventos contêm:

~~~text
eventId
eventType
aggregateId
correlationId
causationId (opcional)
occurredAt
version
data
~~~

Payloads de eventos são snapshots imutáveis do estado que descrevem. Money é
serializado como strings decimais e timestamps usam UTC RFC 3339. As condições
acima são normativas. O payload de WalletBalanceChanged contém walletId,
transactionId, direction, money, balanceBefore, balanceAfter e walletVersion.
Tipo e versão do evento são atribuídos pelo event constructor. Os valores
exatos de versão, a geração de correlation/causation, ordering e dados
  adicionais específicos do evento permanecem abertos; consulte Gaps abertos
  da especificação.

---

## 16. Reconciliation

Reconciliation compara o saldo persistido da wallet com uma reconstrução a
partir do ledger imutável da wallet. Deve informar se os dois são consistentes
e detectar um ledger inconsistente em vez de aceitá-lo silenciosamente.

Reconciliation não é permissão para alterar ou apagar o histórico do ledger.
Qualquer correção deve ser representada por novas entradas financeiras e deve
preservar as regras de atomicidade de wallet, ledger e transaction.

O entry point HTTP conhecido é:

~~~http
POST /wallets/:walletId/reconciliation
~~~

A response contém `walletId`, `storedBalance`, `calculatedBalance`,
`difference`, `consistent` e `checkedEntries`. `difference` é o saldo
armazenado menos o saldo reconstruído do ledger. Reconciliation é read-only, e
divergências são reportadas na response, nos logs estruturados e em uma metric.
Mapeamento de status HTTP, autorização concreta e policy de remediation
permanecem abertos.

---

## 17. Concorrência

A implementação deve funcionar com múltiplos processos independentes da
aplicação. Row-level locking do PostgreSQL no escopo da wallet é o mecanismo
primário de coordenação. O lock da wallet é adquirido na transação financeira,
por exemplo:

~~~sql
SELECT ...
FROM wallets
WHERE id = $1
FOR UPDATE;
~~~

A implementação não deve depender da memória do processo Go ou de locks
process-local. Operações em wallets diferentes não devem ser bloqueadas por um
lock mantido para outra wallet.

O cenário exigido é:

* saldo da wallet: 100.00 BRL;
* duas operações BET diferentes;
* ambas no valor de 80.00 BRL;
* submetidas concorrentemente.

O resultado esperado é:

* exatamente uma transação está em PROCESSED;
* exatamente uma transação é rejeitada por saldo insuficiente;
* saldo final de 20.00 BRL;
* existe exatamente um débito no ledger, além de qualquer entrada de abertura.

A verificação deve incluir pelo menos três processos independentes da
aplicação ou processos equivalentes com memória Go e pools de conexões do banco
separados.

---

## 18. Modelo de recovery e falhas

O sistema assume:

~~~text
at-least-once delivery
~~~

Ele deve tolerar:

* requests HTTP duplicados;
* mensagens SQS duplicadas;
* a mesma operação chegando por HTTP e SQS;
* crash de processo antes do commit do banco;
* crash de processo depois do commit do banco;
* crash do consumer antes da deleção no SQS;
* crash do publisher de outbox;
* indisponibilidade temporária do PostgreSQL;
* indisponibilidade temporária do SQS;
* chegada tardia de uma referência;
* trabalho pendente durante restart da aplicação;
* múltiplas instâncias da aplicação;
* replay após restart.

O comportamento exigido para as janelas de falha é:

* Antes do commit, a transação do banco sofre rollback e sua mutação
  financeira, entrada do ledger, conclusão do inbox e registros do outbox não
  ficam visíveis como commitados.
* Depois do commit e antes da deleção no SQS, o redelivery é seguro porque o
  inbox e o estado de idempotência financeira são duráveis.
* Depois de um commit financeiro e antes da publicação do evento, o registro
  do outbox é recuperado de forma assíncrona.
* Depois de uma publicação ambígua de evento, a republicação mantém o mesmo
  event ID.
* Referências pendentes sobrevivem ao restart do processo e continuam
  retryable até que a policy configurada de pending as resolva ou rejeite.

Falhas transitórias de dependência não devem criar uma mutação financeira
parcial. Uma falha permanente de infraestrutura pode ser registrada como
FAILED de acordo com a policy de falha; essa policy é um specification gap
aberto.

---

## 19. Lifecycle e shutdown

Uber Fx gerencia a composição e o lifecycle da aplicação. O shutdown segue esta
sequência:

~~~text
SIGTERM
  ↓
parar de aceitar trabalho novo
  ↓
parar o polling de mensagens
  ↓
finalizar ou liberar com segurança o trabalho em andamento
  ↓
parar workers em background
  ↓
fechar dependências
  ↓
exit
~~~

Cancelamento e deadlines propagam por context.Context. Workers não devem ser
abandonados sem um mecanismo durável de recovery.

O período de grace e a policy de trabalho em andamento exatos permanecem
abertos.

---

## 20. Health e observabilidade

### 20.1 Health

~~~http
GET /health/live
GET /health/ready
~~~

Liveness indica que o processo está vivo. Readiness verifica PostgreSQL e SQS.
Uma indisponibilidade de dependência faz a readiness falhar sem
necessariamente encerrar o processo.

O contrato wire exato de readiness e a policy de probes permanecem abertos.

### 20.2 Logs e metrics

Use logging JSON estruturado. Identificadores importantes de correlação incluem:

~~~text
correlationId
messageId
transactionId
walletId
providerId
~~~

Logs não devem conter payloads financeiros sensíveis completos ou credenciais.

Metrics devem cobrir, quando aplicável:

~~~text
status da transação
duplicatas de idempotência
quantidade de retries
quantidade de DLQ
conflitos de concorrência
atraso do outbox
latência de processamento
divergência de reconciliation
~~~

---

## 21. Requisitos tecnológicos e arquiteturais

O sistema é um monólito modular que pode executar como múltiplas instâncias
independentes. Ele separa:

~~~text
Domain
Application
Infrastructure
Transport
Composition
~~~

O domínio não deve depender de HTTP, SQS, PostgreSQL, Keycloak ou Uber Fx. Ele
contém entities, value objects, erros de domínio, transições de estado,
invariantes financeiras e regras de negócio. Criação e rehydration são
conceitos separados; rehydration não deve reaplicar operações nem emitir eventos.

Use:

* Go;
* PostgreSQL;
* pgx e SQL explícito para persistência;
* AWS SQS, com LocalStack para desenvolvimento local;
* Keycloak como provider OIDC local;
* Uber Fx para composição e lifecycle;
* Docker Compose para infraestrutura local reproduzível.

A composição Uber Fx usa fx.Module, fx.Provide, fx.Invoke, constructors e
fx.Lifecycle. O domínio não importa Uber Fx.

SQL explícito deve manter inspecionáveis as fronteiras transacionais, row
locks, unicidade, check constraints e condições de update. Migrations
PostgreSQL devem ser versionadas e suportar o comportamento de rollback
definido pelo projeto.

A infraestrutura local inclui PostgreSQL, LocalStack SQS e Keycloak.
Configuração segura de exemplo pode ser commitada, mas secrets reais devem ser
fornecidos por variáveis de ambiente e não devem ser commitados. Um checkout
limpo deve ser reproduzível a partir do Docker Compose, incluindo provisionamento
de filas e IdP.

A organização exata dos packages pode evoluir se as fronteiras e invariantes do
domínio permanecerem intactas.

---

## 22. Testes, Quality Gates e critérios de entrega

Testes verificam comportamento e invariantes, não apenas detalhes de
implementação.

A verificação obrigatória inclui:

* testes unitários do comportamento do domínio;
* testes de integração com PostgreSQL;
* testes de integração com SQS;
* testes de autenticação com Keycloak;
* testes de equivalência HTTP/SQS;
* testes de concorrência e múltiplos processos;
* testes de entrega duplicada;
* testes de idempotência persistente e restart;
* testes de concorrência do outbox;
* testes de retry e recovery;
* testes de pending references;
* testes de reconciliation;
* go test -race.

Os cenários obrigatórios incluem cinquenta requests duplicados concorrentes
para uma operação com exatamente um movimento financeiro; os dois BETs
concorrentes de 80.00 contra uma wallet de 100.00; wallets independentes
progredindo em paralelo; pelo menos três processos independentes da aplicação;
interrupção do consumer após o commit e antes da deleção da mensagem;
publishers de outbox concorrentes; referências tardias de REFUND ou ROLLBACK;
restart da aplicação; e a mesma operação atravessando HTTP e SQS. Testes de
integração usam PostgreSQL real, o IdP e containers LocalStack ou MiniStack
quando aplicável.

PostgreSQL, SQS e o IdP não devem ser substituídos integralmente por mocks em
seus testes de integração.

Os Quality Gates obrigatórios são:

~~~bash
gofmt
go test ./...
go test -race ./...
go vet ./...
git diff --check
~~~

O comando de formatação deve atuar somente nos arquivos Go aplicáveis. Gates
adicionais aplicáveis incluem migrations up/down, PostgreSQL real, LocalStack
SQS, autenticação e autorização Keycloak, shutdown, multi-instance,
idempotência, recovery e testes de startup limpo do Docker Compose.

Uma feature só está completa quando implementação, imposição de invariantes,
testes, gates relevantes passando, revisão de código sensível a race e
documentação obrigatória estão presentes. Compilação sozinha é insuficiente.
Um loop permanece pendente de revisão humana até que sua evidência seja
revisada e aprovada; nenhum loop posterior é iniciado automaticamente.

---

## Gaps abertos da especificação

Esta seção é não normativa. Cada item registra uma decisão necessária que as
fontes inspecionadas não determinam suficientemente. Nenhuma opção abaixo é
selecionada.

### GAP-WAGER-001 — Semântica financeira por operação

* Decisão ausente: a interação e a cardinalidade entre tipos de reversão
  distintos, incluindo se uma transação referenciada pode receber mais de uma
  reversão bem-sucedida de tipos diferentes e como essas combinações são
  restringidas.
* Por que é necessário: CHALLENGE.md define a matriz de operações, a proteção
  contra duplicatas do mesmo tipo e a necessidade de documentar combinações
  entre tipos, mas não seleciona a policy entre tipos.
* Fontes inspecionadas: seções 7–8 de CHALLENGE.md; SPEC.md restaurada;
  Financial Rules de AGENTS.md; seção 15 de ARCHITECTURE.md; Loop 3 de
  TASKS.md.
* Opções possíveis, não selecionadas: uma reversão bem-sucedida por referência,
  uma por tipo de reversão ou uma combinação de tipos explicitamente restrita.

### GAP-REVERSAL-001 — Cardinalidade de reversões entre tipos

* Decisão ausente: se uma referência processada pode ter uma reversão
  bem-sucedida no total ou reversões bem-sucedidas separadas por tipo aplicável,
  e como tipos de reversão distintos interagem.
* Por que é necessário: a proibição de duas reversões bem-sucedidas do mesmo
  tipo aplicável não determina a cardinalidade entre tipos distintos.
* Fontes inspecionadas: seção 7 de CHALLENGE.md; Financial Rules de AGENTS.md;
  seção 15 de ARCHITECTURE.md; Loop 3 de TASKS.md; migration e repository de
  reversão apenas como evidência auxiliar.
* Opções possíveis, não selecionadas: uma reversão bem-sucedida por referência,
  uma por tipo de reversão ou uma combinação de tipos explicitamente restrita.

### GAP-REFERENCE-002 — Validação do game referenciado

* Decisão ausente: se a identidade do game de uma reversão deve coincidir com a
  identidade do game da transação referenciada.
* Por que é necessário: a transação pode carregar identidade de game, mas as
  decisões existentes de validação da referência não determinam se ela
  participa do matching da referência.
* Fontes inspecionadas: seção 7 de CHALLENGE.md; AGENTS.md; seção 15 de
  ARCHITECTURE.md; Loop 3 de TASKS.md; implementação atual apenas como
  evidência auxiliar.
* Opções possíveis, não selecionadas: exigir identidade de game coincidente,
  ignorá-la na validação da referência ou aplicar uma relação definida
  separadamente.

### GAP-IDEMPOTENCY-001 — Resultado do replay e detalhes internos de identidade

* Decisão ausente de CHALLENGE.md: detalhes além do contrato explícito de
  idempotência, incluindo o escopo exato no banco de cada identidade, a
  representação física do snapshot de resultado persistido e o comportamento
  de edge cases para estados ou conflitos não cobertos pelo contrato primário.
  A fonte primária exige que algoritmo de hash, campos de negócio e
  normalizações sejam documentados, mas não os seleciona; qualquer escolha
  arquitetural posterior para esses detalhes não é requisito restaurado do
  desafio.
* Por que é necessário: CHALLENGE.md fixa Idempotency-Key, JSON canônico
  determinístico, inputs excluídos, resultados da mesma chave, comportamento
  da identidade externa e snapshot do saldo original, mas não define todos os
  detalhes de storage, hashing ou edge cases.
* Fontes inspecionadas: seções 9–10 de CHALLENGE.md; Idempotency de AGENTS.md;
  seções 11–12 de ARCHITECTURE.md; Loop 4 de TASKS.md; implementação atual
  apenas como evidência auxiliar.
* Opções possíveis, não selecionadas por esta SPEC: snapshot de resultado
  versionado, registro completo de application result, regras de replay por
  state ou escolha arquitetural posterior para os detalhes de hashing
  delegados pelo desafio.

### GAP-HTTP-001 — Schemas wire HTTP

* Decisão ausente: schemas completos para endpoints e campos não totalmente
  definidos pelos exemplos primários, incluindo opcionalidade, ordenação de
  listas e detalhes restantes de response.
* Por que é necessário: CHALLENGE.md define os nomes wire obrigatórios e os
  exemplos principais, mas não fornece um schema completo para cada endpoint.
* Fontes inspecionadas: seção 9 de CHALLENGE.md; SPEC.md existente; seções
  20–25 de ARCHITECTURE.md; AGENTS.md; Loop 6 de TASKS.md; tipos de resultado
  da aplicação dos Loops 0–5 como evidência auxiliar.
* Opções possíveis, não selecionadas: contrato OpenAPI, schemas JSON por
  endpoint ou contrato de API versionado separadamente.

### GAP-HTTP-002 — Status codes HTTP e envelope de erro

* Decisão ausente: mapeamento de status code e formato do error body para
  validação, rejeição de negócio, conflito de idempotência, replay indisponível,
  não encontrado, falha de autenticação, falha de autorização e falha de
  dependência.
* Por que é necessário: erros tipados da aplicação não definem por si só um
  contrato de transporte.
* Fontes inspecionadas: Context and Errors de AGENTS.md; seções 20–22 de
  ARCHITECTURE.md; contrato de erros HTTP do Loop 6 de TASKS.md; SPEC.md
  existente.
* Opções possíveis, não selecionadas: envelope problem-details, envelope de
  erros do projeto ou responses específicas por endpoint.

### GAP-AUTH-001 — Claim de identidade do provider

* Decisão ausente: o claim OIDC exato ou mapeamento de claim que fornece
  providerId, incluindo o comportamento quando está ausente ou ambíguo.
* Por que é necessário: o isolamento de provider não pode ser testado ou
  imposto precisamente sem um mapeamento determinístico de identidade.
* Fontes inspecionadas: seções 2 e 9 de CHALLENGE.md; Authentication and
  Authorization de AGENTS.md; seções 21–22 de ARCHITECTURE.md; Loops 6 e 10 de
  TASKS.md; configuração local do realm Keycloak.
* Opções possíveis, não selecionadas: sub, um claim de provider dedicado ou
  um mapeamento de claim namespaced.

### GAP-AUTH-002 — Roles, scopes e audience OIDC

* Decisão ausente: roles obrigatórias, scopes, audience, detalhes de validação
  de issuer/JWKS, policy de clock-skew e matriz de autorização para cada
  endpoint.
* Por que é necessário: autenticação e autorização são garantias separadas, e
  um token válido sozinho não estabelece permissão.
* Fontes inspecionadas: seção 2 de CHALLENGE.md; Authentication and
  Authorization de AGENTS.md; seções 21–22 e 31 de ARCHITECTURE.md; Loop 6 de
  TASKS.md; configuração local do realm Keycloak.
* Opções possíveis, não selecionadas: acesso baseado em role, acesso baseado
  em scope ou combinação com validação de audience.

### GAP-HTTP-003 — Policy de acesso à abertura de wallet e health

* Decisão ausente: a identidade interna concreta e o mecanismo de
  transporte/autorização usados para operações de wallet, além dos detalhes de
  binding/exposição de rede das rotas públicas de health.
* Por que é necessário: CHALLENGE.md exige que operações de wallet sejam
  internas e health seja público, mas não seleciona identidade, fronteira ou
  mecanismo de autorização concretos.
* Fontes inspecionadas: seções 2 e 9 de CHALLENGE.md; Authentication and
  Authorization de AGENTS.md; seções 21–25 de ARCHITECTURE.md; Loops 6 e 11 de
  TASKS.md; SPEC.md existente.
* Opções possíveis, não selecionadas: identidade de caller interno, identidade
  administrativa ou fronteira de transporte explicitamente definida.

### GAP-HEALTH-001 — Contrato de readiness

* Decisão ausente: status/body exatos de readiness, timeout e intervalo de
  probe, comportamento de startup enquanto migrations ou provisioning estão
  incompletos e detalhes de probe além de PostgreSQL e SQS.
* Por que é necessário: CHALLENGE.md fixa PostgreSQL e SQS como dependências de
  readiness, mas a orquestração do deployment ainda precisa de response e
  policy de startup determinísticas.
* Fontes inspecionadas: seções 9 e 12 de CHALLENGE.md; seção 25 de
  ARCHITECTURE.md; Loops 6 e 10 de TASKS.md; health checks do Docker Compose;
  SPEC.md existente.
* Opções possíveis, não selecionadas: somente probes de dependência, uma
  state machine de startup ou uma health response com detalhes por dependência.

### GAP-SQS-001 — Contrato de mensagem SQS

* Decisão ausente: campos opcionais do envelope/atributos, regras exatas de
  validação e a policy concreta de FIFO para MessageGroupId e
  MessageDeduplicationId.
* Por que é necessário: CHALLENGE.md fixa os nomes das filas, a disposição
  FIFO/DLQ, o envelope e os campos principais do comando, mas deixa esses
  detalhes de implementação em aberto.
* Fontes inspecionadas: seção 10 de CHALLENGE.md; HTTP e SQS de AGENTS.md;
  seções 17–20 e 29 de ARCHITECTURE.md; Loop 7 de TASKS.md; bootstrap da fila
  LocalStack.
* Opções possíveis, não selecionadas: um envelope de comando direto, um
  envelope de evento versionado ou um adapter de envelope definido pelo provider.

### GAP-SQS-002 — Semântica de duplicata e conclusão da inbox

* Decisão ausente: comportamento para hash divergente, record de completion
  incompleto/nulo e a máquina exata de estados de claim/recovery para um
  consumer interrompido.
* Por que é necessário: CHALLENGE.md fixa identidade durável, hash do payload,
  completion vinculada à transação e supressão de duplicatas, mas não escolhe
  a policy de tratamento desses records excepcionais.
* Fontes inspecionadas: seções 6.5 e 10 de CHALLENGE.md; Inbox de AGENTS.md;
  seção 17 de ARCHITECTURE.md; Loop 7 de TASKS.md; migration e repository da
  inbox; SPEC.md existente.
* Opções possíveis, não selecionadas: rejeitar hash divergente, tratar o
  message ID como autoritativo ou manter estados explícitos de processamento
  da inbox.

### GAP-REFERENCE-001 — Policy de pending references

* Decisão ausente: ownership e claiming do worker, agenda exata de polling e
  fórmula/valores de backoff, valores de retry/TTL e detalhes do tratamento de
  uma transação referenciada pendente ou sem sucesso.
* Por que é necessário: CHALLENGE.md fixa backoff exponencial reiniciável e
  exhaustion como REJECTED com código estável de referência não encontrada e
  evento de rejeição, mas não escolhe a agenda concreta ou a policy de ownership.
* Fontes inspecionadas: seções 7 e 13 de CHALLENGE.md; transações e recovery de
  AGENTS.md; seção 16 de ARCHITECTURE.md; Loop 8 de TASKS.md; defaults atuais
  do ambiente como evidência auxiliar.
* Opções possíveis, não selecionadas: attempts limitados, TTL baseado em tempo
  ou um scheduler durável de trabalho pendente.

### GAP-OUTBOX-001 — Policy de publicação e recovery do outbox

* Decisão ausente: destino/protocolo, definição de sucesso da publicação,
  valores exatos de retry/backoff, duração da lease de claim, limite de
  abandono e critérios de transição para uma falha terminal.
* Por que é necessário: CHALLENGE.md fixa criação atômica, identidade estável,
  múltiplos publishers, claiming, retries, backoff, recovery de trabalho
  abandonado e as janelas de publicação relevantes, mas não escolhe essas
  policies concretas.
* Fontes inspecionadas: seção 11 de CHALLENGE.md; Outbox de AGENTS.md;
  seções 18–19 e 29 de ARCHITECTURE.md; Loop 9 de TASKS.md; migration e
  repository de outbox.
* Opções possíveis, não selecionadas: publicação em SQS, outro broker ou um
  callback da aplicação; recovery de claim baseado em lease ou timestamp.

### GAP-EVENT-001 — Semântica de payload e metadata de eventos

* Decisão ausente: valores de version, regras de geração de
  correlation/causation, garantias de ordenação, estratégia de geração de
  event-ID e schemas adicionais de data além dos payloads de evento definidos
  explicitamente.
* Por que é necessário: CHALLENGE.md fixa os quatro tipos de evento, seus
  principais triggers, envelope, snapshot imutável, timestamps UTC e
  representação monetária em string, mas não define toda a policy de metadata.
* Fontes inspecionadas: seções 11 e 12 de CHALLENGE.md; seção 19 de
  ARCHITECTURE.md; transações e outbox de AGENTS.md; Loop 9 de TASKS.md;
  construção atual de eventos como evidência auxiliar.
* Opções possíveis, não selecionadas: um schema versionado por tipo de evento,
  um schema de snapshot comum ou versões de payload específicas do consumer.

### GAP-FAILURE-001 — Classificação de falhas de infraestrutura

* Decisão ausente: critérios exatos para falha transitória versus permanente,
  ownership do retry, policy de retry de deadlock/serialization, detalhes do
  registro durável de FAILED e resultado exposto para uma operação interrompida.
* Por que é necessário: CHALLENGE.md exige ausência de mutação financeira
  parcial e distingue retry/backoff transitório de tratamento permanente ou por
  exhaustion de DLQ, mas não define toda classificação ou regra de ownership.
* Fontes inspecionadas: seções 3 e 10 de CHALLENGE.md; Transactions e
  Context/Errors de AGENTS.md; seções 14 e 29 de ARCHITECTURE.md; nota do Loop 3
  e Loops 7, 9 e 11 de TASKS.md; serviço financeiro atual como evidência auxiliar.
* Opções possíveis, não selecionadas: retry e depois falha, estado de falha
  reconciliado por operador ou record de falha pertencente à inbox/outbox.

### GAP-RECON-001 — Contrato wire e remediation de reconciliation

* Decisão ausente: mapeamento HTTP de status/erro, mecanismo concreto de
  autorização e qualquer workflow de remediation para uma divergência detectada.
* Por que é necessário: CHALLENGE.md fixa os campos da response de
  reconciliation, a diferença stored-minus-calculated, o comportamento
  read-only e response/log/metric de divergência, mas não autoriza um workflow
  de correção nem define os detalhes restantes de transporte.
* Fontes inspecionadas: seção 12 de CHALLENGE.md; SPEC.md existente; seções 13,
  26 e 29 de ARCHITECTURE.md; Loops 3 e 6 de TASKS.md; use case atual de
  reconciliation como evidência auxiliar.
* Opções possíveis, não selecionadas: reporting read-only, comando interno de
  remediation ou workflow de correção aprovado separadamente.

### GAP-MONEY-001 — Conjunto de currencies e policy de unidade menor

* Decisão ausente: estratégia concreta de validação dos códigos ISO 4217, se
  todo código ISO 4217 é suportado ou somente um subconjunto definido e como
  são tratadas currencies cujas unidades menores ISO não possuem duas casas
  decimais.
* Por que é necessário: a representação int64 fixa de duas casas decimais é
  explícita para o cenário atual de BRL, mas não define sozinha todo o domínio
  de currencies.
* Fontes inspecionadas: seção 6.1 de CHALLENGE.md; seções 2–3 da SPEC.md
  existente; Money de AGENTS.md; seção 6 de ARCHITECTURE.md; validação atual de
  Money como evidência auxiliar.
* Opções possíveis, não selecionadas: suporte somente a BRL, conjunto
  configurado de currencies com duas casas decimais ou metadata de currency
  com unidades menores por currency.

### GAP-LIFECYCLE-001 — Policy de shutdown do trabalho em andamento

* Decisão ausente: deadline exato de shutdown gracioso, comportamento de
  extensão do visibility-timeout e ordem de parada dos workers no deadline.
* Por que é necessário: CHALLENGE.md exige interromper aceitação/polling e
  finalizar ou liberar com segurança o trabalho em andamento dentro de um
  deadline, mas não escolhe o deadline concreto ou a policy dos workers.
* Fontes inspecionadas: seções 4 e 13 de CHALLENGE.md; Context de AGENTS.md;
  seção 24 de ARCHITECTURE.md; Loops 7, 9 e 11 de TASKS.md; seções 8, 11 e 14
  de LOOPING.md.
* Opções possíveis, não selecionadas: drain limitado, cancelamento imediato
  com redelivery ou deadlines por worker.
