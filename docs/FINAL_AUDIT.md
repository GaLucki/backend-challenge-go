# Auditoria final — Fase 12

Data: 2026-10-07. Fonte de verdade: [especificação original](CHALLENGE_SPEC.md),
preservada do commit `bdd9e70`, blob `b19a1a5cdc00fa22e13fae9582777ecb7b6ad248`.
Recomendação: **NOT_READY** para fechar todos os critérios obrigatórios da entrega.
A reprodução local e os testes passaram. Porém, autorização HTTP comprovada não
comprova controle de acesso ao broker: R09 permanece PARTIAL e essa parte do critério
eliminatório de acesso não autorizado a operações não foi verificada adequadamente.
As demais ressalvas também estão explicitadas; não se transforma ausência de prova em PASS.

## Método e evidências

Revisão do contrato original, arquitetura, OpenAPI, migrations, Compose, configuração,
domínio, UoW/repositórios, autorização, handlers/workers e testes. Relatórios disponíveis
das fases 6–11 foram confrontados com código e novas execuções; não foram usados como
prova de PASS. Não existem relatórios separados 0–5 neste checkout; o histórico está
na arquitetura/código/migrations/testes. Alterações já presentes no workspace foram
preservadas. Não houve commit, push, rollback do banco principal, frontend ou auto-repair.

| Evidência | Execução real e resultado |
| --- | --- |
| E1 | Windows: Go 1.27.0, windows/amd64, CGO=0; gcc ausente. `go test ./... -count=1 -json` com quatro TEST_*: exit 0, 13 pacotes testados; PG 82.833s, application 19.462s. Esse run precedeu a adição do teste Kill real; ele passou depois em execução focada. |
| E2 | Runner Docker final: Go 1.27.0 linux/amd64, CGO=1, GCC Debian 12.2.0. `go test ./... -count=1 -json`: exit 0; PG 43.322s, application 6.287s; inclui teste Kill real e todas as regressões. |
| E3 | Mesmo runner/fonte final: `go test -race ./... -count=1 -json`: exit 0, nenhuma race; PG 65.088s, application 16.875s. **222 testes principais PASS, incluindo 70 integrações reais**. Nenhuma integração omitida por falta de ambiente. |
| E4 | `go vet ./...`: exit 0 em Windows e runner Linux. `gofmt -l cmd internal`: saída vazia. |
| E5 | `docker compose --env-file .env.example config --quiet` e `up -d --build --wait --wait-timeout 240`: exit 0. PG/LocalStack/Keycloak healthy; filas reais provisionadas. Overlay audit também validado/construído. |
| E6 | `TestIntegrationFinalMigrationsEntireChainUpDownUp`: PASS; cadeia 1–6 DOWN/UP em schema descartável, sem relações/funções remanescentes e proteções verificadas após UP. Primary: migration incremental 6 aplicada; `schema_migrations=6,false`. Nenhum DOWN no primary. |
| E7 | `go run github.com/getkin/kin-openapi/cmd/validate@v0.149.0 docs/openapi.yaml`: exit 0, OpenAPI 3.0.3, contrato v0.12.0 e exemplos válidos. |
| E8 | `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/final-smoke.ps1`: exit 0. API Go host: três tokens reais, health 200, anonymous 401, provider estrangeiro 403, metrics interno 200, saldo 75.00/versão 2/duas entradas, Inbox 1, quatro eventos publicados e HTTP/SQS replay=true. |
| E9 | Dockerfile runtime construído via perfil app, inicia sem root e responde `{"status":"ok"}` por curl dentro da rede Linux. Porta não acessível do Windows via host networking; smoke E8 verifica a API no host. |
| E10 | E3: três processos financeiros PIDs 917/926/934; duplicidade 942/950/958; reversões 968/976/984; consumers 992/1000/1006; publishers 1033/1041/1049. Cada filho tem heap/pool/SDK próprios. Kill real: PIDs 882 antes do commit e 900 após commit antes do ACK; novos processos recuperam a entrega. |

Os JSONL completos ficam localmente em `artifacts/phase12-test-windows.jsonl`,
`phase12-test-linux.jsonl`, `phase12-race-linux.jsonl`; smoke em
`artifacts/phase12-smoke-result.json`. São artefatos de execução ignorados pelo Git/build,
não necessários para reproduzir. Os comandos estão no README. O único skip de teste
em E2/E3 é `TestFinalAuditProcessHelper`, executado pelos testes pais em processos filhos;
`cmd/api` e `ports` não têm arquivos de teste. Não confundir esse helper com integração omitida.

## Catálogo de testes

Os identificadores abaixo referenciam arquivos reais; a suíte completa executou todos.

| Grupo | Arquivo e exemplos de testes |
| --- | --- |
| M | [money](../internal/domain/money/): parse_test/money_test/final_audit_test; limites, moeda, entradas inválidas, Sub MinInt64, zero inválido. |
| W | [wallet](../internal/domain/wallet/): wallet_test/final_timestamps_test; invariantes, versão, insuficiência e metadados. |
| T | [wager](../internal/domain/wager/): transaction_test/rehydrate_test/final_metadata_test; criação/reidratação, OPENING, FSM e jogo. |
| F | [financial](../internal/application/financial/): service_test, reversals_test, pending_test, delivery_test, hash_test, authorized_test, final_contract_test e final_reconciliation_test. |
| P | [PG financeiro](../internal/adapter/postgres/use_cases_integration_test.go): TwoBetsAgainst100, FiftyConcurrentReplays, ConcurrentExternalDuplicateDifferentKeys, IndependentWalletsProgress, WagerAtomicFailure. |
| R | [PG reversões](../internal/adapter/postgres/reversals_integration_test.go): ConcurrentReversals, PendingOutOfOrderRecoveryAndIdempotency, PendingExpirationAndInvalidReference, PendingWorkersAndSkipLocked, ReversalAndPendingAtomicFailure. |
| S | [SQS real](../internal/adapter/postgres/sqs_integration_test.go): ProvisionedQueues, AllFinancialPaths, CrashAndDeleteRecovery, IncompleteInboxRecovery, PermanentFailuresReachRealDLQ, CrossTransport, 50DuplicatesThreeConsumers, FIFOOrdering, ForcedShutdownRollsBackAndRetainsDelivery. |
| O | [Outbox real](../internal/adapter/postgres/outbox_integration_test.go): EligibilityLeaseRecoveryAndFencing, UncommittedAndRolledBackEventsNeverPublished, RealSendFailureAndPersistentRetry, CrashBoundariesAndMarkFailure, 100EventsThreePublishersAndOrdering, NetworkDoesNotHoldSQLLocksAndShutdownDrains. |
| A | [OIDC real](../internal/adapter/oidc/keycloak_integration_test.go), [verifier](../internal/adapter/oidc/verifier_test.go), [isolamento](../internal/application/auth_integration_test.go): KeycloakTokensAndValidation, OIDCProviderIsolation, FxOIDCDiscoveryFailurePreventsHTTPStartup, JWKSCacheRotationAndOutage. |
| H | [HTTP integrado](../internal/application/http_financial_integration_test.go), [contrato original](../internal/application/final_contract_integration_test.go), [boundary](../internal/adapter/http/financial_test.go): FinalFinancialHTTP, HTTPConcurrentThreeInstances, FinalHTTPCrossTransport, OriginalHTTPAndSQSContractReplay. |
| C | [reconciliation PG](../internal/adapter/postgres/reconciliation_integration_test.go), [HTTP](../internal/application/http_reconciliation_integration_test.go): AllOperationsReadOnlyAndLargeHistory, ControlledCorruption, ConcurrentSnapshots, CanceledReadHasNoPartialResult, ReconciliationHTTPRealKeycloak. |
| V | [observabilidade](../internal/application/observability_integration_test.go), [métricas](../internal/observability/metrics_test.go), [logger](../internal/observability/logger.go), [lifecycle](../internal/application/app_test.go) e [shutdown](../internal/application/outbox_lifecycle_test.go). |
| X | [três processos](../internal/adapter/postgres/final_process_integration_test.go), [Kill real](../internal/adapter/postgres/final_crash_integration_test.go), [migrations](../internal/adapter/postgres/final_migrations_integration_test.go), [broker health](../internal/adapter/postgres/final_broker_health_integration_test.go), [config segura](../internal/config/final_security_test.go). |

## Matriz completa

Referências § indicam seções da especificação original. Risco `G` significa apenas
as limitações globais explicitadas na seção seguinte; não há falha observada no cenário testado.
PASS prova o escopo descrito, sem afirmar correção para toda combinação possível de falhas.

| ID / § | Requisito | Status | Arquivos responsáveis | Testes | Evidência | Observações | Riscos |
| --- | --- | --- | --- | --- | --- | --- | --- |
| R01 / 1,4 | Go/version/Modules, stack real | PASS | go.mod/go.sum, Dockerfile, Compose | E2/E4 build | E1–E5,E9 | Go 1.27.0 explícito; três dependências reais | G |
| R02 / 4 | Composição Fx por construtores/Modules/Provide/Invoke | PASS | application/app.go | V: FxAppConstructs/FxLifecycle | E2,E3 | Domínio independente de adapters/Fx | G |
| R03 / 4 | Startup valida config/dependências e falha fechado | PASS | config/, app.go, postgres/pool.go, oidc/verifier.go | A,V; PoolFailedStartClosesPoolAndSanitizesError | E2,E3 | Discovery/pool/filas antes de ready | G |
| R04 / 4,12 | Cancelamento/timeouts/drenagem/fechamento por lifecycle | PASS | http/server.go, application/*worker*, *publisher*, sqs_consumer.go | V,O,S | E2,E3 | Workers/handlers terminam antes do pool | G |
| R05 / 2 | OAuth2/OIDC externo/client_credentials/Keycloak automático | PASS | oidc/, infra/keycloak/*.json, Compose | A | E2,E3,E5,E8 | Três identidades reais, sem emissão própria | Local fixtures |
| R06 / 2 | JWT assinatura/issuer/audience/expiry/JWKS | PASS | oidc/verifier.go, config/oidc.go | A: tokens reais e claims/chaves inválidas | E2,E3 | Expiry real com relógio controlado; cache/rotação simulada em unidade | G |
| R07 / 2 | Provider do token, anti-spoofing, replay/consulta isolados | PASS | identity/, financial/authorized.go, http/auth.go | A,H,F | E2,E3,E8 | A/B distintos; foreign 403, sem efeitos | G |
| R08 / 2 | Operações de carteira/internal-only e auth efetiva | PASS | http/server.go, financial/authorized.go, identity/ | A,H,C,V | E2,E3,E8 | Wallet/ledger/reconciliation/metrics protegidos; disabled fail-closed | G |
| R09 / 2,10 | Credenciais/políticas do broker | PARTIAL | sqs/client.go, config/config.go, ARCHITECTURE fase 12 | X: BrokerLocalCredentialsCannotEscapeDevelopment | E3,E8 | SDK usa credenciais; dummy fora de dev rejeitado; grants documentados | LocalStack não comprova IAM/queue policy |
| R10 / 4,5 | PostgreSQL real e transações/locks SQL explícitos | PASS | postgres/unit_of_work.go, repositories.go | P, financial_integration_test | E2,E3 | pgx, sem mutex financeiro global | G |
| R11 / 4,15 | Migrations versionadas/aplicação/reversão | PASS | migrations/000001–000006, README | X e migrations históricas | E2,E3,E6 | Full DOWN/UP apenas em schema descartável; primary forward-only | DOWN remove dados se mal utilizado |
| R12 / 5 | Invariantes no banco inclusive fora do UoW | PARTIAL | migrations/000002,000004,000006; UoW | RepositoriesAndConstraints, AtomicRollback, C | E2,E3,E6 | UNIQUE/FK/CHECK/append-only existem; atomicidade da aplicação comprovada | UPDATE SQL arbitrário de wallet não obriga ledger/Outbox por trigger diferida |
| R13 / 5 | Ledger append-only UPDATE/DELETE/TRUNCATE proibidos | PASS | migration 2, postgres/repositories.go | financial_integration_test: RepositoriesAndConstraints | E2,E3 | Statement trigger protege até comandos sem linhas | Admin pode desabilitar trigger |
| R14 / 5,8 | Sem lost update/lock por wallet/wallets paralelas | PASS | Wallets.GetForUpdate/Update, UoW | P: IndependentWalletsProgress; X | E2,E3,E10 | B confirma enquanto A está bloqueada; versão condicionada | G |
| R15 / 6 | Encapsulamento/criação/reidratação/erros classificáveis/context | PASS | domain/, ports/, application/financial/ | M,W,T,F,P | E2,E3 | Sem panic para rejeição; rollback/timeout verificáveis | DTOs de persistência não são entidades |
| R16 / 5,6.1 | Sem float em parsing/cálculo/serialização/persistência de dinheiro | PASS | domain/money/, financial/, BIGINT migrations | M,F,P,H | E2,E3 + inspeção | Float somente em métricas/tempo, nunca Money | G |
| R17 / 6.1 | Decimal string/escala/NaN/Infinity/expoente/negativo/empty | PASS | money/parse.go, http/financial.go, sqs/contract.go | M,H,S e sqs/final_contract_test | E2,E3 | Contrato original duas casas; legado normaliza formas equivalentes | Compatibilidade documentada |
| R18 / 6.1 | ISO 4217/moedas incompatíveis | PASS | money/money.go | M,W,F | E2,E3 | Subconjunto explícito BRL/USD/EUR, todas de duas casas; XYZ rejeitado | Não é catálogo completo ISO |
| R19 / 6.1 | Soma/subtração/negação/comparação/zero/overflow exatos | PASS | money/money.go,parse.go | M: final_audit_test e limites existentes | E2,E3 | MinInt64−MinInt64=0; zero inválido não compara/nega com sucesso | G |
| R20 / 6.1 | Persistência valor/moeda e diferenças internas negativas | PASS | postgres/repositories.go, reconciliation.go | M,P,C | E2,E3 | BIGINT cents; NUMERIC só no CHECK exato; difference pode ser negativa | Null em reconstrução fora do int64 |
| R21 / 6.2 | Wallet identidade/jogador/moeda/saldo/versão/timestamps | PASS | wallet/wallet.go, postgres/repositories.go, http/wallets.go | W; PG roundtrip; H | E2,E3 | Metadata preservada/restaurada; rejeição não muda timestamps | Rehydrate legado sem metadata guarda unknown |
| R22 / 6.2 | Única wallet(player,currency), saldo>=0, currency compatível | PASS | migration 2, wallet/, financial/service.go | W,F,P,H | E2,E3 | Duplicate conflict; moeda/saldo por domínio + FK/CHECK | G |
| R23 / 6.2 | Versão inicial 1, incrementa somente mudança financeira | PASS | wallet/, financial/service.go,reversals.go | W,F,P,R,X | E2,E3,E10 | OPENING versão 1; LOSS/rejeição/replay não incrementam | G |
| R24 / 6.2,6.4 | Cada mudança confirmada com ledger, sem efeitos parciais | PASS | financial/service.go, UoW, repos | P: OpeningAtomicFailure/WagerAtomicFailure; R,S | E2,E3 | Inclui falha em ledger/Outbox/Inbox/result/pending | Escritas fora do UoW: R12 |
| R25 / 6.3 | Transação externa: IDs, provider, key/hash, rodada/jogo, Money, resultado | PARTIAL | wager/, financial/hash.go, idempotency.go, migration 6 | T,F,H: OriginalHTTPAndSQSContractReplay | E2,E3,E8 | Novo contrato preserva game; key/hash/resultado em registro associado | Legado omite jogo/key SQS; não se inventa metadata histórica |
| R26 / 6.3 | FSM PENDING/PENDING_REFERENCE/terminais e replay | PASS | wager/state.go,transaction.go; service/pending | T,F,R | E2,E3 | Criação e reidratação não reaplicam dinheiro | G |
| R27 / 6.3 | PROCESSED/REJECTED/FAILED imutáveis | PASS | wager/transaction.go, migration 6 | T,X: FinalMigrationsEntireChainUpDownUp | E2,E3,E6 | Domain transitions + SQL UPDATE/DELETE bloqueados | Admin SQL privilegiado excluído |
| R28 / 6.3 | PENDING confirmado com retomada durável | PASS | service.go, delivery.go, pending.go, inbox.go | F,R,S,X Kill | E2,E3,E10 | Síncrono não confirma aceite PENDING intermediário; espera é durável | G |
| R29 / 6.3 | OPENING internal-only, metadata própria, crédito inicial único | PASS | financial/service.go, wager/, migrations 2,6 | F,P,H,S,X | E2,E3,E6 | HTTP/SQS OPENING rejeitados; unique positivo | Zero OPENING legado permitido no nível de persistência |
| R30 / 6.3 | Falha permanente de infraestrutura registrada FAILED | PARTIAL | wager/state.go,transaction.go, schema; SQS classify/redrive | T,S | E2,E3 | Estado/transição existem; pipeline usa rollback/retry/DLQ para falhas técnicas | Não há caminho automático que persista FAILED para infraestrutura esgotada |
| R31 / 6.4 | Ledger IDs/direção/valor/before/after/tempo/arithmetic/UNIQUE | PASS | ports/persistence.go, migration 2, repositories.go | PG constraints, F,C | E2,E3 | SQL valida construção persistida, FK escopo e aritmética exata | Record DTO mutável em memória; banco append-only |
| R32 / 6.5 | Inbox consumidor/message/hash/received/completed/UNIQUE | PASS | postgres/inbox.go, financial/delivery.go, migration 2 | F,S | E2,E3,E8 | Persistente; hash conflict não reaplica efeito | G |
| R33 / 6.5 | Inbox e financeiro/ledger/Outbox no mesmo commit | PASS | delivery.go, UoW | S: CrashAndDeleteRecovery; X Kill | E2,E3,E10 | Pending durable permite ACK sem resolver referência agora | G |
| R34 / 6.5 | Outbox eventId/aggregate/type/payload/retries/next/published | PASS | financial/events.go, ports/publication.go, migrations 2,5 | F,O | E2,E3 | Registros confirmados atomicamente | G |
| R35 / 7,9 | OPENING positivo atomicamente com ledger e dois eventos | PASS | financial/service.go,events.go | F: CreateWalletOpeningAndZero; P: CreateWalletZeroAndOpening | E2,E3,E8 | WagerTransactionProcessed + WalletBalanceChanged | G |
| R36 / 7,9 | OPENING zero sem transação/ledger/eventos | PASS | financial/service.go | F,P | E2,E3 | Wallet 0.00/versão 1 | G |
| R37 / 7 | BET positivo/saldo suficiente/insuficiência auditada | PASS | financial/service.go, wallet/ | F,P,X | E2,E3,E10 | Rejeição estável INSUFFICIENT_FUNDS, sem DEBIT | G |
| R38 / 7 | WIN positivo e referência opcional BET mesma rodada | PASS | financial/service.go,reversals.go; sqs/contract.go | F: WinCanReferenceProcessedBetInSameRound, H | E2,E3 | Escopo/estado da referência validados; auditoria também verifica | G |
| R39 / 7 | LOSS zero/moeda/Processed sem ledger/saldo/versão/BalanceChanged | PASS | service.go, events.go | F,P,S,O | E2,E3 | WagerTransactionProcessed preservado | G |
| R40 / 7 | REFUND integral de BET e ROLLBACK BET/WIN/REFUND | PASS | financial/reversals.go, references.go | F,R,S | E2,E3 | Reversões positivas; LOSS não reversível | G |
| R41 / 7 | Escopo provider/player/wallet/currency/round, amount igual | PASS | reversals.go, references.go | F: ReferenceValidationFailures; R: ReferenceValidationAndProviderScope | E2,E3 | Referência resolve por provider+external | G |
| R42 / 7,8 | Reversão única, REFUND vs ROLLBACK sem devolução dupla | PASS | migration 4, Reversals.Claim, reversals.go | F,R: ConcurrentReversals; X | E2,E3,E10 | Um sucesso sobre débito, demais ALREADY_REVERSED | G |
| R43 / 7 | Reversão debitando sem saldo rejeita com código próprio | PASS | reversals.go | F,R: InsufficientFundsForReversal | E2,E3 | Código distinto de BET sem saldo | G |
| R44 / 7 | PENDING_REFERENCE persistido, referência ausente/pendente/inválida | PASS | pending.go, reversals.go, references.go, migration 4 | F,R,S,H | E2,E3 | Referência rejeitada/falha gera rejeição definitiva; não movimenta | G |
| R45 / 7 | Backoff exponencial/tentativas/TTL/worker restart/SKIP LOCKED | PASS | pending.go,worker.go, config/, references.go | F,R: PendingWorkersAndSkipLocked/PendingExpirationAndInvalidReference | E2,E3 | Esgotamento REFERENCE_NOT_FOUND + evento, sem saldo | G |
| R46 / 7 | FailureCode estável e resultados rejeitados/pending persistidos | PASS | financial/types.go, idempotency.go | F,R,H | E2,E3 | Worker atualiza snapshot pendente atomicamente | G |
| R47 / 8,13 | Duas BET 80 sobre 100: 1 processada/1 rejeitada/saldo20/1DEBIT/v2 | PASS | service.go, wallet locking | P: TwoBetsAgainst100; X: ThreeProcessesFinancialConcurrency | E2,E3,E10 | Conferido diretamente no banco | G |
| R48 / 8,13 | 50 tentativas idênticas em paralelo, um efeito | PASS | idempotency.go, UoW | P: FiftyConcurrentReplays; H; X | E2,E3,E10 | 49 replays, um debit; FIFO não mascara duplicidade | G |
| R49 / 8,13 | Pelo menos três processos independentes com memória/conexões próprias | PASS | final_process_integration_test.go | X: ThreeProcesses*, ThreeConsumerProcesses*, ThreePublisherProcesses* | E2,E3,E10 | OS processes; testes HTTP anteriores são três servidores no mesmo processo | G |
| R50 / 9 | POST/GET wallet, ledger cursor/ordering estável | PASS | http/wallets.go, financial/reads.go, financial_reads.go | H,F | E2,E3,E8 | Corpo original, aliases, limit/cursor wallet-bound | G |
| R51 / 9 | GET transaction interno/externo permite acompanhar estado/failure | PASS | http/transactions.go, financial/reads.go | H,A | E2,E3,E8 | Authorize antes de retornar snapshot; estrangeiro 403 | G |
| R52 / 9 | POST wager, DTO/JSON/Money/key/correlation/errors/status | PASS | http/financial.go,transactions.go,errors.go | H: boundary/contracts | E2,E3,E7,E8 | Strict JSON, aliases coerentes, 64KiB/content-type | G |
| R53 / 9 | Idempotency-Key obrigatória preservada, namespace provider | PASS | http/transactions.go, financial/authorized.go, idempotency.go | F,H,A | E2,E3 | Não substitui key por external ID | G |
| R54 / 9 | Canonical hash negócio, ordem/normalização, HTTP/SQS equivalentes | PASS | financial/hash.go, sqs/contract.go, delivery.go | F,H: OriginalHTTPAndSQSContractReplay | E2,E3,E8 | v3 sorted JSON; key/metadata fora; formatos originais equivalentes | v1/v2 legados preservam hash histórico |
| R55 / 9 | Replay observed balance original, conflitos e external ID único | PASS | service.go, idempotency.go, delivery.go | F,P,H,S | E2,E3,E8 | Replays depois de WIN não leem novo saldo | G |
| R56 / 9,12 | Reconciliation consistente, divergências, read-only/log/metric | PASS | reconciliation.go (app/PG), http/, telemetry | C,V,F | E2,E3,E8 | stored−calculated, reporte estruturado, sem repair | G |
| R57 / 9 | Live público; ready PG+SQS habilitados | PASS | http/health.go, sqs/health.go, app.go | health_test; X: ReadinessProbesRealBrokerQueues; V | E2,E3,E8,E9 | PG/broker failure 503 ready; live permanece 200 | Cache broker 1s |
| R58 / 10 | FIFO/DLQ/provision/redrive/group/dedup | PASS | infra/localstack/10-queues.py, sqs/contract.go, consumer.go | S: ProvisionedQueues/FIFOOrdering/PermanentFailuresReachRealDLQ | E2,E3,E5 | Grupos por wallet, dedup por messageId; maxReceiveCount real | FIFO dedup tem janela limitada |
| R59 / 10 | Envelope original/key data/messageId/hash conflict | PASS | sqs/contract.go, delivery.go | sqs/final_contract_test; H,S | E2,E3,E8 | Original nested e flat legado; Inbox usa messageId do envelope | Produtores devem ser confiáveis |
| R60 / 10 | Commit antes de Delete; business rejection ACK | PASS | sqs/consumer.go, delivery.go | S: AllFinancialPaths/CrashAndDeleteRecovery; X Kill | E2,E3,E10 | ACK só após durable completion | ACK failure permite redelivery |
| R61 / 10 | Transient exponential backoff; permanente/esgotado DLQ real | PASS | consumer.go, config/, LocalStack redrive | sqs/final_contract_test; S real DLQ e retry | E2,E3 | Visibility backoff sem ACK; conflicts permanentes | DLQ requer operação posterior manual |
| R62 / 10,13 | Crash antes/após commit, restart e redelivery sem duplicar | PASS | final_crash_integration_test, UoW, Inbox | X: ConsumerAbruptProcessDeathAndRestart; S | E2,E3,E10 | Kill real, rollback parcial e recuperação por outro OS process | Não simula perda de disco PG |
| R63 / 10,13 | 50 entregas reais/3 consumers/wallets independentes | PASS | consumer.go; final_process_integration_test | X: ThreeConsumerProcessesFiftyRealDuplicates; S independent wallets | E2,E3,E10 | 50 receives SDK comprovados, dedup FIFO burlado nos fixtures | G |
| R64 / 11 | Eventos publicados somente após commit, rollback não publica | PASS | events.go, publication.go, publisher.go | O: UncommittedAndRolledBackEventsNeverPublished | E2,E3 | Inspeção publisher separado do UoW e SDK real | G |
| R65 / 11 | Claim/lease/fencing/SKIP LOCKED sem lock durante rede | PASS | postgres/publication.go, migration 5, outbox/publisher.go | O: EligibilityLeaseRecoveryAndFencing/NetworkDoesNotHoldSQLLocks | E2,E3 | Lock curto; stale publisher não marca nova lease | G |
| R66 / 11 | Stable eventId/dedup, retry_count/next_attempt/backoff | PASS | event_sender.go, publisher.go, publication.go | O: RealSendFailureAndPersistentRetry; publisher_test | E2,E3 | Retry durable e backoff saturado; mark após send | Send aceito + mark falho duplica publicação |
| R67 / 11 | Crash boundaries/outbox restart, sem evento confirmado perdido | PASS | publisher.go, publication.go | O: CrashBoundariesAndMarkFailure/EligibilityLeaseRecovery | E2,E3 | Fault injection + lease expirada + nova instância | Testes não fazem SIGKILL do publisher |
| R68 / 11,13 | Múltiplos publishers/100 eventos/order por aggregate | PASS | publisher.go, publication.go; final_process_integration_test | O: 100EventsThreePublishersAndOrdering; X: ThreePublisherProcessesHundredEvents | E2,E3,E10 | 100 eventIds encontrados, backlog zero; ordem verificada por aggregate | Não há ordem global entre aggregates |
| R69 / 11 | At-least-once publication documentada, downstream dedup | PASS | README, ARCHITECTURE, event_sender.go | O: CrashBoundariesAndMarkFailure | E2,E3 | Não promete exactly-once delivery/publication | Downstream deve manter Inbox própria |
| R70 / 12 | Métricas HTTP/financial/Inbox/SQS/Outbox/pending/recon/PG | PASS | observability/metrics.go, telemetry.go, operational*.go | V,F: telemetry; outbox/sqs metrics_test | E2,E3,E8 | Interno somente, métricas pós confirmação onde aplicável | Counters locais, gauges aproximados/stale |
| R71 / 12 | Baixa cardinalidade, logs JSON/correlação/sem secrets/payload | PASS | logger.go, context.go, http/logging.go, metrics.go | V,H | E2,E3 | Whitelists de labels; sanitização; correlação propagada | Fx tem diagnósticos em texto stderr |
| R72 / 13 | Unitários Money/wallet/FSM/operações/zero/hash | PASS | domain/*_test, financial/*_test | M,W,T,F | E2,E3 | Regressões novas preservam casos anteriores | G |
| R73 / 13 | Integração real PG/IdP/SQS, atomicidade/retry/DLQ | PASS | integration_test em PG/oidc/application | P,R,S,O,A,H,C,V,X | E2,E3 | 70 integrações principais, nenhuma dependência totalmente mockada | G |
| R74 / 13 | Reconciliação >100 entries, corrupção, snapshots concorrentes | PASS | PG/app reconciliation e fixtures | C | E2,E3 | Comparação exata antes/depois de todas as tabelas: sem mutação/eventos | Histórico não tem bound de memória |
| R75 / 13 | Concorrência reversões/pending/recon, sem double spend/lost updates | PASS | references.go, pending.go, reconciliation.go | R,C,X | E2,E3,E10 | Contenção cross-type/skip-locked e snapshots | Testes não provam toda topologia de deadlocks |
| R76 / 13,15 | Race/test/vet/gofmt com integração habilitada | PASS | Dockerfile, docker-compose.audit.yml, testes | E2/E3/E4 | E2–E4 | Bloqueio GCC Windows resolvido via Linux, sem skip financeiro | Requer host networking alcançando dependências |
| R77 / 15 | README/env/Compose/Dockerfile/OpenAPI/arquitetura reproduzíveis | PASS | README, .env.example, .gitignore, Dockerfile, Compose, docs | E5/E7/E8/E9 | E5–E9 | Original preservado; sem paths locais ou secrets reais; API host validada | Windows container inbound: R79 |
| R78 / extensão de segurança | IAM AWS/TLS/role PostgreSQL restrita em produção | NOT_VERIFIED | Modelo/policies em ARCHITECTURE fase 12 | Sem ambiente AWS/produção nesta auditoria | Inspeção/local apenas | Não houve deploy/account externo nem teste de queue policies reais | Necessário antes de produção |
| R79 / execução alternativa | Publicação API Docker host-network para Windows | PARTIAL | Compose perfil app, Dockerfile | curl Linux E9; host Go E8 | E8,E9 | Runtime funciona no Docker; entrada pela porta Windows não funcionou | Usar API host; não alegar Windows container e2e |
| R80 / extensão de recovery | SIGKILL real de publisher antes/depois de Send | NOT_VERIFIED | outbox tests de fault/lease | O verifica boundaries por simulação controlada | E2,E3 | Comportamento de lease/dup/recovery provado; morte OS publisher não executada | Não extrapolar simulação para kill físico |

## Ressalvas, causa, impacto e ações

1. **R09/R78, broker/produção:** credenciais locais são dummy e não constituem
   autorização IAM. Impacto: dar acesso direto de escrita a providers na fila comum
   permitiria alegar outro provider no body. Correção local: guard de dummy/endpoint,
   entrada original validada, isolamento HTTP e política de produtor interno documentada.
   Pendência externa: executar least-privilege/queue-policy tests com identidade AWS real,
   workload identity/TLS e role SQL distinta da role de migrations. Nenhum segredo real foi pedido/criado.
2. **R12, SQL fora da aplicação:** o desenho usa constraints por registro e UoW,
   sem assert diferido global ledger/Outbox para SQL arbitrário. Impacto: usuário com UPDATE
   direto pode criar saldo sem evidência correspondente. Proteções existentes e novas
   foram verificadas; recon detecta corrupção. Resolver completamente exigiria mudar
   contrato de escrita/roles ou triggers cross-table e fixtures históricos; não foi
   introduzida uma reimplementação ampla disfarçada de auditoria. Restringir credenciais
   de execução e jamais permitir acesso SQL direto aos providers.
3. **R25, compatibilidade:** fases anteriores aceitavam type sem jogo e SQS flat sem key.
   Correção mínima: original kind/game/envelope/key, persistência e hash v3, com regressões
   reais HTTP→SQS e SQS→HTTP. Hashes/metadata desconhecida antigos permanecem intactos.
   Retirar legado exigiria política de depreciação e migração de contratos, sem inventar históricos.
4. **R30, FAILED:** domínio/schema suportam FAILED, mas pipeline concreto prioriza rollback,
   retry e DLQ. Impacto: infraestrutura permanente não gera automaticamente novo resultado
   FAILED no banco. DLQ e códigos de negócio não foram falsamente mapeados a FAILED;
   resolver esse caminho requer política operacional explícita de persistência/redrive.
5. **R79, Desktop:** conectividade de saída suficiente para o runner não garantiu entrada
   na API pela rede host Windows. Dockerfile foi construído e runtime respondeu em Linux;
   README/smoke usam host Go no Windows. Não foram alteradas configurações globais do Desktop
   nem o issuer para contornar validação OIDC.
6. **R80, publisher kill:** testes reais PG/SQS reproduzem send-before-mark e leases expiradas
   com falhas determinísticas; consumer agora também sofre Kill real. Publisher SIGKILL
   é uma extensão de prova ainda não executada, distinta de falha funcional observada.

Não há requisito marcado FAIL no escopo testado. PARTIAL não significa PASS pleno.
Sem IAM/roles de produção, garantias de escrita SQL externa e tratamento automático FAILED,
a entrega recebe **NOT_READY**. O critério eliminatório de acesso não autorizado
precisa de prova de enforcement da mensageria, além dos resultados HTTP que já passaram.

## Critérios eliminatórios

| Critério original | Resultado | Prova |
| --- | --- | --- |
| Autenticação efetiva nos endpoints de negócio | PASS | A/H/E8, faltante/inválido/expirado→401 |
| Impedir acesso não autorizado a operações/transações em todos os canais | PARTIAL | HTTP PASS em A/H/C/V/E8; controle de produtores/credenciais/policies SQS não comprovado (R09/R78) |
| Não calcular dinheiro em ponto flutuante | PASS | M/F/PG/DTO + inspeção, R16–R20 |
| Impedir saldo negativo por concorrência | PASS | P/X/E10, saldo20/v2/umDEBIT |
| Impedir movimentação duplicada | PASS | 50 HTTP, 50 receives SQS, cross-transport, reversões e Kill |
| Idempotência não restrita à memória | PASS | SQL claims/results/Inbox, novos processos e restart |
| Não depender de uma única instância | PASS | Três OS processes, pools/SDK/memórias distintos |
| Não publicar antes de commit | PASS | O: uncommitted/rollback invisíveis; sender somente publisher |
| Ledger auditável | PASS | Append-only/SQL constraints + reconciliation read-only |
| Não substituir PG/SQS/IdP integralmente por mocks | PASS | E2/E3: 70 integrações com três containers reais |

Esses resultados são do código atual no ambiente descrito. Não atribuem notas do
avaliador nem prometem ausência de falhas fora dos cenários executados.
