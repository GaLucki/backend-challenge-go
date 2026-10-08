# Fase 10 — relatório de wallet reconciliation

Fase concluída em 2026-10-07. A rota antes 501 agora executa auditoria financeira
completa, internal-only, read-only. O estado não commitado da fase 9 encontrado no
início foi preservado. O inventário abaixo identifica somente o trabalho da fase
10, mesmo quando git status também mostra os arquivos anteriores da fase 9.

## Inventário e desenho — itens 1–12

1. **Arquivos criados (9):**
   - `internal/ports/reconciliation.go`
   - `internal/adapter/postgres/reconciliation.go`
   - `internal/application/financial/reconciliation.go`
   - `internal/application/financial/reconciliation_test.go`
   - `internal/adapter/http/reconciliation_test.go`
   - `internal/adapter/postgres/reconciliation_integration_test.go`
   - `internal/application/http_reconciliation_integration_test.go`
   - `docs/PHASE10_OPERATIONS.md`
   - `docs/PHASE10_REPORT.md`

2. **Arquivos alterados nesta fase (10):**
   - `internal/application/app.go`: providers Fx do reader e serviço.
   - `internal/adapter/http/financial.go`: dependência do caso de uso.
   - `internal/adapter/http/wallets.go`: endpoint real em lugar do placeholder.
   - `internal/adapter/http/server.go`: campos de auditoria no log existente.
   - `internal/adapter/http/financial_test.go`: fixture e contrato 200.
   - `internal/application/http_financial_integration_test.go`: antigo 501 agora
     exige 200 e CONSISTENT sobre o histórico financeiro real anterior.
   - `docs/openapi.yaml`: contrato real, exemplos e schemas.
   - `docs/ARCHITECTURE.md`: estratégia e garantias.
   - `docs/PHASE9_OPERATIONS.md`: instruções alinhadas ao endpoint atual.
   - `README.md`: status concluído até fase 10 e links.

3. **Arquitetura:** handler delega a `ReconciliationService`, que depende de
   `ports.ReconciliationReader` e Authorizer. PostgreSQL implementa snapshot
   consistente. Handler não possui SQL. Core de escritas, UoW, workers e publishers
   permanecem com as regras anteriores.

4. **Fontes comparadas:** wallet persistida, ledger completo da wallet e conjunto
   deduplicado de suas transações, transações ligadas pelo ledger e referências
   imediatas de REFUND/ROLLBACK. Sem tabela ou contabilidade paralela.

5. **Saldo:** começa em zero, CREDIT soma e DEBIT subtrai usando Money/int64 cents
   com overflow/underflow protegido. Resultado agregado comparado com wallet;
   saldo negativo é divergência. Nenhum float foi introduzido.

6. **Cadeia:** ordem wallet_version/id em collation C; primeiro before=0;
   before=after anterior; after=before +/- amount; amount positivo, direção válida,
   saldos não negativos; último after=wallet.balance. Soma e cadeia são verificadas
   separadamente.

7. **Ledger ↔ transaction:** existência, wallet, moeda, amount, estado e direção;
   exige um movimento por transação financeira PROCESSED, detecta duplicidade e
   movimentos indevidos. Currency do ledger é implícita na wallet. Reversões
   chamam o mesmo `validateReference` usado pelo processamento/pending worker.

8. **Versão:** expectedWalletVersion=1+movimentos PROCESSED válidos após criação,
   excluindo OPENING/LOSS. Ledger versions também são conferidas. Derivação usa
   transações, não contagem ingênua de ledger. OPENING positivo fica em versão 1.

9. **Snapshot/concorrência:** três queries dentro de REPEATABLE READ READ ONLY,
   sem FOR UPDATE/global lock/N+1. Snapshot estabelecido pela leitura de wallet,
   antes das demais. Commit/scan/rows/cancelamento falhos descartam tudo, com
   rollback em contexto independente e limitado. Não modifica o UoW de escritas.

10. **Authorization:** handler e application exigem identidade internal válida,
    antes de ler banco. Providers 403, sem token 401. OIDC/Keycloak e políticas
    anteriores não foram alterados.

11. **HTTP:** 200 CONSISTENT ou DIVERGENT; inexistente 404; ID inválido 400;
    falha técnica/cancelamento/prazo 503. Sem body ou Idempotency-Key obrigatório.
    DTO inclui saldos decimais, versões, contagens, divergences e checkedAt UTC.
    Divergência financeira detectada não é erro HTTP.

12. **Códigos estáveis:** BALANCE_MISMATCH, LEDGER_CHAIN_BROKEN,
    LEDGER_AMOUNT_MISMATCH, LEDGER_DIRECTION_MISMATCH, LEDGER_CURRENCY_MISMATCH,
    LEDGER_TRANSACTION_NOT_FOUND, TRANSACTION_LEDGER_MISSING,
    UNEXPECTED_LEDGER_ENTRY, NEGATIVE_RECONSTRUCTED_BALANCE,
    WALLET_VERSION_MISMATCH, TRANSACTION_WALLET_MISMATCH, DUPLICATE_LEDGER_ENTRY,
    LEDGER_ARITHMETIC_OVERFLOW, TRANSACTION_INVALID. Detalhes têm code,
    transactionId/ledgerEntryId quando aplicável, expected, actual e mensagem fixa
    sanitizada. Amount/chain usam centavos e soma usa decimais; sem SQL/stack/secrets.

## Evidência financeira — itens 13–30

13. **Wallet zero:** PostgreSQL real, saldo 0.00, versão 1, ledger vazio,
    CONSISTENT. Primeiro WIN posterior: 1.00, versão 2, uma entrada, CONSISTENT.

14. **OPENING:** wallet 100.00, um CREDIT/opening, before=0, versão 1,
    CONSISTENT. HTTP real também confirma saldo e contagens; nenhuma presunção de
    OPENING para wallets inicialmente zero.

15. **BET/WIN/LOSS:** 100.00 → BET20=80.00/version2 → WIN50=130.00/version3;
    LOSS0 mantém130.00/version3, sem entrada. Auditoria após cada operação.

16. **REFUND/ROLLBACK:** refund BET20 →150.00/version4; rollback REFUND20
    →130.00/version5; rollback WIN50→80.00/version6; novo BET10→70.00/version7;
    rollback BET10→80.00/version8. Todos CONSISTENT com direções corretas.

17. **Rejected:** BET1000 sobre80.00 é REJECTED sem ledger e mantém a auditoria
    CONSISTENT. Estados FAILED/PENDING sem ledger são aceitos nos testes unitários;
    FAILED é terminal sem efeito financeiro na semântica atual.

18. **Pending reference:** REFUND5 antes de BET5 futuro não modifica saldo/versão
    e reconcilia CONSISTENT. Depois BET5 e resolução pelo worker, saldo80.00,
    versão10, ledger10, CONSISTENT. Não exige ledger enquanto pending.

19. **Balance mismatch:** corrupção direta de wallet balance para79.00, enquanto
    ledger reconstrói80.00: DIVERGENT/BALANCE_MISMATCH, sem correção. Verificado em
    adapter PostgreSQL e HTTP real, com chamadas repetidas e 200.

20. **Ledger chain:** before e after alterados preservando a aritmética local,
    mas rompendo o encadeamento: LEDGER_CHAIN_BROKEN. Unitários também verificam
    primeiro before e after aritmeticamente incorreto.

21. **Amount:** ledger BET19 em lugar de20, after ajustado para manter CHECK:
    LEDGER_AMOUNT_MISMATCH, além de outras divergências pertinentes.

22. **Direction:** BET associado a CREDIT com before/after coerentes localmente:
    LEDGER_DIRECTION_MISMATCH. Audit não depende somente do CHECK aritmético.

23. **Missing ledger/transaction:** remover movimento de BET PROCESSED gera
    TRANSACTION_LEDGER_MISSING. Link para transaction inexistente com bypass de
    FK somente no schema descartável gera LEDGER_TRANSACTION_NOT_FOUND.

24. **Unexpected ledger:** LOSS, REJECTED, PENDING_REFERENCE e FAILED associados
    a um movimento real geram UNEXPECTED_LEDGER_ENTRY. Duplicidade histórica
    gera DUPLICATE_LEDGER_ENTRY; vínculo com wallet diferente é detectado. Moeda
    USD em transaction de wallet BRL gera LEDGER_CURRENCY_MISMATCH. Também há
    fixtures reais para saldo negativo e overflow. São 17 subcasos PostgreSQL.

25. **Version mismatch:** corromper wallet.version ou a versão de uma entrada
    gera WALLET_VERSION_MISMATCH. Happy path demonstra OPENING não incrementando,
    LOSS/rejeição sem incremento e todas as reversões incrementando.

26. **Histórico >100:** 120 WIN de0.01 após os fluxos anteriores; auditoria verifica
    130 entradas e132 transações, saldo81.20, wallet/expectedVersion130,
    CONSISTENT. Sem truncamento pelo default50/max100 do GET ledger.

27. **Concorrência mesma wallet:** teste pausa a leitura real após estabelecer o
    snapshot de wallet A100.00/version1. BET20 de A confirma antes de liberar a
    auditoria. Relatório permanece100.00/version1/ledger1/transaction1; nova
    auditoria vê80.00/version2/ledger2. Nenhum estado misturado ou bloqueio de linha.

28. **Concorrência wallets diferentes:** mesma pausa em A; BET20 de B confirma
    enquanto A é auditada, dentro de prazo3s. A continua CONSISTENT100.00/version1.
    Os gates são wrappers pgx apenas em testes, sem hooks/sleeps em produção.

29. **Read-only:** comparação de JSON exato de todas as linhas antes/depois,
    em wallets, wager_transactions, wallet_ledger_entries, outbox_events,
    inbox_messages, wager_idempotency_records, wager_reversals e
    pending_wager_references. Validado em histórico normal/repetido e cada
    corrupção; HTTP confirma seus seis conjuntos principais sem mutação.

30. **Sem Outbox:** JSON completo de Outbox fica idêntico, não só sua contagem.
    Reader usa modo READ ONLY e o caso de uso não depende de repositories de
    escritas, Inbox, Outbox ou Idempotency. Não emite WalletBalanceChanged.

## Contratos, qualidade e ambiente — itens 31–44

31. **Keycloak real:** client_credentials dos três clients; sem token401,
    provider-a403, provider-b403, internal-service200; wallet inexistente404,
    divergência200 e tabela temporariamente indisponível503. Regra de isolamento
    anterior preservada. Unitários comprovam recusa antes de acessar persistence.

32. **OpenAPI atualizado:** versão0.10.0; endpoint real, Bearer internal, 200 com
    exemplos CONSISTENT/DIVERGENT, erros401/403/404/503, schemas de resultado,
    divergência e saldo de auditoria, nullable explícito. Componente501 removido.
    README, architecture, operações da fase9 e nova operaçãofase10 alinhados.

33. **`go test ./... -count=1`: PASS.** Execução com TEST_DATABASE_URL,
    TEST_SQS_ENDPOINT, TEST_OIDC_ISSUER_URL e TEST_KEYCLOAK_HEALTH_URL definidos.
    PostgreSQL, LocalStack e Keycloak reais; não foi execução com integrações
    omitidas. Package PostgreSQL68.981s, application8.960s, HTTP3.853s. Regressões
    duas BET80 em100, cinquenta tentativas iguais, múltiplas instâncias, duplicates
    SQS, crash/recovery, pending, multi-publisher, HTTP↔SQS e restart incluídas.

34. **`go vet ./...`: PASS**, sem diagnóstico. Arquivos Go formatados por gofmt.

35. **`go test -race ./...`: não executável neste ambiente.** Tentado com
    CGO_ENABLED=1; compilação falha por `C compiler "gcc" not found`. Detector não
    chegou a rodar; não reportado como PASS. Concorrência funcional foi verificada
    com banco real, mas isso não substitui o detector.

36. **OpenAPI:** `go run github.com/getkin/kin-openapi/cmd/validate@v0.149.0
    docs/openapi.yaml`: PASS, exit0. Nenhuma alteração em go.mod/go.sum.

37. **Compose:** `docker compose --env-file .env.example config --quiet`: PASS.
    `up -d --build --wait --wait-timeout 240`: PASS. Build LocalStack concluído;
    PostgreSQL, LocalStack e Keycloak healthy. Arquivos Compose/infra intactos.

38. **PostgreSQL:** postgres16-alpine healthy, porta5432, banco wagering acessível.
    Dados do operador preservados; integração financeira isolada em schemas
    descartáveis.

39. **LocalStack:** healthy, imagem baseada em4.6.0, porta4566. Listagem confirma
    somente as três filas anteriores: wager-transactions.fifo,
    wager-transactions-dlq.fifo e wager-events.fifo. Sem evento de reconciliation.

40. **Keycloak:**26.7.5 healthy; realm jungle-gaming/clients existentes,
    localhost8081 e management9090 em loopback. Grants reais usados nos testes.

41. **Migrations:** versão5, dirty=false; nenhuma migration000006 necessária.
    Migrations1–5 não alteradas. Fixtures de corrupção checam current_schema com
    prefixo de schema de integração, desabilitam/reabilitam trigger em transação e
    verificam append-only após a auditoria. Constraints removidas pertencem só a
    schemas descartáveis destruídos no cleanup; produção não foi enfraquecida.

42. **Correções anteriores:** nenhum defeito anterior de produção identificado.
    Somente duas expectativas501 atualizadas para o contrato200 solicitado, com
    validação de CONSISTENT adicionada no teste real da fase9. Nenhuma garantia ou
    teste de regressão anterior removido/enfraquecido.

43. **Decisões/limitações:** memory O(histórico), três queries sem N+1 e ordenação
    determinística; contexto HTTP10s. Snapshot MVCC não bloqueia DML, mas locks
    normais de tabela podem conflitar com DDL e histórico longo retém versões para
    vacuum. checkedAt é conclusão, não snapshot token. Resultado não é persistido.
    ledgerBalance é null quando amount/direction inválidos ou overflow impedem
    reconstrução segura, e pode ser negativo em corrupção; versão esperada é null
    se semântica histórica inválida impede derivação. Wallet impossível de
    rehidratar resulta em erro técnico sanitizado, nunca CONSISTENT fabricado.
    Auditoria compara wallet/ledger/transações; não é uma auditoria completa dos
    eventos de Outbox, claims de reversão ou do desafio. Logs de conclusão incluem
    correlationId, walletId, reconciliationStatus, contagens, divergenceCount e
    durationMs; falhas técnicas usam ERROR sem detalhes SQL ou histórico completo.

44. **Encerramento:** auto-repair, tracing distribuído, métricas finais e auditoria
    final não foram implementados. Trabalho encerrado ao final da fase10.
