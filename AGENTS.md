# AGENTS.md

## Projeto

Desafio de backend para processamento distribuído de transações de apostas.

O sistema é um backend financeiro. Correção, consistência, idempotência e
recuperação de falhas têm prioridade sobre velocidade de implementação ou
complexidade de abstração.

## Princípios de engenharia

1. Preserve a correção financeira acima de tudo.
2. Prefira comportamento explícito e verificável a abstrações engenhosas.
3. Mantenha a lógica de domínio independente de HTTP, SQS, PostgreSQL,
   Keycloak e Uber Fx.
4. Adapters de infraestrutura não devem conter regras de negócio.
5. Invariantes financeiras devem ser impostas tanto pela lógica da aplicação
   quanto por constraints do PostgreSQL quando aplicável.
6. Não introduza dependências a menos que forneçam valor claro.
7. Não implemente funcionalidades especulativas fora dos requisitos do
   desafio.
8. Mantenha as mudanças pequenas e verificáveis de forma independente.

## Regras financeiras

Nunca:

* use `float32` ou `float64` para valores monetários;
* faça rounding silencioso de entrada monetária inválida;
* altere ou apague entradas do ledger;
* atualize o saldo da wallet fora da transação financeira do banco;
* dependa de estado em memória para idempotência;
* dependa da deduplicação FIFO do SQS como mecanismo de idempotência
  financeira;
* use locks locais do processo como mecanismo de concorrência financeira;
* publique um evento de integração antes do commit da transação de origem;
* permita que o saldo de uma wallet fique negativo;
* permita que uma transação financeira processada seja aplicada duas vezes;
* permita duas reversões bem-sucedidas do mesmo tipo aplicável;
* ignore a validação de domínio em um adapter.

## Money

Money deve:

* usar aritmética exata;
* carregar amount e currency;
* usar representação externa fixa com duas casas decimais;
* rejeitar entradas financeiras externas negativas;
* rejeitar notação científica;
* rejeitar escala excessiva;
* rejeitar representações numéricas inválidas;
* rejeitar aritmética entre currencies incompatíveis;
* detectar overflow de inteiro quando `int64` for usado.

A representação externa é:

```json
{
  "amount": "25.00",
  "currency": "BRL"
}
```

## Wallet

Wallet é a raiz do aggregate financeiro.

As mutações de saldo devem ser realizadas atomicamente com a entrada
correspondente do ledger e o estado da transação.

A concorrência da wallet deve ser coordenada no escopo da wallet.

Wallets independentes devem poder progredir concorrentemente.

## Ledger

O ledger é append-only.

Toda mutação efetiva de saldo deve produzir exatamente uma entrada
correspondente no ledger.

As entradas do ledger são imutáveis.

Correções devem criar novas entradas financeiras em vez de modificar entradas
anteriores.

## Idempotência

A idempotência deve sobreviver a restarts do processo.

A aplicação deve distinguir:

1. mesma chave de idempotência + mesmo payload de negócio;
2. mesma chave de idempotência + payload de negócio diferente;
3. mesma identidade de transação externa + chave de idempotência diferente.

Um replay bem-sucedido deve retornar o resultado persistido do processamento
original.

Não reconstrua a resposta de replay a partir do saldo atual da wallet.

## Transações

Alterações financeiras devem usar transações explícitas do PostgreSQL.

A fronteira transacional deve estar visível na implementação do repository ou
da aplicação.

Quando aplicável, os itens a seguir devem fazer commit atomicamente:

* estado da wager transaction;
* saldo da wallet;
* entrada do ledger;
* conclusão do inbox;
* criação do evento de outbox.

## HTTP e SQS

HTTP e SQS devem usar o mesmo use case da aplicação para processamento
financeiro.

Código específico de transporte deve traduzir a entrada em comandos da
aplicação e não deve duplicar regras financeiras.

## Inbox

O processamento de mensagens SQS deve usar registros duráveis de inbox.

A unicidade do inbox deve ser imposta pelo PostgreSQL.

Uma mensagem duplicada não deve fazer a operação financeira executar
novamente.

## Outbox

Eventos de integração devem ser criados transacionalmente com o estado que
descrevem.

A publicação do outbox ocorre de forma assíncrona após o commit.

O worker do outbox deve suportar:

* múltiplos workers/instâncias;
* claiming de registros;
* retries;
* backoff;
* recovery de trabalho abandonado;
* event IDs estáveis.

## Concorrência

A implementação deve funcionar com múltiplos processos independentes da
aplicação.

O cenário exigido é:

* saldo da wallet: `100.00 BRL`;
* duas operações `BET` diferentes;
* ambas no valor de `80.00 BRL`;
* submetidas concorrentemente.

Resultado esperado:

* exatamente uma transação é `PROCESSED`;
* exatamente uma transação é rejeitada por saldo insuficiente;
* saldo final é `20.00 BRL`;
* existe exatamente um débito no ledger.

A implementação não deve depender da memória do processo Go para garantir
esse resultado.

## Autenticação e autorização

Endpoints de negócio exigem autenticação OAuth 2.0/OIDC real.

A identidade autenticada determina o `providerId` autorizado.

Um provider não deve acessar transações de outro provider, inclusive replays.

Operações internas de abertura de wallet não devem ser expostas como
operações de provider.

Nunca implemente armazenamento de senha ou emissão customizada de tokens.

## Uber Fx

Use Uber Fx para composição da aplicação e gerenciamento de lifecycle.

Use:

* `fx.Module`;
* `fx.Provide`;
* `fx.Invoke`;
* constructors;
* `fx.Lifecycle`.

O domínio não deve importar Uber Fx.

## Context e erros

Todas as operações de I/O devem receber `context.Context`.

Respeite cancelamento e timeouts.

Erros de negócio devem ser tipados ou classificáveis com `errors.Is` /
`errors.As`.

Não use `panic` para falhas de validação de negócio.

## Testes

Testes devem verificar comportamento, não detalhes de implementação.

A verificação obrigatória inclui:

* testes unitários;
* testes de integração com PostgreSQL;
* testes de integração com SQS;
* testes de autenticação com Keycloak;
* testes de concorrência;
* entrega duplicada;
* equivalência HTTP/SQS;
* concorrência do outbox;
* retry/recovery;
* pending references;
* restart da aplicação;
* `go test -race`.

Não substitua integralmente PostgreSQL, SQS e o IdP por mocks nos testes de
integração.

## Fluxo de desenvolvimento

Trabalhe em slices verticais pequenos.

Para cada slice:

1. inspecione a especificação existente;
2. identifique a invariante relevante;
3. implemente a menor mudança coerente;
4. adicione ou atualize a verificação;
5. execute formatação e testes relevantes;
6. inspecione as falhas;
7. corrija a causa raiz;
8. atualize a documentação se uma decisão arquitetural mudou;
9. somente então avance para o próximo slice.

Não implemente melhorias não relacionadas enquanto trabalha em um slice.

## Definição de concluído

Uma feature não é considerada completa apenas porque o código compila.

Um slice está completo quando:

* a implementação existe;
* as invariantes relevantes são impostas;
* existem testes relevantes;
* os testes passam;
* código sensível a race foi considerado;
* a documentação foi atualizada quando necessário;
* nenhum requisito conhecido foi ignorado silenciosamente.

## Comportamento do agente

Antes de uma mudança arquitetural significativa:

* inspecione `SPEC.md`;
* inspecione `ARCHITECTURE.md`;
* inspecione `TASKS.md`;
* explique brevemente a mudança pretendida;
* verifique que ela não viola uma decisão existente.

Se requisitos entrarem em conflito, não escolha silenciosamente. Registre o
conflito em `TASKS.md` ou `ARCHITECTURE.md` e resolva-o explicitamente.

Não reescreva código funcional apenas por preferência de estilo.

Não adicione abstrações sem um caso de uso concreto.

Não alegue que uma garantia está implementada sem um teste ou constraint de
banco de dados que a demonstre.
