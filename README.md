# Jungle Gaming — processamento financeiro distribuído

Backend Go com API HTTP autenticada, SQS FIFO, PostgreSQL, ledger append-only,
idempotência persistente, Inbox/Outbox e reconciliação read-only. HTTP e o envelope
SQS original compartilham o núcleo financeiro; Money usa centavos int64.

A especificação original foi preservada integralmente em [CHALLENGE_SPEC.md](docs/CHALLENGE_SPEC.md).
Decisões: [ARCHITECTURE.md](docs/ARCHITECTURE.md). Contrato: [OpenAPI](docs/openapi.yaml).
Entrega: [FINAL_AUDIT.md](docs/FINAL_AUDIT.md) e [FINAL_DELIVERY_REPORT.md](docs/FINAL_DELIVERY_REPORT.md).

## Arquitetura e tecnologias

`net/http → OIDC → autorização → casos de uso → pgx / Unit of Work`.
SQS acrescenta Inbox na mesma transação. Publisher publica somente eventos confirmados,
em fila de saída separada. Uber Fx compõe configuração, pool, handlers e workers e
controla startup, drenagem e shutdown. Domínio não depende desses frameworks.

Go **1.27.0** em go.mod/Dockerfile, Go Modules, PostgreSQL 16, AWS SDK Go v2,
LocalStack 4.6.0, Keycloak 26.7.5, Prometheus client e slog JSON.

## Pré-requisitos

Git, Go 1.27.0, Docker com containers Linux e Docker Compose v2 recente.
O runner de race traz GCC/CGO: não exige instalar GCC no Windows.
Use Linux ou Docker Desktop com conectividade de host habilitada para o runner
(`network_mode: host`); o issuer local é `http://localhost:8081`.
A auditoria validou a saída do runner para as três dependências.
No Windows, execute a API no host: a publicação de sua porta pelo host networking
não funcionou neste Desktop, embora a imagem respondesse dentro da rede Docker.

Instale migrations CLI e coloque o diretório bin do GOPATH no PATH:

```sh
go install -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3
```

## Inicialização

Copie `.env.example` para `.env` (`Copy-Item` no PowerShell; `cp` no Linux).
Os valores e secrets fornecidos são fixtures locais. Compose não exporta essas
variáveis para um processo Go iniciado no host.

```sh
docker compose config --quiet
docker compose up -d --build --wait --wait-timeout 240
migrate -path migrations -database "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable" up
migrate -path migrations -database "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable" version
```

Esperado: PostgreSQL, LocalStack e Keycloak healthy; migration **6**, sem dirty.
LocalStack provisiona `wager-transactions.fifo`, `wager-transactions-dlq.fifo` e
`wager-events.fifo`. Keycloak importa realm e três identidades automaticamente.

PowerShell, usando o formato simples e sem aspas da `.env.example`:

```powershell
Get-Content .env | ForEach-Object {
  if ($_ -match '^([^#=\s]+)=(.*)$') {
    [Environment]::SetEnvironmentVariable($matches[1], $matches[2], 'Process')
  }
}
go run ./cmd/api
```

Linux:

```sh
set -a
. ./.env
set +a
go run ./cmd/api
```

API `localhost:8080`; Keycloak `localhost:8081`; management health `localhost:9090`;
LocalStack `localhost:4566`. O perfil `app` constrói a imagem final sem root,
para a rede host Linux: `docker compose --profile app up -d --build api`.
Não inicie as duas APIs no mesmo endereço. Ctrl+C encerra o processo Go;
`docker compose --profile app stop api` encerra o container.
`docker compose down` preserva o volume; `down -v` apaga dados e não é necessário.

## Migrations

Migrations 1–5 estão preservadas; a 6 acrescenta game_id, unicidade de OPENING
positivo e proteção de terminais. Registros históricos/hashes não são reescritos.
Banco existente recebe somente `migrate ... up`.

Reversão: `migrate -path migrations -database "$DISPOSABLE_DATABASE_URL" down 1`.
Use exclusivamente banco/schema descartável; nunca reverta dados financeiros no
banco principal. `TestIntegrationFinalMigrationsEntireChainUpDownUp` executa DOWN
completo e novo UP de 1–6 em schema aleatório e verifica as proteções.

## Tokens e API HTTP

Em outro terminal PowerShell, com API iniciada:

```powershell
$base = 'http://localhost:8080'
$tokenUrl = 'http://localhost:8081/realms/jungle-gaming/protocol/openid-connect/token'
$tokens = @{}
foreach ($client in @('provider-a','provider-b','internal-service')) {
  $tokens[$client] = (Invoke-RestMethod -Method Post -Uri $tokenUrl -Body @{
    grant_type='client_credentials'; client_id=$client
    client_secret="local-dev-$client-secret"
  }).access_token
}
$internal = @{Authorization="Bearer $($tokens['internal-service'])"}
$external = 'bet-' + [guid]::NewGuid().ToString('N')
$provider = @{Authorization="Bearer $($tokens['provider-a'])"; 'Idempotency-Key'=$external}
$player = 'demo-' + [guid]::NewGuid().ToString('N')
$wallet = Invoke-RestMethod -Method Post -Uri "$base/wallets" -Headers $internal -ContentType 'application/json' -Body (@{
  playerId=$player; initialBalance=@{amount='100.00'; currency='BRL'}
} | ConvertTo-Json -Depth 4)
$body = @{
  externalTransactionId=$external; playerId=$player; walletId=$wallet.id
  roundId='round-demo'; gameId='fortune-chimp'; kind='BET'
  money=@{amount='25.00'; currency='BRL'}
} | ConvertTo-Json -Depth 4
$bet = Invoke-RestMethod -Method Post -Uri "$base/wagering/transactions" -Headers $provider -ContentType 'application/json' -Body $body
Invoke-RestMethod -Method Post -Uri "$base/wagering/transactions" -Headers $provider -ContentType 'application/json' -Body $body
Invoke-RestMethod "$base/wagering/transactions/$($bet.transactionId)" -Headers $provider
Invoke-RestMethod "$base/providers/provider-a/wagering/transactions/$external" -Headers $provider
Invoke-RestMethod "$base/wallets/$($wallet.id)" -Headers $internal
Invoke-RestMethod "$base/wallets/$($wallet.id)/ledger?limit=50" -Headers $internal
Invoke-RestMethod -Method Post "$base/wallets/$($wallet.id)/reconciliation" -Headers $internal
(Invoke-WebRequest -UseBasicParsing "$base/metrics" -Headers $internal).Content
```

Não imprima/persista tokens. ProviderId deriva do token; se enviado, precisa coincidir.
Providers acessam somente suas transações. Wallet, ledger, reconciliation e metrics
são internal-only. Health é público. OIDC desabilitado não libera endpoints financeiros.

| Resultado | HTTP |
| --- | --- |
| Nova wallet / operação PROCESSED | 201 |
| Replay PROCESSED / leitura / reconciliation, inclusive DIVERGENT | 200 |
| PENDING_REFERENCE, inclusive replay pendente | 202 |
| REJECTED / FAILED persistido, com failureCode | 422 |
| Entrada inválida / conflito de key ou ID externo | 400 / 409 |
| Sem token / sem permissão / recurso ausente | 401 / 403 / 404 |
| Body acima de 64 KiB / media type incorreto | 413 / 415 |
| Dependência indisponível | 503 |

Erros técnicos usam `{error:{code,message},correlationId}`. X-Correlation-ID é propagado
ou gerado. Cursor do ledger é opaco, estável e vinculado à wallet; limite 1–100.
Respostas incluem campos originais id/version, status/balance e detalhes das fases anteriores.

## Garantias financeiras e concorrência

Money suporta BRL/USD/EUR, códigos ISO de duas casas. Contratos atuais exigem decimal
string com duas casas, sem float, expoente, NaN/Infinity, negativos ou arredondamento.
Limite positivo `92233720368547758.07`; diferenças internas podem ser negativas.

OPENING positivo confirma wallet, transação, ledger e dois eventos juntos, com versão 1.
Abertura zero não cria movimento. BET debita; WIN credita e pode referenciar BET processada
na mesma rodada. LOSS exige zero e não altera saldo/versão. REFUND/ROLLBACK são positivos,
integrais e provider-scoped, com escopo e referência validados. A mesma BET não recebe
reembolso duplo combinando REFUND/ROLLBACK. Reversão sem saldo usa failureCode próprio.

Locks por wallet, atualização condicionada à versão e constraints PG preservam os efeitos,
sem mutex global. Testes incluem três processos independentes, 50 tentativas idênticas e
duas BETs de 80 sobre 100: uma PROCESSED, uma REJECTED, saldo 20, versão 2 e um DEBIT.
Wallets independentes avançam em paralelo.

Referência ausente em reversão gera PENDING_REFERENCE durável. Worker usa SKIP LOCKED,
backoff exponencial, tentativas/TTL, retoma após restart e atualiza o resultado persistido.
Falhas transitórias revertem o UoW. Não há commit intermediário PENDING no fluxo síncrono.
Terminais não recebem novas transições.

Hash financeiro SHA-256 v3 usa JSON com chaves ordenadas e campos de negócio, incluindo
jogo/referência, com Money em centavos. Key/correlação/transporte ficam fora do hash.
Namespace por provider e observedBalance persistido garantem replay original.
Hashes v1/v2 e payloads legados das fases 4–7 são preservados para recovery.

## SQS, Inbox e Outbox

Envie o envelope WagerTransactionRequested da especificação original: messageId,
occurredAt e data com providerId, externalTransactionId, idempotencyKey, playerId,
walletId, roundId, gameId, kind e money. data.idempotencyKey corresponde à key HTTP.
BuildSendInput normaliza para o formato interno v1; mensagens originais diretas também funcionam.

MessageGroupId: `wallet:` + SHA-256 hexadecimal de walletId UTF-8.
MessageDeduplicationId: `delivery:` + SHA-256 hexadecimal de messageId UTF-8.
Consumer valida grupo/payload e usa identidade do envelope na Inbox. Commit de
Inbox/domínio/ledger/Outbox precede DeleteMessage; rejeição confirmada permite ACK.
Transitórios alteram visibility com backoff, sem ACK; permanentes chegam à DLQ por redrive.
Default local: polling 20s, visibility 90s, maxReceiveCount 5, quatro workers, processamento 5s.

Publisher reivindica heads por aggregate com lease persistente/fencing e SKIP LOCKED;
libera locks antes do I/O e marca publicação após envio. EventId/dedup são estáveis em retry.
Ordenação vale por aggregate; a fila de eventos é separada da entrada. Testes cobrem 100
eventos, três publishers, retries e recovery. Entrega/publicação são at-least-once:
efeito financeiro único depende de PG/idempotência; downstream deduplica eventId persistentemente.

O broker local usa dummy credentials e não prova enforcement IAM de produção.
Somente produtores internos confiáveis devem publicar: providerId no body não autentica
provider. Permissões mínimas: [arquitetura fase 12](docs/ARCHITECTURE.md#phase-12-final-audit-and-delivery).

## Reconciliation e observabilidade

Reconciliation usa snapshot REPEATABLE READ READ ONLY do histórico completo,
sem limite de 100 entradas. Compara saldo, chain, transações/referências e versão.
Retorna storedBalance, calculatedBalance, difference, consistent, checkedEntries e
divergências detalhadas. Não modifica saldo/ledger/Inbox/Outbox e não faz auto-repair.
Reconstrução/diferença fora do int64 retorna null com evidência de divergência.

Metrics exige Bearer internal-service: HTTP, financeiro, Inbox/SQS, Outbox, pendências,
reconciliation, pool/runtime. Labels limitadas, sem IDs financeiros. Counters são locais;
gauges compartilhados têm freshness e não devem ser somados entre réplicas.
Logs da aplicação são JSON, correlacionados e sanitizados; diagnósticos Fx ficam em stderr.
Sem tokens/secrets/payloads financeiros completos. Live não consulta dependências;
ready verifica startup, PG e filas SQS habilitadas, com timeout/cache broker de 1s.
Shutdown cancela polling, drena trabalho e fecha o pool por último.

## Testes completos e race reproduzível

No host, habilite todas as integrações. Sem TEST_* os testes reais fazem skip explícito;
isso não comprova a entrega.

```powershell
$env:TEST_DATABASE_URL='postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
$env:TEST_SQS_ENDPOINT='http://localhost:4566'
$env:TEST_OIDC_ISSUER_URL='http://localhost:8081/realms/jungle-gaming'
$env:TEST_KEYCLOAK_HEALTH_URL='http://localhost:9090/health/ready'
gofmt -l cmd internal
go test ./... -count=1
go vet ./...
```

Linux via runner versionado; quatro TEST_* e CGO já configurados:

```sh
docker compose -f docker-compose.yml -f docker-compose.audit.yml --profile audit build audit
docker compose -f docker-compose.yml -f docker-compose.audit.yml --profile audit run --rm audit go version
docker compose -f docker-compose.yml -f docker-compose.audit.yml --profile audit run --rm audit gcc --version
docker compose -f docker-compose.yml -f docker-compose.audit.yml --profile audit run --rm audit go test ./... -count=1
docker compose -f docker-compose.yml -f docker-compose.audit.yml --profile audit run --rm audit go test -race ./... -count=1
docker compose -f docker-compose.yml -f docker-compose.audit.yml --profile audit run --rm audit go vet ./...
go run github.com/getkin/kin-openapi/cmd/validate@v0.149.0 docs/openapi.yaml
```

Rebuild após mudanças no código. Schemas/filas de teste são aleatórios, descartáveis e
removidos no cleanup. O helper de subprocesso aparece como skip na invocação principal
mas executa nos filhos; nenhum teste real deve pular por falta de TEST_*.
Sem build tags para esconder integração.

Foco: `go test ./internal/adapter/postgres -run 'TestIntegrationThree|TestIntegrationConsumerAbrupt|TestIntegrationFinalMigrations' -count=1 -v`.
Abrupt termination usa Kill real antes/após commit. Outbox cobre crash boundaries por
injeção determinística de falhas e expiração de lease. Operação anterior: docs/PHASE*_OPERATIONS.md.

Smoke completo da API no host, com fixtures locais e uma wallet exclusiva:
`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/final-smoke.ps1`.
Esse comando aplica a política somente ao processo, sem alterar a configuração permanente.
Valida três tokens, autorização, HTTP/SQS replay, Inbox, quatro eventos publicados,
ledger, reconciliation e metrics. Preserva o histórico criado; resultados ficam em
`artifacts/`, ignorado pelo Git e pelo build Docker. O script não imprime tokens.

## Limitações conhecidas

Compose é desenvolvimento: H2 Keycloak, secrets dummy e PG administrativo para fixtures.
Não comprova IAM AWS, TLS de produção, cluster ou testes de carga. Administrador SQL pode
desabilitar triggers; UPDATE arbitrário fora do UoW não tem constraint diferida que obrigue
ledger/Outbox. Reconciliation detecta divergências. Produção exige app role restrita,
migration role separada, TLS e política IAM efetivamente verificada.

Protocolo legado pode omitir jogo/key SQS; metadados históricos não são reconstruídos.
Não há exactly-once publication, tracing, frontend, partidas dobradas ou auto-repair.
As ressalvas e a classificação final constam da auditoria. Relatórios históricos 0–11
não são evidência de execução desta fase; resultados novos estão nos relatórios finais.
