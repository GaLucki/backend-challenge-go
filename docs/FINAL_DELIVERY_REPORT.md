# Relatório final de entrega — Fase 12

Data: 2026-10-07. Classificação: **NOT_READY** para encerrar todos os critérios
obrigatórios. A avaliação local é reproduzível e os testes passaram, mas o controle
de acesso ao broker não fecha a prova eliminatória de acesso não autorizado a operações.
A [matriz completa](FINAL_AUDIT.md) contém 80 verificações: **73 PASS, 5 PARTIAL,
0 FAIL, 2 NOT_VERIFIED**. Fonte de verdade preservada: [CHALLENGE_SPEC.md](CHALLENGE_SPEC.md).
As referências E1–E10 e os grupos de testes abaixo estão definidos na matriz.

## 1. Requisitos PASS

73 linhas da matriz: stack Go/Fx/PG/SQS/IdP, ciclo de vida, autenticação e autorização,
Money, operações/abertura/reversões, locks/versão, idempotência, Inbox/Outbox,
pending/retries/DLQ, API original, reconciliation, observabilidade e documentação.
PASS descreve o escopo executado; não extrapola para produção ou todos os possíveis interleavings.

## 2. Requisitos PARTIAL

R09: broker local com dummy credentials, enforcement IAM não demonstrado.
R12: schema/UoW protegem o fluxo da aplicação, mas SQL arbitrário de saldo não
exige ledger/Outbox por constraint diferida. R25: protocolo legado pode omitir jogo/key
SQS. R30: FAILED existe no domínio/schema, sem pipeline automático de persistência
de falha permanente de infraestrutura. R79: imagem funciona na rede Linux, mas host
networking não publicou sua porta para este Windows; execução Go host foi validada.

## 3. Requisitos FAIL

Nenhum na matriz após as correções, no escopo descrito. PARTIAL não foi convertido
em PASS por ausência de testes. Sem reinterpretar a matriz como certificação de produção.

## 4. Requisitos NOT_VERIFIED

R78: IAM AWS, TLS/identidades/role SQL de produção. R80: SIGKILL físico de publisher;
seus crash boundaries/recovery foram exercitados por falhas determinísticas e lease
expirada com PG/SQS reais. Não se afirma que esse método equivale a kill físico.

## 5. Critérios eliminatórios

Nove critérios originais: PASS no ambiente local. O critério de acesso não autorizado
é PARTIAL: HTTP foi comprovado; mensageria ainda exige prova de credenciais/policies
efetivamente aplicadas. Sem endpoint HTTP de negócio
anônimo; isolamento e internal-only reais; nenhum cálculo monetário float;
saldo não negativo por concorrência; efeito financeiro único; idempotência durável;
três processos independentes; publicação depois de commit; ledger auditável;
integração real com PG/SQS/IdP. A tabela de provas está no final da matriz.

## 6. Correções realizadas

| Problema / causa | Impacto | Correção mínima e regressão |
| --- | --- | --- |
| HTTP anterior exigia currency top-level e type, sem preservar jogo | Corpo original recusado / metadata perdida | Currency inferida de initialBalance; kind/gameId; aliases id/version/status/balance; integração original real H/E8 |
| Consumer só entendia envelope flat, sem data.idempotencyKey | Contrato original inválido e key HTTP/SQS sem continuidade | Parse nested estrito/normalização; namespace compartilhado e claim/result no mesmo UoW da Inbox; HTTP→SQS e SQS→HTTP |
| Hash antigo não incluía jogo e não usava ordenação de chaves | Payload de jogo diferente podia ser equivalente | SHA-256 v3 sobre mapa JSON ordenado; versões antigas preservadas; jogo diferente gera conflito |
| Sub negava MinInt64 antes de subtrair | Operações representáveis rejeitadas, como Min−Min | Sub direta com bounds; regressão Money |
| Money zero inválido negava/comparava; qualquer AAA passava como moeda | Value object inválido / código não ISO | Rejeita uninitialized; subset BRL/USD/EUR; regressões de códigos inválidos |
| Wallet descartava timestamps ao reidratar | Agregado não carregava metadata original | CreatedAt/UpdatedAt no agregado/SQL/read DTO, reidratação própria e teste de imutabilidade em débito recusado |
| WIN recusava toda referência | Referência opcional do contrato não funcionava | Validação de BET processada no mesmo escopo/round; auditor também verifica referência |
| REFUND/ROLLBACK aceitavam zero | Contradição com a especificação original | Positivos obrigatórios; regressões zero sem efeito; dois fixtures rollback-LOSS usam 1 cent mantendo REFERENCE_TYPE_INVALID |
| Schema não impedia outro OPENING positivo nem mutação terminal | Defesa do banco incompleta | Migration 6: unique positivo/terminal UPDATE+DELETE; teste UP/DOWN/UP |
| Readiness só verificava PG após startup | Broker indisponível podia manter ready | Probe command/event queues habilitadas, cache 1s/timeout; live independente; testes unitário e LocalStack |
| Falha transitória dependia só da visibility fixa | Sem backoff exponencial no processamento | ChangeMessageVisibility com delay exponencial limitado; sem Delete; regressão de receive count/cap |
| Conflitos de key/external ID eram classificados transitórios | Retry sem possibilidade de sucesso | Classificação permanente, redrive; rollback do novo claim/Inbox em conflito |
| Falta de Dockerfile raiz e GCC Windows | Build/race não reproduzíveis | Dockerfile Go 1.27.0 test CGO/GCC + runtime sem root, overlay audit e perfil app |
| Três instâncias dos testes antigos compartilham OS process | Não comprovava memória independente | Subprocessos reais financeiros/consumer/publisher, heaps/pools/SDK separados; Kill/restart real do consumer |
| Metadata podia ser devolvida em objeto parcialmente inicializado após erro | Contrato de erro do construtor inconsistente | NewExternal retorna zero antes de anexar GameID em falha; regressão |
| Dummy/plaintext broker configurável fora de dev | Configuração insegura podia escapar para produção | Rejeita dummy e HTTP em endpoint/URLs de filas habilitadas fora de development/test; regressão de URL explícita |

Não houve refactor geral, alteração de migrations 1–5 ou nova dependência de runtime.
O núcleo existente, a política de reversão única e as fronteiras transacionais foram preservados.

## 7. Arquivos alterados

Escopo desta fase; o workspace já continha mudanças das fases anteriores,
portanto `git diff` inteiro não equivale à lista de autoria da fase 12.

- Domínio: money/money.go; wager/transaction.go, errors.go, rehydrate.go;
  wallet/wallet.go, errors.go e novas regressões final_*_test.go.
- Aplicação: app.go; financial/types.go, service.go, reversals.go, hash.go,
  authorized.go, delivery.go, events.go, reconciliation.go e regressões final_*.
- Adapters: HTTP wallets.go/transactions.go/health.go + health_test;
  PostgreSQL repositories.go/financial_reads.go/reconciliation.go;
  SQS contract.go/consumer.go/new health.go e final_contract_test.
- Fixtures/regressões PG: financial_integration_test, pending_result_failure_integration_test,
  reconciliation_integration_test, final_process/final_crash/final_migrations/final_broker_health;
  application/outbox_lifecycle_test e final_contract_integration_test;
  financial/reversals_test conserva o assertion de LOSS inválida.
- Config: config.go/final_security_test.go. Sem nova variável obrigatória.
- Entrega: Dockerfile, .dockerignore, docker-compose.audit.yml, perfil app no Compose,
  .gitignore, scripts/final-smoke.ps1, README, arquitetura, OpenAPI e três documentos finais
  (spec preservada, auditoria e relatório). Relatórios históricos mantidos.

## 8. Testes unitários

M/W/T/F mais HTTP/OIDC/SQS/publisher/config/telemetry: parsing, overflow, moedas,
estado/rehidratação, zero por tipo, reversões/referências, hash/conflict/replay,
timeouts/drenagem, JWT/cache, autorização, cardinalidade/sanitização e regressões.
E2/E3 incluem todas essas verificações, com **222 testes principais PASS** no total.

## 9. Testes de integração

**70 testes principais de integração PASS** com PG, LocalStack e Keycloak reais.
Todas as quatro TEST_* estavam configuradas. Helpers não substituem a infraestrutura:
schemas/filas temporários reais, tokens reais, AWS SDK real e processos reais.
Somente TestFinalAuditProcessHelper pula no pai e executa nos filhos.

## 10. Testes financeiros

Zero/positivo de abertura, BET/WIN/LOSS, REFUND/ROLLBACK/ROLLBACK de REFUND,
escopo/amount/reference inválidos, insuficiência de reversão, overflow, versão,
ledger/eventos por resultado, observedBalance depois de outras movimentações,
UoW rollback de cada etapa e resultado pendente resolvido/expirado.
Money nunca passa por ponto flutuante.

## 11. Testes de concorrência

Duas BETs 80 sobre 100: exatamente uma PROCESSED, uma REJECTED,
saldo20/versão2/umDEBIT. 50 iguais: 49 replays/um débito. Três processos reais
disputam saldo/idempotência/reversões. Wallet independente avança com outra bloqueada.
REFUND, ROLLBACK e cross-type não duplicam devolução. Pending SKIP LOCKED,
consumers, publishers e reconciliation concorrentes passam em E2/E3.
Nenhum double spend/negative balance/lost update/deadlock não tratado observado;
os testes não provam ausência de todo possível interleaving.

## 12. Testes SQS

Provisioning/FIFO/group/dedup, Inbox/hash conflict, 50 SDK deliveries comprovadas
por três processos, business rejection ACK, falha transitória/visibility/backoff,
erro permanente/DLQ real, incomplete Inbox, HTTP/SQS original nos dois sentidos,
shutdown forçado. Kill real antes do commit e após commit antes de DeleteMessage,
com restart por outro processo e redelivery real: um único efeito financeiro.

## 13. Testes Outbox

Evento dentro da transação; uncommitted/rollback nunca publicado. IDs/dedup estáveis;
heads/SKIP LOCKED/lease/fencing, retry_count/next_attempt/backoff; send e mark failures,
lease expirado/restart, rede fora dos locks SQL. 100 eventos encontrados, backlog zero,
três publisher OS processes. Ordem por aggregate verificada pela suíte existente.
Publication continua at-least-once. SIGKILL físico do publisher não executado.

## 14. Testes OIDC

client_credentials provider-a/provider-b/internal-service, sujeitos distintos,
discovery/JWKS/assinatura reais, expiry com clock controlado, audience/issuer errados,
ausência/token inválido/tampering, spoofing, foreign transaction/replay, wallet/internal
restrito. Cache/rotation/outage complementados por unidade com issuer controlado.
Backend não cria tokens e não usa bypass de autenticação.

## 15. Testes Reconciliation

PG REPEATABLE READ READ ONLY, todos os tipos, mais de 100 entries, chain/saldo/
amount/direction/currency/state/reference/version, corrupção controlada em schemas
descartáveis, snapshots concorrentes/cancelamento. Evidência exata das tabelas
antes/depois prova que não altera estado/Outbox. DIVERGENT é 200 estruturado.
Campos originais incluídos e difference=stored−calculated.

## 16. Métricas e logs

Metrics interno 200, anônimo401/providers403; HTTP, finanças, SQS/Inbox,
Outbox/pending/recon/PG/runtime. Baixa cardinalidade, freshness de coleta,
post-commit effect counters, redaction/correlation, JSON da aplicação e drenagem.
Fx ainda emite diagnósticos próprios em stderr. Counters locais e gauges aproximados
não são contabilidade financeira durável nem prova de redrive por si só.

## 17. go test ./... -count=1

**PASS**. Windows E1 e Linux final E2, com infraestrutura real habilitada.
E1 antecede o último teste Kill e últimas validações; seus casos focados passaram
e E2 executou a fonte final completa. JSONL com 222 principais/70 integrações,
zero failures e nenhum integration skip.

## 18. go test -race ./... -count=1

**PASS** em Linux Docker, CGO=1/GCC12.2.0/Go1.27.0, com as mesmas três dependências
reais. Nenhuma race detectada. Não se declara race Windows, pois gcc continua
ausente no host; o bloqueio foi resolvido com runner reproduzível, sem omitir integração.

## 19. go vet ./...

**PASS** em Windows e Linux. Uma tentativa no sandbox Windows teve acesso ao cache
negado; rerun autorizado do mesmo comando terminou exit0. Gofmt com saída vazia.

## 20. Docker Compose

Config/default e overlay audit válidos; up --build --wait exit0, três dependências
healthy. Dockerfile test e runtime construídos. Perfil app runtime responde na
rede Docker Linux; Windows usa API host comprovada. Nenhum serviço pesado adicional.

## 21. PostgreSQL

Container real healthy, pgx/UoW/row locks/constraints confirmados. Primary migration6
dirty=false; somente forward migration aplicada. Fixtures isolados/cleanup preservam
dados primary. Smoke cria uma wallet exclusiva e preserva seu ledger de auditoria.
Role SQL de produção restrita e guarda de writes arbitrários permanecem ressalvas.

## 22. LocalStack

Healthy, três filas provisionadas, SDK consumer/publisher/DLQ reais; envelope original
do smoke foi concluído na Inbox e resultou em replay, sem outro débito. Broker não
é certificado como enforcement IAM. Nada afirma exactly-once delivery/publication.

## 23. Keycloak

Healthy, realm importado, três clientes client_credentials reais, JWT/JWKS validado.
Fixtures e embedded H2 de desenvolvimento explicitamente identificados.
Não foram introduzidos secrets reais, password grant ou emissão própria.

## 24. Migrations

Migrations 1–5 preservadas; 6 incremental. UP/DOWN/UP completo em schema descartável,
sem relações/funções restantes após DOWN, depois dados financeiros válidos e guards.
Banco principal não sofreu rollback e ficou version6/dirty=false.

## 25. OpenAPI

**PASS** do validator kin-openapi v0.149.0. OpenAPI3.0.3/versão0.12.0:
contratos originais/aliases, jogo, moedas suportadas, decimal canônico, aliases
condicionais, internal Bearer, error/status/cursor/recon/metrics e broker readiness.
Exemplos atualizados passaram junto com o documento.

## 26. README

Reescrito para execução reproduzível: pré-requisitos, .env, Docker/migrations,
tokens, API, filas, idempotência/garantias, race Linux e comandos de smoke.
Original preservado byte a byte, hash Git conferido. Arquitetura possui seção final
que prevalece sobre limitações históricas, incluindo antigo bloqueio GCC.
Sem dependência de caminhos pessoais no repositório.

## 27. Riscos residuais

IAM/roles/TLS externos não demonstrados; SQL direto pode escapar das invariantes
semânticas; legacy metadata ausente; infraestrutura esgotada não vira FAILED
automaticamente; publication repetida precisa Inbox downstream; histórico completo
de recon usa memória proporcional; counters podem perder observação entre commit/crash.
Desktop host networking não garante porta API Windows. Detalhes/causas/remediações
de cada PARTIAL/NOT_VERIFIED estão na matriz.

## 28. Limitações

ISO subset BRL/USD/EUR; sem contrato para currencies de outra escala; sem frontend,
auto-repair, double-entry ledger, load/RPS benchmark, tracing ou deploy externo.
Não há nota do avaliador ou promessa de exatamente uma entrega/publicação.
Testes com mocks complementam, sem substituir, as integrações reais.

## 29. Pendências

Antes de produção: IAM e queue policies efetivas/workload identity/TLS, role SQL
restrita e separada de migrations, política para SQL fora do UoW/FAILED/DLQ;
decisão de depreciação de legado. Para ampliar prova de recovery, executar SIGKILL
de publisher. Para API container no Windows, resolver publicação inbound Desktop
ou usar a execução host já documentada, preservando validação do issuer.
Nenhuma dessas pendências foi escondida por alteração de status ou skip de integração.

## 30. Recomendação de prontidão

**NOT_READY**. A entrega local é reproduzível e o bloqueio de race foi resolvido.
O critério eliminatório de impedir acesso não autorizado a operações está PARTIAL:
HTTP passou, mas a autorização dos produtores no broker não foi comprovada.
As cinco linhas PARTIAL e duas NOT_VERIFIED estão preservadas, sem afirmar que todos
os eliminatórios passaram. Após fechar essa prova e tratar as demais ressalvas, a
classificação pode ser reavaliada. Fase 12 encerrada; sem commit/push automático
e sem avanço para outra fase.
