# Relatório da fase 6

Fase 6 concluída em 06/10/2026, preservando as fases 0–5. A suíte completa foi executada com
PostgreSQL e SQS LocalStack reais. Este relatório distingue o trabalho desta fase das alterações
anteriores já presentes no working tree, inclusive arquivos ainda não versionados.

1. **Arquivos criados nesta fase:** `.gitattributes`; `infra/localstack/Dockerfile`;
   `infra/localstack/10-queues.py`; `internal/adapter/sqs/{client,contract,consumer,consumer_test}.go`;
   `internal/adapter/postgres/{inbox,sqs_integration_test}.go`;
   `internal/application/financial/{delivery,delivery_test}.go`;
   `internal/application/sqs_consumer.go`; `internal/config/sqs_test.go`;
   `docs/PHASE6_OPERATIONS.md`; `docs/PHASE6_REPORT.md`.

2. **Arquivos alterados nesta fase:** `.env.example`, `docker-compose.yml`, `go.mod`, `go.sum`,
   `docs/ARCHITECTURE.md`, `internal/application/app.go`, `internal/application/app_test.go`,
   `internal/config/config.go`, `internal/ports/persistence.go`,
   `internal/application/financial/service_test.go`, `internal/adapter/postgres/repositories_test.go`.
   As mudanças anteriores no domínio Wallet e nas migrations foram preservadas.

3. **Dependências:** AWS SDK Go v2 core `v1.47.1`, config `v1.33.7`, SQS `v1.52.2`.
   Dependências transitivas do SDK estão fixadas em `go.mod`/`go.sum`; nenhum SDK AWS foi importado
   pelo domínio ou pela application financeira.

4. **Compose:** PostgreSQL e seu volume preservados; LocalStack `4.6.0` com build mínimo do script
   de inicialização, porta 4566, credenciais dummy e healthcheck verificando ambas as filas.
   A aplicação segue pelo entrypoint Go/Fx no host, conforme comandos operacionais.

5. **Filas:** `wager-transactions.fifo` e `wager-transactions-dlq.fifo`, ambas FIFO, dedup explícita;
   redrive principal→DLQ, limite 5 recebimentos, retenção DLQ 14 dias e permissão restrita à origem.

6. **Contrato:** envelope `schemaVersion=1`, messageId durável, identidade provider/external,
   player/wallet/type, `money.amount` string decimal, currency, round, referência para reversões,
   correlationId, causationId e occurredAt. JSON inválido, campos extras e tipos não suportados
   são rejeitados. Nenhum cálculo monetário usa float.

7. **MessageGroupId:** `wallet:` + SHA-256 hexadecimal da walletId; ordem por wallet e concorrência
   entre wallets. Consumer valida a correspondência grupo/wallet e mantém ordem no lote recebido.

8. **MessageDeduplicationId:** `delivery:` + SHA-256 hexadecimal do messageId do envelope.
   Produtores devem usar IDs únicos no escopo da fila/consumer e preservá-los em retries.
   A janela de dedup do FIFO não é a garantia financeira.

9. **Identidade Inbox:** `(consumerName, envelope.messageId)`, nunca receiptHandle nem o MessageId
   atribuído pela AWS. O nome do consumer identifica a versão lógica do consumidor.

10. **Canonical hash:** SHA-256 de envelope tipado em ordem fixa, Money com duas casas e timestamp
    UTC; inclui metadata do envelope e exclui metadata de recebimento AWS. Testes confirmam
    equivalência de whitespace, ordem JSON, representação decimal e offset de horário.

11. **Inbox + financeiro:** claim persistente, lock Inbox, core financeiro existente, wallet/wager/
    ledger/Outbox e conclusão Inbox dentro da mesma UoW/pgx.Tx. Falha na conclusão do Inbox também
    desfaz os efeitos financeiros; não existe implementação paralela das regras de wagering.

12. **Duplicates:** completed + mesmo hash retorna duplicate e pode receber ACK sem novos efeitos.
    Incomplete + mesmo hash é recuperado sob lock SQL; uma operação externa equivalente preexistente
    apenas conclui Inbox. Ambos os cenários foram testados com componentes reais.

13. **Hash conflict:** erro permanente, sem sobrescrever hash ou aplicar efeitos; mensagem retida
    para redrive. Teste confirmou hash original intacto e mensagem conflitante na DLQ real.

14. **Classificação:** REJECTED persistido e PENDING_REFERENCE são terminais aceitos pelo transporte;
    erros SQL/contexto/timeout são transitórios; JSON/schema/Money/grupo inválidos e conflitos de
    integridade são permanentes. Logs registram classificação, outcome e failureCode sem payload.

15. **Retry/redrive/DLQ:** erros não recebem delete; SQS controla visibility e redrive. Falhas ao
    buscar mensagens têm espera cancelável de 1s. Poison JSON e hash conflict chegaram à DLQ real
    após 3 recebimentos nas filas temporárias de teste; filas locais provisionadas usam limite 5.

16. **Commit-before-delete:** `ProcessDelivery` retorna somente após commit; `Handle` então chama
    DeleteMessage. A aplicação não publica Outbox nem reenvia manualmente mensagens com erro.

17. **Crash após commit:** processamento real sem ACK, visibility expirada e novo consumer/pool;
    redelivery foi duplicate. Saldo 80.00 BRL, version 2, um BET, um ledger de débito e um Inbox
    completed, sem repetir Outbox. Totais incluindo OPENING: 2 wagers, 2 ledgers e 4 eventos.

18. **Crash antes do commit:** triggers reais de falha em Outbox e Inbox completion causaram rollback
    integral. Saldo/version ficaram 100.00/1; apenas OPENING permaneceu; Inbox ficou vazio.
    Redelivery com instância nova aplicou o BET uma única vez.

19. **Delete failure:** SDK real recebeu receipt inválido e falhou depois do commit. Redelivery
    posterior encontrou duplicate e foi deletada; saldo, version, ledger, wager e Outbox permaneceram
    com os mesmos valores do cenário de crash após commit.

20. **50 duplicates:** 50 mensagens reais com dedup IDs AWS diferentes e envelope idêntico, mais
    50 tentativas simultâneas de Process com uma mensagem realmente recebida. Resultado: um único
    débito de 20.00 BRL, saldo 80.00/version 2, um Inbox completed e fila esvaziada.
    A dedup temporária do FIFO foi deliberadamente contornada neste teste.

21. **Três consumers:** cada instância tem serviço, pool e SDK client próprios. Stress passou nas
    três instâncias; teste de wallets independentes confirmou dois commits enquanto a terceira
    aguardava um lock SQL real. Nenhum mutex de processo é autoridade financeira.

22. **HTTP/SQS:** HTTP-first → SQS external_duplicate com transactionId original; SQS-first →
    HTTP com chave nova recebe duplicate-external. Nenhum débito foi reaplicado. Payload financeiro
    diferente sob external identity igual gera conflito permanente no SQS. HTTP foi exercitado via
    caso de uso ProcessHTTPWager; endpoints financeiros finais continuam fora desta fase.

23. **Graceful shutdown:** Fx iniciou/parou consumer com SQS real. Cancelar polling preservou o
    processamento ativo até commit/ACK; cancelamento forçado do contexto ativo desfez Inbox e
    financeiro, reteve a entrega e permitiu recovery. Teste unitário confirma que o tail do lote
    não começa depois do stop. Hooks aguardam o término antes do fechamento do pool.

24. **go test:** `go test ./... -count=1` passou com TEST_DATABASE_URL e TEST_SQS_ENDPOINT definidos.
    Incluiu integrações anteriores, novas integrações, unitários e lifecycle Fx com PostgreSQL/SQS.
    Não foram removidos testes existentes.

25. **go vet:** `go vet ./...` passou, exit code 0.

26. **go test -race:** tentativa com `CGO_ENABLED=1` falhou na compilação porque `gcc` não está
    instalado/no PATH. O race detector não pôde executar neste Windows; isso não é um resultado
    de ausência de data races. Nenhuma instalação de toolchain adicional foi realizada.

27. **Compose config/up:** `docker compose --env-file .env.example config --quiet` passou;
    `docker compose --env-file .env.example up --build -d --wait` passou. Os comandos documentados
    para listar filas, consultar atributos e receber da DLQ também foram executados.

28. **Estado:** `wagering-postgres` e `wagering-localstack` healthy. Filas permanentes vazias na
    validação, atributos FIFO/redrive confirmados. PostgreSQL schema_migrations version 4, dirty false.
    Filas e schemas temporários foram removidos pelos testes.

29. **Migrations:** nenhuma migration nova necessária; migrations 1–4 não foram editadas nesta
    fase. UP→DOWN→UP específico de uma migration 5 não se aplica. A suíte anterior de migrations
    continuou passando em schemas isolados.

30. **Correções anteriores:** nenhum defeito de negócio anterior exigiu correção. O core financeiro
    anterior foi reutilizado sem alterar suas regras. Test doubles foram ampliados para Inbox;
    guards de transação e lifecycle receberam testes adicionais.

31. **Decisões/limitações:** visibility 90s > lote 10 × (processamento 5s + ACK 3s), sem extensão
    dinâmica; long polling 20s; concorrência 4. LocalStack é emulador com filas efêmeras neste Compose;
    não houve teste contra AWS hospedada. Crash foi simulado na fronteira commit/ACK e por falhas SQL,
    sem hard kill do processo. FIFO serializa entregas da mesma wallet, portanto as 50 tentativas
    concorrentes exercitam explicitamente a application/Inbox com uma entrega real; as 50 mensagens
    reais adicionais comprovam dedup fora do mecanismo temporário FIFO. DLQ pode abrir lacunas na
    sequência financeira de uma wallet; replay requer avaliação operacional. `.gitattributes` fixa
    LF nos arquivos executados dentro do Linux, inclusive após checkout no Windows.

32. **Escopo final:** não implementados publisher do Transactional Outbox, OIDC/Keycloak,
    autenticação/autorização, endpoints HTTP financeiros finais, reconciliation, tracing,
    métricas finais ou fases posteriores. O trabalho encerra na fase 6.

Arquitetura: [ARCHITECTURE.md](ARCHITECTURE.md). Execução e testes:
[PHASE6_OPERATIONS.md](PHASE6_OPERATIONS.md).
