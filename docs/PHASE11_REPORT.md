# Relatório da Fase 11

Observabilidade e hardening operacional concluídos, preservando os casos de uso,
contratos financeiros, ledger, Inbox/Outbox e reconciliação das fases anteriores.
Os resultados abaixo correspondem à execução local em 7 de outubro de 2026.
Instruções de reprodução e interpretação: [PHASE11_OPERATIONS.md](PHASE11_OPERATIONS.md).

## 1. Arquivos criados

- `internal/ports/telemetry.go`
- `internal/config/metrics.go`
- `internal/config/metrics_test.go`
- `internal/observability/metrics.go`
- `internal/observability/metrics_test.go`
- `internal/adapter/postgres/operational.go`
- `internal/adapter/http/metrics.go`
- `internal/adapter/http/metrics_test.go`
- `internal/adapter/sqs/metrics_test.go`
- `internal/application/financial/telemetry.go`
- `internal/application/financial/telemetry_test.go`
- `internal/application/operational_metrics.go`
- `internal/application/operational_metrics_test.go`
- `internal/application/observability_integration_test.go`
- `internal/application/outbox/metrics_test.go`
- `docs/PHASE11_OPERATIONS.md`
- `docs/PHASE11_REPORT.md`

## 2. Arquivos alterados

- `.env.example`, `go.mod`, `go.sum`, `README.md`.
- `docs/ARCHITECTURE.md`, `docs/PHASE8_OPERATIONS.md`, `docs/openapi.yaml`.
- `internal/config/config.go`, `internal/config/oidc.go`, `internal/config/oidc_test.go`.
- `internal/observability/logger.go`.
- `internal/adapter/postgres/pool.go`, `internal/adapter/postgres/pool_test.go`.
- `internal/adapter/http/correlation.go`, `internal/adapter/http/health.go`,
  `internal/adapter/http/server.go`, `internal/adapter/http/financial_test.go`,
  `internal/adapter/http/reconciliation_test.go`.
- `internal/adapter/sqs/consumer.go`.
- `internal/application/app.go`, `internal/application/pending_worker.go`,
  `internal/application/sqs_consumer.go`, `internal/application/outbox_publisher.go`,
  `internal/application/http_financial_integration_test.go`.
- `internal/application/financial/service.go`, `internal/application/financial/delivery.go`,
  `internal/application/financial/pending.go`, `internal/application/financial/reconciliation.go`.
- `internal/application/outbox/publisher.go`.

O workspace já continha alterações e arquivos não rastreados das fases 9/10.
Este inventário descreve o trabalho da fase 11; o estado anterior foi preservado,
sem reset, descarte de alterações ou commit automático.

## 3. Dependências

Adicionado o cliente oficial `github.com/prometheus/client_golang v1.24.1`.
`go mod tidy` registrou suas dependências transitivas: perks, xxhash, goautoneg,
client_model, common, procfs e protobuf. Também atualizou `golang.org/x/sync`
para v0.22.0, `x/sys` para v0.47.0 e `x/text` para v0.40.0, conforme o grafo
resolvido. `puddle/v2 v2.2.2`, já presente, passou a dependência direta por ser usado
na regressão de fechamento do pool. A versão Go do projeto foi mantida. O validador OpenAPI foi executado
com versão fixada e não foi adicionado como dependência da aplicação.

## 4. Métricas Prometheus

Registry privado por instância Fx, collectors oficiais Go/process, counters,
gauges e histogramas sob o prefixo `wagering_`. Labels passam por whitelist
finita antes de criar séries; valores desconhecidos são normalizados. Nenhuma
label contém walletId, playerId, providerId, transactionId, messageId ou eventId.
O catálogo completo está no documento operacional. Estatísticas usam segundos
e contagens, sem converter valores monetários para float.

## 5. Segurança de /metrics

`GET /metrics` exige Bearer OIDC válido e autorização interna, usando os mesmos
componentes existentes. Sem token: 401; provider autenticado: 403; identidade
interna: 200. Não há modo público. Desabilitar OIDC não libera acesso.
`METRICS_ENABLED=false` remove a rota, retornando 404. Foram documentados token
client_credentials do Keycloak local e consulta autenticada via PowerShell.

## 6. Métricas HTTP

`http_requests_total{method,route,status}`, `http_request_duration_seconds{method,route}`
e `http_in_flight` incluem autenticação e respostas de erro. Rotas usam templates
como `/wallets/{walletId}`, ou classificação limitada para rota não encontrada.
Métodos desconhecidos são agrupados. URLs concretas não criam séries.

## 7. Métricas financeiras

O core compartilhado observa tentativas, duração, novas transações confirmadas
por operação/estado inicial e movimentos confirmados. HTTP/SQS não duplicam essa
contagem. Os estados PROCESSED, REJECTED, PENDING_REFERENCE e FAILED são suportados;
o core atual não cria transações FAILED. Commit falho não conta sucesso.
LOSS processada e BET rejeitada não contam movimento. OPENING positivo conta
operação/movimento; criação de wallet com zero não gera OPENING financeiro.

## 8. Métricas de idempotência

`idempotency_events_total{outcome}` distingue replay, duplicate Inbox,
external_duplicate, idempotency_conflict, external_conflict e inbox_hash_conflict.
Replay/duplicate não criam nova operação nem movimento. Tentativas continuam
observáveis separadamente dos efeitos persistidos.

## 9. Métricas SQS

`sqs_events_total{outcome}` mede received, completed, duplicate, transient_failure,
permanent_failure, delete_failure e poll_failure. Há duração de processamento
e gauge de chamadas Process em andamento. Completed exige resultado durável e
DeleteMessage aceito; falha de processamento ou ACK não conta completed.
Retries são observados como falhas transitórias/redelivery, preservando retention
e a política anterior do consumer.

## 10. Métricas DLQ

SDK real GetQueueAttributes coleta a soma aproximada de mensagens visíveis e
em andamento. Gauges indicam coleta habilitada, último sucesso e timestamp da
última amostra válida. Falha mantém o valor anterior e marca success=0.
Não há contador alegando que o consumer realizou redrive: essa ação pertence ao
SQS. O teste de profundidade envia uma mensagem diretamente à DLQ como fixture;
não é apresentado como prova de redrive automático.

## 11. Métricas Outbox

Counters de published, publish_failure, retry_scheduled, lease_lost, mark_failure
e store_failure, além de duração da tentativa. Published só incrementa depois
de envio aceito e published_at confirmado no PostgreSQL. Gauges SQL cacheados
medem eventos não publicados, retries futuros e idade do evento mais antigo.
EventId, lease, ordenação e recuperação de crash do publisher foram preservados.

## 12. Métricas Pending

Backlog de referências não concluídas e counters de retry, resolved, expired,
rejected e error. Resultados persistidos são contados depois do commit. Retry
não cria operação. Resolução registra seu movimento, sem contar uma nova
transação PROCESSED: a operação já foi contada no estado inicial PENDING_REFERENCE.
TTL/max attempts continuam seguindo a política existente. Duração inclui polls
vazios; estes não incrementam counters de resultados.

## 13. Métricas Reconciliation

Execuções CONSISTENT, DIVERGENT e ERROR, duração e ocorrências por código limitado
de divergência, após autorização. Auditorias repetidas contam novamente suas
constatações; não representam wallets distintas. Não há label walletId nem log
de histórico completo. A reconciliação permanece somente leitura financeira.

## 14. Métricas PostgreSQL

Collector usa pgxpool.Stat para conexões totais, em uso, ociosas, máximo,
aquisições, tempo acumulado, cancelamentos e aquisições com pool vazio, sem SQL.
Uma consulta operacional limitada amostra Outbox não publicada e pending,
com contexto/timeout e intervalo configuráveis. Scrape não executa consultas
de backlog nem chamadas SQS. Falhas de dependência têm labels limitadas.

## 15. Logging

Mantido slog JSON. Core/Inbox, HTTP, SQS, Outbox, pending, reconciliation e
lifecycle têm campos úteis disponíveis: correlação, IDs, tipo, estado, duração
e retries. IDs ficam em logs, não em métricas. Credenciais, tokens, Authorization,
payload, DSN e receipt/claim tokens não são registrados. Logger redige chaves
sensíveis e substitui objetos error por classificação fixa; erros de configuração
e conexão também foram sanitizados. Testes verificam ausência de segredos.

## 16. CorrelationId

Mantida propagação HTTP → core → Outbox e SQS → Inbox → core → Outbox.
Pending usa a correlação persistida. Consumer coloca correlação da mensagem no
contexto; não gera novo ID a cada camada. Header HTTP válido é preservado;
valores repetidos, com whitespace/controle ou acima de 128 caracteres recebem
ID novo seguro, também retornado na resposta. Integração verifica correlação
persistida no Outbox de uma operação SQS.

## 17. Health

Liveness pública continua independente de indisponibilidade temporária de SQS.
Readiness exige inicialização crítica concluída e ping PostgreSQL; banco/pool
indisponível retorna 503 enquanto liveness retorna 200. Readiness só é habilitada
após todos os hooks OnStart. `wagering_ready` reflete o último probe e fica zero
no shutdown; não é um monitor de dependências em background.

## 18. Graceful shutdown

Hook final de startup marca unready primeiro no shutdown. Coleta é cancelada e
aguardada; consumer/publisher param polling e drenam; pending cancela e aguarda
cleanup. HTTP recusa trabalho novo durante drenagem, faz Shutdown, força Close
em timeout e aguarda handlers/Serve. Pool fecha por último. Nenhum evento ou
mensagem é confirmado antecipadamente. Testes cobrem drenagem, cancelamento da
coleta e ciclo Fx real com listener, workers e pool.

## 19. Configuração

Adicionados somente `METRICS_ENABLED=true`, `METRICS_COLLECT_INTERVAL=15s` e
`METRICS_COLLECT_TIMEOUT=2s`. Intervalo mínimo de 1s, timeout positivo e menor que
intervalo quando habilitado. Endpoint fixo e proteção interna obrigatória estão
documentados em `.env.example`; LOG_LEVEL e timeouts existentes são reutilizados.
Bool/durações inválidas falham sem ecoar valores sensíveis. Audience OIDC vazia
é aceita apenas na compatibilidade development/test; outros ambientes a exigem.

## 20. Docker Compose

Compose preservado com PostgreSQL, LocalStack e Keycloak. Optou-se pela alternativa
permitida de endpoint autenticado com comandos suficientes para validação,
sem servidor Prometheus/Grafana adicional. Backend continua no host conforme
fase 8. Documento operacional fornece consultas e exemplos PromQL para um
Prometheus externo com OAuth2 client_credentials.

## 21. Testes unitários

Novos testes cobrem registry privado, counters/histograms concorrentes, labels
normalizados/limitados, disable, logs seguros, endpoint 401/403/200/404,
correlation ID, drenagem HTTP, configurações inválidas, commit falho sem sucesso,
replay/rejeição sem movimento, resultados de reconciliação, consumer/ACK/poll,
publisher/send/mark/lease e coleta stale/cancelamento. Test doubles são restritos
a testes unitários; provas de integração usam dependências reais.

## 22. Testes de integração

`TestIntegrationObservabilityHTTPFinancialAndReadiness` usa PostgreSQL e tokens
Keycloak reais: proteção /metrics, rotas normalizadas, BET processada/rejeitada,
replay sem movimento, conflitos, pending/retry/resolução/expiração,
CONSISTENT/DIVERGENT/ERROR, pool indisponível/readiness e shutdown.

`TestIntegrationObservabilitySQSOutboxDLQAndLifecycle` usa PostgreSQL e LocalStack
reais: recebimento, Inbox duplicate/hash conflict, external duplicate, falha
DeleteMessage/poll, publicação/retry duráveis, backlog/lag, atributos DLQ,
correlação persistida e encerramento Fx. Filas e schemas de teste são descartáveis.
Nenhum mock substitui as dependências nessas provas.

## 23. Regressões financeiras

Suíte anterior preservada e aprovada junto com os novos testes: valores int64,
overflow, saldo/versionamento, ledger atômico, idempotência, concorrência,
refund/rollback únicos, pending, Inbox commit antes de ACK, Outbox após commit,
multi-instância/multi-publisher, crash recovery e reconciliação. Não foram
adicionadas regras financeiras nem enfraquecidas assertions existentes.

## 24. Regressões de segurança

Testes anteriores de autenticação, autorização, isolamento de provider,
internal-only, issuer/audience, strict JSON, Content-Type, limites, envelope e
idempotency key continuam passando. Acrescentadas provas de /metrics restrito,
sem labels com IDs, logs sem segredo, correlação limitada e proibição de audience
vazia fora de development/test. Nenhuma rota financeira ganhou bypass.

## 25. go test

`go test ./... -count=1`: **PASS** com TEST_DATABASE_URL, TEST_SQS_ENDPOINT,
TEST_OIDC_ISSUER_URL e TEST_KEYCLOAK_HEALTH_URL apontando para o Compose real.
A execução final ocorreu após as últimas mudanças de produção; todos os pacotes
passaram. Os dois novos testes de integração também passaram isoladamente.
A regressão final de falha de startup/sanitização do pool passou em execução
focada adicional, sem novas mudanças no código de produção.
Arquivos Go alterados foram formatados com gofmt.

## 26. go vet

`go vet ./...`: **PASS**, após as últimas alterações de produção.

## 27. go test -race

Tentado com CGO_ENABLED=1. **Não executado**: compilação runtime/cgo falhou porque
`gcc` não está no PATH deste Windows. Isso não é resultado positivo do detector.
Com compilador C instalado, executar o comando documentado para obter essa prova.

## 28. OpenAPI

`go run github.com/getkin/kin-openapi/cmd/validate@v0.149.0 docs/openapi.yaml`:
**PASS**. Documento v0.11.0 inclui /metrics no mesmo servidor, Bearer interno,
formatos de exposição, respostas de autorização e 404 quando desabilitado.
Contratos financeiros foram preservados.

## 29. Compose/health

`docker compose --env-file .env.example config --quiet`: **PASS**.
`docker compose --env-file .env.example up -d --build --wait --wait-timeout 240`:
**PASS**. PostgreSQL, LocalStack e Keycloak ficaram healthy. A suíte real confirmou
conexão com as três dependências. Readiness/liveness da aplicação também foram
verificadas nos cenários normais e de indisponibilidade do pool.

## 30. Migrations

Nenhuma migration nova e nenhuma migration anterior alterada. Schema permanece
na versão 5; consulta real a schema_migrations confirmou version=5 e dirty=false.

## 31. Bugs anteriores corrigidos

- Audience vazia podia desativar essa comparação fora de ambientes locais:
  validação agora rejeita a configuração, com regressão unitária.
- Correlação HTTP aceitava entradas excessivas/inadequadas: limitada e validada,
  com testes de headers inválidos/repetidos e propagação válida.
- Timeout de shutdown podia deixar handlers ou pending usando o pool em fechamento:
  agora há drenagem/join explícito, testados em unidade e lifecycle real.
- Pool criado em startup com ping falho podia ficar sem hook OnStop: fecha no
  próprio caminho de erro, com regressão verificando pool fechado sem OnStop.
  Erros crus de conexão/configuração poderiam incluir credenciais: substituídos
  por mensagens seguras; startup, configuração e redaction cobertos por testes.
- Readiness podia ser habilitada antes de todos os hooks críticos terminarem:
  habilitada no hook final, desabilitada primeiro ao parar; lifecycle real cobre
  o comportamento operacional. Não houve correção de regra financeira.

## 32. Limitações técnicas

Counters são locais ao processo: reiniciam e um crash entre commit e observação
pode perder a métrica. Não substituem ledger, Inbox ou registros duráveis. Gauges
compartilhados de backlog/DLQ não devem ser somados entre réplicas. Coleta é
periódica e mantém amostra stale em falha, explicitada por success/timestamp.
Profundidade DLQ é aproximada, cobre visible + not visible e não prova redrive.
Readiness gauge depende de probes. Cleanup SQL limitado pode ultrapassar a
janela de grace para garantir join antes de fechar pool. Não há servidor
Prometheus embutido no Compose. Detector de race depende do gcc ausente.

## 33. Encerramento do escopo

Não foram implementados auto-repair, tracing avançado, auditoria final ou fases
posteriores. A fase 11 termina com instrumentação, hardening mínimo, testes e
documentação; a reconciliação existente continua detectando sem reparar.
