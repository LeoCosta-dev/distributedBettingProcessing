# Looping Engineering Runbook

Este documento define o protocolo obrigatório para execução dos loops descritos em `TASKS.md`.

O objetivo é permitir execução incremental assistida por agente, mantendo cada loop isolado, verificável e sujeito a revisão humana antes do próximo checkpoint.

---

## 1. Execution Protocol

Execute somente o próximo loop pendente definido em `TASKS.md`.

### Context Loading

Antes de qualquer implementação:

1. leia `AGENTS.md`;
2. leia `SPEC.md`;
3. leia `ARCHITECTURE.md`;
4. leia `TASKS.md`;
5. inspecione o estado atual do repositório;
6. identifique o próximo loop pendente;
7. identifique as invariantes e decisões arquiteturais relacionadas ao loop.

Não implemente antes de compreender o contexto existente.

---

## 2. Scope Control

Trabalhe exclusivamente no escopo do próximo loop pendente.

Não:

- avance para loops posteriores;
- implemente funcionalidades futuras apenas por conveniência;
- redesenhe a arquitetura sem necessidade demonstrável;
- altere decisões já documentadas silenciosamente;
- enfraqueça invariantes existentes;
- marque tarefas como concluídas sem evidência;
- faça commit;
- faça push.

Se uma alteração fora do loop for indispensável para sua conclusão, explique a necessidade antes de ampliar o escopo.

---

## 3. Sources of Truth

Durante a execução, considere:

### `AGENTS.md`

Define as regras permanentes de engenharia que devem ser respeitadas pelo agente.

### `SPEC.md`

Define o comportamento esperado do sistema e suas invariantes funcionais.

É a principal fonte para determinar **o que precisa ser verdadeiro**.

### `ARCHITECTURE.md`

Define as decisões arquiteturais já tomadas e como as garantias da especificação serão implementadas.

Não redesenhe essas decisões sem necessidade técnica demonstrável.

### `TASKS.md`

Define a decomposição incremental do trabalho.

É a fonte para determinar:

- qual é o próximo loop;
- qual é o escopo atual;
- quais verificações são necessárias;
- quais tarefas já foram concluídas.

### `LOOPING.md`

Define o protocolo de execução.

Este documento controla **como cada loop deve ser executado e validado**.

---

## 4. Engineering Rules

Durante a implementação:

- siga `AGENTS.md`;
- trate `SPEC.md` como fonte das invariantes funcionais;
- siga as decisões registradas em `ARCHITECTURE.md`;
- use `TASKS.md` como controle de progresso;
- preserve separação entre domínio, aplicação e infraestrutura;
- prefira mudanças pequenas e verificáveis;
- não enfraqueça garantias existentes para fazer testes passarem;
- não introduza abstrações sem necessidade concreta;
- não implemente antecipadamente funcionalidades pertencentes a loops futuros.

Quando uma decisão não estiver explicitamente coberta pela documentação:

1. escolha a solução mais simples compatível com as invariantes existentes;
2. evite ampliar desnecessariamente o escopo;
3. registre a decisão no relatório final;
4. sinalize a decisão para revisão humana quando ela puder impactar loops posteriores.

---

## 5. Financial Invariants

As garantias financeiras possuem prioridade sobre conveniência de implementação.

Durante qualquer loop que toque comportamento financeiro:

- nunca utilize ponto flutuante para dinheiro;
- nunca permita saldo negativo;
- preserve aritmética monetária exata;
- preserve idempotência persistente;
- preserve atomicidade das operações financeiras;
- preserve imutabilidade do ledger;
- não produza movimento financeiro duplicado;
- não publique eventos antes do commit da transação correspondente;
- não dependa exclusivamente de memória local para idempotência;
- não dependa de locks locais para concorrência financeira;
- considere execução concorrente;
- considere retries;
- considere mensagens duplicadas;
- considere múltiplas instâncias;
- considere interrupção abrupta;
- considere indisponibilidade temporária de dependências.

Uma solução correta em uma única instância não deve ser considerada suficiente quando a especificação exige comportamento distribuído.

---

## 6. Implementation

Implemente somente o código de produção e os testes necessários para satisfazer integralmente o loop atual.

Cada garantia marcada como concluída deve possuir evidência verificável por uma ou mais das seguintes formas:

- teste automatizado;
- constraint do banco de dados;
- índice;
- chave de unicidade;
- transação explícita;
- comportamento explicitamente implementado;
- teste de integração;
- teste de concorrência;
- teste de recuperação;
- inspeção reproduzível.

Não considere compilação isoladamente como evidência suficiente de corretude.

Não considere apenas a existência de código como evidência de que uma garantia foi satisfeita.

---

## 7. Database Guarantees

Quando o loop envolver PostgreSQL, migrations ou persistência:

- utilize migrations versionadas;
- preserve capacidade de aplicar migrations;
- preserve capacidade de rollback quando definido pelo projeto;
- utilize constraints para invariantes que possam ser protegidas pelo banco;
- utilize índices e unicidade quando fizerem parte das garantias do sistema;
- torne transações explícitas quando houver atomicidade entre múltiplas escritas;
- não transfira para memória garantias que precisam sobreviver a restart;
- considere concorrência entre múltiplas instâncias;
- valide migrations contra PostgreSQL real quando aplicável.

Garantias críticas não devem depender exclusivamente de validação na camada de aplicação quando também puderem ser protegidas pelo banco.

---

## 8. Messaging Guarantees

Quando o loop envolver SQS, inbox, outbox ou processamento assíncrono:

assuma sempre semântica **at-least-once**.

Considere explicitamente:

- mensagens duplicadas;
- retries;
- redelivery;
- interrupção depois do commit e antes do ack/delete;
- múltiplos consumidores;
- múltiplos publishers;
- indisponibilidade temporária do broker;
- restart da aplicação.

Nunca trate entrega única como premissa de corretude.

---

## 9. Authentication and Authorization

Quando o loop envolver autenticação ou autorização:

- utilize o IdP externo definido pela arquitetura;
- não implemente armazenamento próprio de senha;
- não implemente emissão própria de credenciais quando isso substituir o IdP;
- valide autenticação;
- valide autorização;
- preserve isolamento entre providers;
- não considere autenticação válida como autorização suficiente.

Testes devem demonstrar tanto acesso permitido quanto acesso proibido quando aplicável.

---

## 10. Testing Strategy

Os testes devem demonstrar invariantes, não apenas aumentar cobertura.

Priorize testes capazes de provar comportamento observável e propriedades importantes do sistema.

Quando aplicável, cubra:

- happy path;
- rejeições de negócio;
- entradas inválidas;
- idempotência;
- duplicação;
- concorrência;
- overflow;
- saldo insuficiente;
- transições de estado;
- constraints;
- rollback;
- retries;
- recovery;
- múltiplas instâncias;
- falhas temporárias;
- shutdown.

Prefira testes table-driven quando houver matrizes de comportamento ou estados.

Não altere uma garantia correta apenas para simplificar um teste.

---

## 11. Quality Gates

Antes de declarar o loop implementado, execute todos os Quality Gates aplicáveis.

### Gates obrigatórios

```bash
gofmt
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

O comando de formatação deve atuar somente nos arquivos Go aplicáveis.

### Gates adicionais

Quando aplicável ao loop, execute também:

- testes unitários específicos;
- testes de integração;
- testes com PostgreSQL real;
- migrations `up`;
- migrations `down`;
- testes com SQS/LocalStack;
- testes com Keycloak;
- testes de autenticação;
- testes de autorização;
- testes de concorrência;
- testes de idempotência;
- testes de recuperação;
- testes de shutdown;
- testes multi-instância;
- reconstrução limpa com Docker Compose.

Uma falha em qualquer gate aplicável deve ser corrigida antes da conclusão da execução.

Não marque uma tarefa como concluída quando o gate que demonstra sua garantia não puder ser executado.

Se algum gate não puder ser executado por limitação do ambiente, informe explicitamente:

- qual gate não foi executado;
- por que não foi executado;
- qual garantia permanece sem verificação.

---

## 12. Verification Before Completion

Antes de atualizar `TASKS.md`, confronte novamente a implementação com o escopo do loop.

Para cada item que pretende marcar como concluído, pergunte:

1. o comportamento foi realmente implementado?
2. existe evidência verificável?
3. a garantia continua válida sob concorrência quando aplicável?
4. a garantia continua válida após restart quando aplicável?
5. a implementação respeita `SPEC.md`?
6. a implementação respeita `ARCHITECTURE.md`?
7. algum teste apenas aparenta provar algo que a implementação não garante estruturalmente?

Somente depois dessa revisão atualize o checklist.

---

## 13. TASKS.md

Atualize `TASKS.md` somente após as verificações.

Uma tarefa somente pode receber `[x]` quando houver evidência de que sua garantia foi implementada e verificada.

Não marque um loop como concluído apenas porque o código correspondente existe.

Não marque como concluída uma garantia cuja validação relevante não tenha sido executada.

Se uma tarefa estiver parcialmente implementada, ela deve permanecer pendente.

---

## 14. Final Report

Ao terminar a implementação do loop, apresente um relatório para revisão humana.

### Alterações

Informe:

- arquivos criados;
- arquivos modificados;
- comportamento implementado;
- testes adicionados ou alterados.

### Decisões

Informe:

- decisões técnicas tomadas durante o loop;
- interpretações necessárias da especificação;
- trade-offs introduzidos;
- qualquer decisão não explicitamente prevista pela documentação.

### Verification

Informe individualmente o resultado de:

- testes específicos do loop;
- `gofmt`;
- `go test ./...`;
- `go test -race ./...`;
- `go vet ./...`;
- `git diff --check`;
- demais Quality Gates aplicáveis.

Não use apenas "todos os testes passaram".

Informe quais categorias de verificação foram efetivamente executadas.

### Risks / Review Points

Informe explicitamente:

- pontos que merecem revisão humana;
- garantias difíceis de demonstrar;
- limitações conhecidas;
- decisões que podem impactar loops posteriores;
- comportamento ainda não verificado;
- qualquer divergência ou ambiguidade encontrada entre documentação e implementação.

Não esconda riscos apenas porque os testes passaram.

---

## 15. Human Review Gate

Após apresentar o relatório:

**PARE.**

Não:

- faça commit;
- faça push;
- inicie o próximo loop;
- implemente preventivamente itens de loops futuros;
- considere aprovação implícita;
- transforme automaticamente o resultado em checkpoint.

Aguarde revisão humana.

O estado esperado neste momento é:

> **IMPLEMENTED — PENDING HUMAN REVIEW**

---

## 16. Corrections After Human Review

Caso a revisão humana solicite correções:

1. trabalhe exclusivamente nas correções solicitadas;
2. não avance para o próximo loop;
3. não aproveite a correção para ampliar o escopo;
4. atualize os testes necessários;
5. execute novamente os Quality Gates afetados;
6. execute novamente os gates globais obrigatórios;
7. apresente novo relatório;
8. pare novamente para revisão humana.

Uma implementação que já passou pelos Quality Gates ainda pode conter erro semântico.

Quality Gates automatizados não substituem revisão das invariantes.

---

## 17. Git Checkpoint

O agente executor do loop não deve criar commit automaticamente.

O checkpoint Git ocorre somente após aprovação humana.

Quando explicitamente solicitado após aprovação:

1. inspecione `git status`;
2. inspecione o diff;
3. confirme que apenas alterações pertencentes ao loop serão incluídas;
4. execute os Quality Gates necessários;
5. utilize Conventional Commits;
6. crie um único checkpoint coerente para o loop;
7. informe hash e mensagem;
8. não faça push sem solicitação explícita.

Não inclua alterações não relacionadas no checkpoint.

---

## 18. Definition of Loop Completion

Um loop somente está completamente concluído quando:

1. seu escopo foi implementado;
2. suas invariantes foram preservadas;
3. seus testes específicos passaram;
4. os Quality Gates obrigatórios passaram;
5. os Quality Gates adicionais aplicáveis passaram;
6. `TASKS.md` reflete somente garantias verificadas;
7. decisões e riscos foram reportados;
8. houve revisão humana;
9. eventuais correções da revisão foram concluídas;
10. houve aprovação humana;
11. foi criado o checkpoint Git correspondente.

Antes da aprovação humana, o loop deve ser considerado:

> **IMPLEMENTED — PENDING HUMAN REVIEW**

Após aprovação e checkpoint:

> **COMPLETED**

Somente então o próximo loop pode ser iniciado.

---

## 19. Next Loop

Depois que o checkpoint do loop atual tiver sido criado, pare.

Não inicie automaticamente o próximo loop no mesmo run.

O próximo loop deve começar através de uma nova execução explícita deste runbook.

A instrução mínima para iniciar uma nova execução é:

> Leia `LOOPING.md` e execute o próximo loop pendente.

---

## Core Principle

O objetivo do Looping Engineering neste projeto não é maximizar a quantidade de código produzida por um agente.

O objetivo é transformar uma especificação em pequenas unidades de execução que possam ser:

**implementadas → verificadas → revisadas → corrigidas → aprovadas → versionadas**

antes que uma nova unidade de complexidade seja adicionada ao sistema.

Autonomia de execução não implica autoridade sobre corretude.

A implementação do agente é uma proposta até que as invariantes tenham sido verificadas e o loop tenha passado pela revisão humana.