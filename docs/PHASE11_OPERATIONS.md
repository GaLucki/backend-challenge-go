# Fase 11: observabilidade e operação

## Subir e consultar

Use [PHASE8_OPERATIONS.md](PHASE8_OPERATIONS.md) para as dependências Compose,
migrations5 e backend no host com OIDC. Não foi adicionado servidor Prometheus:
o endpoint autenticado e os comandos abaixo permitem validar as métricas. As
dependências continuam PostgreSQL, LocalStack e Keycloak; Grafana não é necessária.

```powershell
docker compose --env-file .env.example config --quiet
docker compose --env-file .env.example up -d --build --wait --wait-timeout 240
docker compose --env-file .env.example ps
```

Configurações já existentes DATABASE_URL/OIDC/SQS/Outbox são mantidas. Para o
backend Go, defina também as variáveis operacionais ou use seus defaults:

```powershell
$env:METRICS_ENABLED = 'true'
$env:METRICS_COLLECT_INTERVAL = '15s'
$env:METRICS_COLLECT_TIMEOUT = '2s'
$env:LOG_LEVEL = 'info'
$env:SHUTDOWN_TIMEOUT = '10s'
```

Compose lê .env.example; isso não injeta variáveis no processo Go executado no
host. O backend continua iniciado com `go run ./cmd/api` conforme a fase8.
Em outro terminal, solicite token ao Keycloak do Compose e consulte o backend:

```powershell
$phase11TokenUrl = 'http://localhost:8081/realms/jungle-gaming/protocol/openid-connect/token'
$phase11Token = (Invoke-RestMethod -Method Post -Uri $phase11TokenUrl -Body @{
  grant_type = 'client_credentials'
  client_id = 'internal-service'
  client_secret = 'local-dev-internal-service-secret'
}).access_token
$phase11Headers = @{ Authorization = "Bearer $phase11Token"; Accept = 'text/plain'; 'X-Correlation-ID' = 'phase11-metrics' }
$phase11Metrics = (Invoke-WebRequest -Method Get -Uri 'http://localhost:8080/metrics' -Headers $phase11Headers).Content
$phase11Metrics -split "`n" | Select-String '^wagering_'
```

Credenciais acima são exclusivamente locais. Tokens duram60s; obtenha outro quando
expirar. Não há credencial em query string. Sem token retorna401; provider-a/b403;
internal-service200. `/metrics` usa o mesmo servidor e Bearer das rotas financeiras.
METRICS_ENABLED=false remove a rota (404), instrumentação e loop de coleta. Não
existe modo público. OIDC desabilitado não libera acesso. Fora de development/test,
OIDC exige issuer HTTPS e audience não vazia. Contrato em [OpenAPI](openapi.yaml).

## Catálogo

Todos os nomes abaixo começam com `wagering_`. Counters/histograms com labels só
aparecem após a primeira observação da série. Labels têm whitelist finita; IDs
financeiros e providerId nunca entram em labels. Go/process collectors oficiais
também são expostos pelo registry privado de cada instância.

| Métrica | Labels | Semântica |
| --- | --- | --- |
| http_requests_total | method, route, status | Requests concluídos, incluindo401/403/404/405 |
| http_request_duration_seconds | method, route | Histograma de duração, incluindo auth |
| http_in_flight | nenhuma | Requests HTTP ativos |
| financial_attempts_total | operation, outcome | Tentativas do core: new/replay/duplicate/external_duplicate/error |
| financial_processing_duration_seconds | operation | Duração da tentativa, incluindo waits SQL/replay/erro |
| financial_operations_total | operation, state | Transações novas confirmadas, classificadas pelo estado inicial confirmado |
| financial_movements_total | operation | Movimentos de saldo confirmados, incluindo OPENING/resolução pending |
| idempotency_events_total | outcome | replay, duplicate, external_duplicate, idempotency_conflict, external_conflict, inbox_hash_conflict |
| sqs_events_total | outcome | received, completed, duplicate, transient_failure, permanent_failure, delete_failure, poll_failure |
| sqs_processing_duration_seconds | nenhuma | Processamento de mensagem, sem DeleteMessage |
| sqs_in_flight | nenhuma | Chamadas Process ativas, sem incluir polling/ACK |
| outbox_events_total | outcome | published, publish_failure, retry_scheduled, lease_lost, mark_failure, store_failure |
| outbox_publication_duration_seconds | nenhuma | Renew + send + confirmação SQL da tentativa |
| outbox_pending | nenhuma | Eventos não publicados no banco compartilhado |
| outbox_retry_scheduled | nenhuma | Eventos não publicados com retry_count>0 e próxima tentativa futura |
| outbox_lag_seconds | nenhuma | Idade do evento não publicado mais antigo;0 se backlog vazio |
| pending_references | nenhuma | Referências persistidas sem completed_at |
| pending_events_total | outcome | retry/resolved/expired/rejected confirmados e error; sem polls vazios |
| pending_processing_duration_seconds | nenhuma | Tentativas de resolução, incluindo polls vazios |
| reconciliation_runs_total | result | CONSISTENT, DIVERGENT, ERROR; após autorização |
| reconciliation_divergences_total | code | Ocorrências detectadas por código, incluindo auditorias repetidas |
| reconciliation_duration_seconds | result | Duração completa da auditoria |
| dependency_failures_total | dependency, component | Falhas observadas em postgres/sqs e componente limitado |
| postgres_pool_connections/in_use/idle/max_connections | nenhuma | Gauges da instância pgxpool.Stat |
| postgres_pool_acquires_total/acquire_duration_seconds_total/acquire_canceled_total/acquire_empty_total | nenhuma | Counters acumulados pgxpool, sem queries SQL |
| dlq_depth_approximate | nenhuma | ApproximateNumberOfMessages + ApproximateNumberOfMessagesNotVisible |
| dlq_collection_enabled/success/timestamp_seconds | nenhuma | Coleta configurada, último sucesso e horário da última amostra válida |
| operational_collection_success/timestamp_seconds | nenhuma | Sucesso/freshness da última amostra PostgreSQL |
| ready | nenhuma | Última resposta do probe readiness;0 no shutdown |

Na tabela, os grupos pool/DLQ/operational abreviam nomes com prefixo comum: por
exemplo `wagering_dlq_collection_timestamp_seconds`. Durações são em segundos;
histogramas usam buckets0.005,0.025,0.1,0.5,1,2.5,5,10,30 e +Inf. Não se medem
amounts monetários; floats do formato Prometheus são somente estatísticas/tempo.

## Tentativas, efeitos e replays

Uma BET nova PROCESSED incrementa tentativa/new, operação/BET/PROCESSED e
movimento/BET. Replay incrementa tentativa/replay e idempotency/replay, sem nova
operação/movimento. BET REJECTED incrementa operação/BET/REJECTED, sem movimento.
LOSS PROCESSED é operação sem movimento. OPENING positivo é operação/movimento;
wallet criada com zero não gera observação financeira OPENING.

PENDING_REFERENCE nova é uma operação no estado inicial PENDING_REFERENCE, sem
movimento. Retry só incrementa pending/retry. Resolução incrementa pending/resolved
e o movimento da reversão; não incrementa operação nova nem altera o counter do
estado inicial. TTL/max attempts incrementam pending/expired, sem movimento.
Outros resultados terminais rejeitados pelo worker usam pending/rejected.
FAILED é label suportada quando aplicável; o core atual não cria operações FAILED.

As observações financeiras ficam no caso de uso compartilhado, depois do retorno
do commit. HTTP/SQS não contam o mesmo resultado novamente. Inbox duplicate e
external duplicate ficam separados. Erro de commit/rollback não vira sucesso.
O consumer só conta completed após processamento durável e DeleteMessage aceito.
O publisher só conta published após send aceito e published_at confirmado; mark
failure pode levar a redelivery do mesmo eventId, sem contador published antecipado.

Counters são por processo e reiniciam com a instância. Crash entre commit e
instrumentação pode perder observação: métricas não substituem ledger nem são
contabilidade durável. Gauges Outbox/pending/DLQ refletem o recurso compartilhado;
**não somar** esses gauges entre réplicas para obter backlog global.

Exemplos de consultas, caso o avaliador use um Prometheus externo configurado
com OAuth2 client_credentials de internal-service (renovação automática do token):

```promql
sum(rate(wagering_http_requests_total[5m])) by (route, status)
histogram_quantile(0.95, sum(rate(wagering_financial_processing_duration_seconds_bucket[5m])) by (le, operation))
sum(rate(wagering_financial_operations_total[5m])) by (operation, state)
sum(rate(wagering_idempotency_events_total[5m])) by (outcome)
max(wagering_outbox_lag_seconds)
max(wagering_dlq_depth_approximate)
sum(rate(wagering_reconciliation_runs_total{result="DIVERGENT"}[5m]))
```

## Freshness, logs, health e troubleshooting

Backlog SQL e DLQ são coletados fora do scrape, em loop limitado por timeout.
Intervalo default15s; mínimo1s e maior que timeout positivo (default2s). Amostra
falha mantém os valores anteriores e marca success=0. Timestamp mede último
sucesso;0 significa nenhum sample válido. DLQ só é consultada quando SQS está
habilitado; confira enabled=1 e success=1 antes de interpretar depth. Profundidade
é aproximada e pode variar com mensagens visíveis/em andamento. O redrive é feito
pelo SQS, e não há counter alegando que o consumer moveu a mensagem para DLQ.

Lag inclui eventos não publicados com lease ou retry futuro. Se lag aumenta,
verifique publication failures, scheduled retries, mark/store failures, backlog
e disponibilidade do SDK/banco. Sem erro de dependência e com backlog crescente,
confira OUTBOX_PUBLISHER_ENABLED e separação das filas. Gauge stale não comprova
backlog vazio. Uma falha do broker não impede scrape de dados cacheados.

Logs slog JSON preservam correlationId e IDs úteis somente nos logs, com
operationType/status/outcome/durationMs/retryCount. HTTP usa rota normalizada;
Inbox/Core, SQS, pending, publisher, reconciliation e lifecycle têm contexto
sanitizado. Não há payload financeiro completo, access token, Authorization,
secret, password, DSN ou claimToken. Erros crus são substituídos por classificação
fixa; erros de startup/configuração também foram sanitizados. Correlation IDs
HTTP devem ter até128 caracteres ASCII imprimíveis sem espaços; valores inválidos
ou headers repetidos recebem ID gerado, propagado na resposta e Outbox.

`/health/live` continua público e mede processo vivo. `/health/ready` exige todos
os hooks críticos inicializados e ping PostgreSQL; responde503 se pool/banco falha.
Discovery OIDC e validação de filas ocorrem no startup. Indisponibilidade temporária
de SQS usa retries/retention e métricas, sem derrubar liveness. Se readiness falha,
confira conectividade/schema/migrations, limites de pool e logs sanitizados.

Shutdown marca unready antes de parar workers. Collector é cancelado/joined;
polling consumer/publisher cessa e drena trabalho; pending cancela e aguarda
cleanup; HTTP faz Shutdown, força Close em timeout e aguarda handlers; pool fecha
por último. Cancelamento não confirma eventos/mensagens indevidamente. Cleanup
SQL limitado pode ultrapassar a janela de grace configurada, para preservar a
ordem de fechamento. Ctrl+C encerra pelo lifecycle Fx existente.

## Verificação

```powershell
$env:TEST_DATABASE_URL = 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
$env:TEST_SQS_ENDPOINT = 'http://localhost:4566'
$env:TEST_OIDC_ISSUER_URL = 'http://localhost:8081/realms/jungle-gaming'
$env:TEST_KEYCLOAK_HEALTH_URL = 'http://localhost:9090/health/ready'
go test ./... -count=1
go vet ./...
$env:CGO_ENABLED = '1'
go test -race ./...
go run github.com/getkin/kin-openapi/cmd/validate@v0.149.0 docs/openapi.yaml
docker compose --env-file .env.example config --quiet
docker compose --env-file .env.example up -d --build --wait --wait-timeout 240
docker compose --env-file .env.example exec -T postgres psql -U wagering -d wagering -c 'SELECT version,dirty FROM schema_migrations;'
```

Race no Windows requer gcc/CGO. Integrações usam PostgreSQL, Keycloak e SQS reais,
schemas e filas descartáveis. Nenhuma migration nova. Escopo encerrado na fase11:
sem auto-repair, tracing avançado ou auditoria final.
