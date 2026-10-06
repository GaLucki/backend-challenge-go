# Relatório da fase 7

Fase 7 concluída em 06/10/2026. A base das fases 0–6 foi testada antes das alterações e a suíte
completa passou depois da implementação, com PostgreSQL e SQS LocalStack reais. Os arquivos de
fases anteriores ainda não commitados foram preservados.

1. **Arquivos criados:** migrations `000005_outbox_publication_leases.{up,down}.sql`;
   `internal/ports/publication.go`; `internal/adapter/postgres/publication.go`;
   `internal/adapter/postgres/outbox_integration_test.go`;
   `internal/adapter/sqs/{event_sender,event_sender_test}.go`;
   `internal/application/outbox/{publisher,publisher_test}.go`;
   `internal/application/outbox_publisher.go`; `internal/application/outbox_lifecycle_test.go`;
   `internal/config/{outbox,outbox_test}.go`;
   `docs/PHASE7_OPERATIONS.md`; `docs/PHASE7_REPORT.md`.

2. **Arquivos alterados nesta fase:** `.env.example`, `docker-compose.yml`,
   `infra/localstack/10-queues.py`, `go.mod`, `internal/application/app.go`,
   `internal/application/app_test.go`, `internal/config/config.go`,
   `docs/ARCHITECTURE.md`, `docs/PHASE6_OPERATIONS.md`. Alterações anteriores em `go.sum`,
   domínio, repositórios financeiros e migrations 1–4 foram preservadas.

3. **Dependências:** nenhum módulo ou versão nova. O AWS SDK Go v2 existente é reutilizado;
   `github.com/aws/smithy-go v1.28.1`, antes transitivo, aparece como dependência direta por causa
   da instrumentação de requests reais nos testes. Não houve novo serviço downstream.

4. **Migration:** 000005 adiciona sequência persistente de publicação, claimed_at, claim_until,
   claimed_by, claim_token, constraints e índice de heads por aggregate. É necessária para
   terminar a transação SQL antes do I/O SQS e recuperar publishers mortos. Payloads/IDs existentes
   permanecem intactos; migrations 1–4 não foram editadas.

5. **Fila de eventos:** `wager-events.fifo`, provisionada automaticamente, FIFO com dedup explícita,
   polling 20s e retenção 14 dias. É distinta de comandos e da DLQ de comandos. Config valida nomes/
   URLs, e Fx compara ARNs reais para impedir publicação na entrada mesmo via alias de URL.

6. **MessageGroupId:** `aggregate:` + SHA-256 hexadecimal de aggregateId. Eventos de wallet mantêm
   walletId; eventos de estado mantêm transactionId, conforme o envelope financeiro anterior.
   Não há grupo global nem ordenação entre aggregates diferentes.

7. **MessageDeduplicationId:** `event:` + SHA-256 hexadecimal de eventId, idêntico em todas as
   tentativas do evento. A dedup limitada do FIFO não substitui dedup persistente downstream.

8. **Claim/lease:** CTE autocommit com FOR UPDATE SKIP LOCKED seleciona somente heads unpublished,
   due e sem lease vivo. O lease é persistido antes de retornar. Cada batch recebe token novo;
   Renew/Mark/Retry exigem token atual não expirado. Nenhuma transação/row lock atravessa SendMessage.
   Teste real NOWAIT confirmou que a linha permanece disponível enquanto o request SQS está pausado.

9. **Multi-publisher:** identidades, pools e clients independentes; até um evento por aggregate por
   batch, com envio paralelo de aggregates. Ownership/recovery dependem exclusivamente do PostgreSQL,
   sem mutex global ou estado indispensável em memória.

10. **Ordering:** somente o menor publication_sequence ainda não publicado de um aggregate pode
    ser claimado. Retry, lease vivo, data futura e row lock do head bloqueiam seu tail. Eventos
    financeiros novos seguem sequência de inserção protegida pelos locks financeiros existentes.
    Backfill legado usa occurred_at/eventId. A garantia cobre progressão dos eventIds distintos;
    uma duplicata tardia de um request antigo pode aparecer depois de eventos posteriores. Não é
    ordem global, nem fencing do broker para requests já em trânsito.

11. **Retry/backoff:** retry indefinido, base 1s e cap 1m, doubling saturado sem overflow. Next attempt
    é persistido no relógio PostgreSQL. Retry_count conta falhas de Send duravelmente registradas;
    não é zerado no sucesso e satura no limite do INTEGER existente. Não há descarte automático.

12. **Falha de publicação:** fila inexistente gerou erro SQS real; published_at continuou NULL,
    retry_count passou a 1 e depois a 2, backoff persistido passou de 5s a 10s no fixture, claim foi
    liberado e um publisher novo respeitou o next_attempt_at. Retry posterior publicou o mesmo
    evento sem criar outro registro e preservou retry_count=2.

13. **Crash antes do publish:** claim durável sem Send, expiração da lease e publisher novo com
    pool independente resultaram em entrega e published_at preenchido. Nenhum evento ficou perdido.

14. **Crash depois do publish antes do mark:** dois Send reais foram observados, com payload,
    eventId e dedup ID iguais. O primeiro deixou published_at NULL; recovery expirou a lease,
    reenviou e marcou. Falha real via trigger no mark também foi recuperada. Dentro da janela FIFO
    o broker entregou uma cópia; o teste não alega exactly-once publication/delivery.

15. **Restart:** a instância nova usa apenas Outbox/lease/retry persistidos para localizar e publicar
    o backlog, incluindo após claim abandonado e falha de marcação. Tokens antigos não conseguiram
    renovar, marcar, incrementar retry ou liberar o claim recuperado de outra instância.

16. **EventId/envelope estáveis:** enviado exatamente o JSONB persistido, sem reconstrução dos
    eventos. IDs, version, Money string, UTC/RFC3339, causation e correlation foram preservados.
    Todos os quatro tipos financeiros existentes chegaram à FIFO de eventos.

17. **Múltiplos publishers:** teste com três instâncias passou e confirmou participação de todas.
    Outro teste pausou um request real de um aggregate e permitiu que um publisher independente
    publicasse outro aggregate, sem esperar pelo primeiro e sem SQL lock mantido durante I/O.

18. **100 eventos:** 25 wallets com OPENING e 25 BETs produziram 100 envelopes financeiros reais.
    Três publishers processaram o backlog; todos os 100 eventIds foram recebidos, sem perdas,
    todos ficaram published, leases limpas, payloads preservados e ordem crescente por aggregate.

19. **Graceful shutdown:** polling cancelado não iniciou novo tail; Send ativo terminou com ACK
    broker e mark antes de sair. Claims ainda não enviados são liberados em paralelo com deadline
    de SQL, evitando batchSize × timeout. Cancelamento forçado mantém envios ambíguos recuperáveis
    por lease. Fx inicia/para publisher, consumer e pending worker antes do fechamento do pool.
    Os testes Fx novos usam schemas isolados para não claimar backlog preexistente do operador.

20. **go test:** `go test ./... -count=1` passou com TEST_DATABASE_URL e TEST_SQS_ENDPOINT definidos,
    incluindo todos os testes anteriores e os novos de publicação. Após isolar os schemas dos
    testes Fx, `go test ./internal/application -run IntegrationFxOutbox -count=1 -v` também passou.
    Testes de invisibilidade uncommitted/rollback, seleção/lease/fencing e migrations usam PostgreSQL
    real; testes de broker usam SDK + LocalStack, sem substituir esses componentes por mocks.

21. **go vet:** `go vet ./...` passou, exit code 0, inclusive após o isolamento dos testes Fx.

22. **go test -race:** tentativa com CGO_ENABLED=1 falhou na compilação: `gcc` ausente do PATH.
    O race detector não executou neste Windows; isso não é evidência de ausência de data races.

23. **Compose config/up:** `docker compose --env-file .env.example config --quiet` passou;
    `docker compose --env-file .env.example up --build -d --wait` passou. LocalStack foi atualizado
    com script de provisionamento e healthcheck para três filas; PostgreSQL/volume preservados.

24. **PostgreSQL:** container wagering-postgres healthy; schema_migrations version 5, dirty false.
    Dados financeiros anteriores foram preservados. Schemas temporários foram removidos.

25. **LocalStack:** container wagering-localstack healthy, imagem local phase7 baseada no pin
    4.6.0. Atributos da nova fila foram consultados e o comando operacional de Receive executado.
    Estado de filas do emulador continua efêmero; não é uma validação em AWS hospedada.

26. **Filas:** wager-transactions.fifo e wager-transactions-dlq.fifo continuam provisionadas com
    FIFO/redrive da fase 6; wager-events.fifo também existe e é FIFO. Sem consumer de negócio da
    saída, sem envio de evento para a entrada e sem loop de geração de novos wagers.

27. **UP/DOWN/UP:** validado em schema isolado com eventos financeiros anteriores e também pelo
    CLI migrate no PostgreSQL local: 5/u → 5/d → 5/u, versão final 5. IDs/payloads e ledger/wallet
    permaneceram iguais; não sobrou dirty state.

28. **Correções anteriores:** nenhum defeito financeiro de fases anteriores exigiu correção.
    O core, consumer, Inbox, idempotência e pending worker mantiveram as regras existentes.
    Ajustes desta fase ficaram restritos à publicação, composição, configuração, testes e docs.

29. **Limitações/decisões:** sem transação distribuída SQL/SQS; publicação at-least-once exige
    dedup downstream por eventId. Leases/tokens fazem fencing de estado SQL, não de pedidos SQS
    já em trânsito. Payload inválido ou broker indisponível pode bloquear o próprio aggregate;
    outros continuam. Crash usa fronteiras claim/send/mark e triggers, não hard kill do processo.
    Expiração de timestamps SQL acelera testes, sem sleeps longos. Middleware SDK observa/pausa
    chamadas reais e não devolve respostas falsas. Retenção broker é finita, e eventual publicação
    depende da recuperação da infraestrutura e de publishers ativos.

30. **Escopo:** não implementados OIDC/Keycloak, authorization de providers, endpoints financeiros
    HTTP finais, reconciliation, tracing, métricas finais completas, consumer de negócio dos
    eventos ou funcionalidades de fases posteriores. O trabalho encerra na fase 7.

Detalhes de desenho: [ARCHITECTURE.md](ARCHITECTURE.md).
Comandos reproduzíveis: [PHASE7_OPERATIONS.md](PHASE7_OPERATIONS.md).
