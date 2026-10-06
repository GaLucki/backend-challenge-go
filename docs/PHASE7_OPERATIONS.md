# Fase 7: operação do Transactional Outbox

Pré-requisitos: Docker Desktop/Compose, Go compatível com `go.mod` e o CLI `migrate` já utilizado
pelo projeto. Para `-race` no Windows também são necessários CGO e um compilador C compatível.

## Inicialização

```powershell
docker compose --env-file .env.example config --quiet
docker compose --env-file .env.example up --build -d --wait
migrate -path migrations -database 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable' up
docker compose --env-file .env.example ps
docker compose --env-file .env.example exec -T localstack awslocal sqs list-queues
```

São provisionadas automaticamente a FIFO de comandos `wager-transactions.fifo`, sua DLQ
`wager-transactions-dlq.fifo` e a FIFO de eventos `wager-events.fifo`. PostgreSQL mantém seu volume;
as filas do emulador são efêmeras. A aplicação Go/Fx continua executada no host.

Antes de iniciar o serviço, aplique a migration 000005; o entrypoint não executa migrations.
Para validar a reversibilidade especificamente da nova migration, com publishers parados:

```powershell
migrate -path migrations -database 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable' version
migrate -path migrations -database 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable' down 1
migrate -path migrations -database 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable' up
```

O primeiro comando deve indicar 5 antes de executar esse `down 1`. DOWN remove leases/sequência,
preservando eventos e dados financeiros; o banco deve terminar na versão 5 sem dirty state.

## Executar a aplicação

```powershell
Get-Content .env.example | ForEach-Object {
    $phase7Line = $_.Trim()
    if ($phase7Line -and -not $phase7Line.StartsWith('#')) {
        $phase7Parts = $phase7Line.Split('=', 2)
        [Environment]::SetEnvironmentVariable($phase7Parts[0], $phase7Parts[1], 'Process')
    }
}
go run ./cmd/api
```

O exemplo habilita consumer e publisher. `OUTBOX_PUBLISHER_ENABLED=false` desabilita somente
publicação; o core financeiro continua registrando eventos para recovery posterior. Ctrl+C/SIGTERM
aciona shutdown pelo Fx. `/health/live` e `/health/ready` preservam as verificações anteriores.

| Variável | Default | Uso |
| --- | --- | --- |
| OUTBOX_PUBLISHER_ENABLED | false, true no exemplo | Habilitar publicação |
| EVENT_QUEUE_NAME | wager-events.fifo | Destino FIFO de saída |
| EVENT_QUEUE_URL | vazio | URL explícita ou resolução por nome |
| OUTBOX_POLL_INTERVAL | 1s | Espera entre batches |
| OUTBOX_BATCH_SIZE | 10 | Até 100 heads independentes por batch |
| OUTBOX_BASE_RETRY_DELAY | 1s | Primeiro retry falho |
| OUTBOX_MAX_RETRY_DELAY | 1m | Cap do exponential backoff |
| OUTBOX_CLAIM_DURATION | 30s | Lease recuperável |
| OUTBOX_PUBLISH_TIMEOUT | 5s | Deadline de envio SDK |
| OUTBOX_STORE_TIMEOUT | 3s | Deadline de claim/renew/mark/retry |

O lease deve exceder send + mark, e atrasos de retry devem ter resolução mínima de 1µs.
Config e startup recusam destino igual à fila de comandos/DLQ. Credenciais locais são dummy;
em AWS, mantenha o endpoint vazio e use credenciais/roles apropriadas pelo SDK. Não é necessário
instalar biblioteca ou serviço adicional para publicação.

## Inspecionar backlog e eventos

```powershell
docker compose --env-file .env.example exec -T postgres psql -U wagering -d wagering -c "SELECT event_id,event_type,aggregate_id,retry_count,next_attempt_at,published_at,claimed_by,claim_until,publication_sequence FROM outbox_events ORDER BY publication_sequence;"
docker compose --env-file .env.example exec -T localstack awslocal sqs get-queue-attributes --queue-url http://localhost:4566/queue/us-east-1/000000000000/wager-events.fifo --attribute-names All
docker compose --env-file .env.example exec -T localstack awslocal sqs receive-message --queue-url http://localhost:4566/queue/us-east-1/000000000000/wager-events.fifo --wait-time-seconds 5 --visibility-timeout 10 --message-system-attribute-names All
```

`receive-message` inspeciona e oculta temporariamente a entrega; não apaga o evento. Não há consumer
de negócio downstream nesta fase. Para endpoints diferentes, resolva a URL com `get-queue-url`.

Retry é indefinido com backoff limitado. Uma publicação confirmada com mark SQL falho permanece
pendente até recovery do lease e pode ser reenviada. O eventId e o dedup ID permanecem iguais.
Um evento inválido bloqueia seu aggregate; diagnostique o payload persistido e a causa antes de
qualquer reparo administrativo. Não delete eventos nem preencha published_at sem confirmação SQS.
Um downstream precisa deduplicar por eventId, inclusive após a janela de dedup do FIFO.

## Testes reproduzíveis

```powershell
$env:TEST_DATABASE_URL = 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
$env:TEST_SQS_ENDPOINT = 'http://localhost:4566'
go test ./... -count=1
go vet ./...
$env:CGO_ENABLED = '1'
go test -race ./...
```

Para rodar apenas a nova integração:

```powershell
go test ./internal/adapter/postgres -run IntegrationOutbox -count=1 -v
go test ./internal/application -run 'IntegrationFxOutbox' -count=1 -v
```

Testes usam schemas e filas temporárias e os removem ao terminar. Não purgam filas permanentes.
Sem variáveis de teste, integrações correspondentes são explicitamente skipped. A suíte anterior
continua incluída no comando completo; não é necessário build tag.

O teste de volume cria 25 wallets e 25 BETs, produzindo 100 envelopes financeiros reais. Três
publishers com pools/clients próprios executam batches concorrentes; todos devem participar,
todos os 100 eventos devem chegar com IDs/payloads originais e published_at deve ficar preenchido.
FIFO é verificado por aggregate/sequence, sem prometer ordem entre aggregates diferentes.

Crashes são simulados na fronteira claim/send/mark. Dois envios reais com IDs iguais são observados
por middleware SDK; within-window dedup do FIFO pode entregar só uma cópia, sem transformar a
garantia de publicação em exactly-once. A suíte usa falha real de fila inexistente, trigger SQL de
mark failure, expiração de timestamps de lease/retry e pausa controlada de requests reais para
testar paralelismo/shutdown sem sleeps longos. Não há hard kill do processo ou validação em uma
conta AWS hospedada.
