# Fase 10: operação de wallet reconciliation

Reconciliation compara wallet, ledger completo e transações relacionadas, em
snapshot consistente, e retorna evidências financeiras. É read-only: não corrige
saldo, versão ou registros, não cria eventos e não exige Idempotency-Key.

## Ambiente e chamada autenticada

Use [PHASE8_OPERATIONS.md](PHASE8_OPERATIONS.md) para subir PostgreSQL, LocalStack
e Keycloak, aplicar migrations e iniciar o backend com OIDC. A fase 10 não
adiciona configuração, dependência runtime ou migration; a versão continua 5.

Em outro terminal PowerShell, obtenha token e crie uma wallet ou use uma existente:

```powershell
$phase10TokenUrl = 'http://localhost:8081/realms/jungle-gaming/protocol/openid-connect/token'
$phase10Token = (Invoke-RestMethod -Method Post -Uri $phase10TokenUrl -Body @{
  grant_type = 'client_credentials'
  client_id = 'internal-service'
  client_secret = 'local-dev-internal-service-secret'
}).access_token
$phase10Headers = @{ Authorization = "Bearer $phase10Token"; 'X-Correlation-ID' = 'reconciliation-example' }
$phase10Body = @{
  playerId = "phase10-example-$([Guid]::NewGuid().ToString('N'))"
  currency = 'BRL'
  initialBalance = @{ amount = '100.00'; currency = 'BRL' }
} | ConvertTo-Json
$phase10Wallet = Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/wallets' -Headers $phase10Headers -ContentType 'application/json' -Body $phase10Body
$phase10WalletId = $phase10Wallet.walletId
$phase10Report = Invoke-RestMethod -Method Post -Uri "http://localhost:8080/wallets/$phase10WalletId/reconciliation" -Headers $phase10Headers
$phase10Report | ConvertTo-Json -Depth 8
```

Os secrets acima pertencem ao ambiente local já provisionado. Tokens locais
expiram em 60s; obtenha outro token quando necessário. Não há body na chamada de
reconciliation. Consulte [OpenAPI](openapi.yaml) para o contrato completo.

```json
{
  "walletId": "id-gerado",
  "currency": "BRL",
  "status": "CONSISTENT",
  "walletBalance": { "amount": "100.00", "currency": "BRL" },
  "ledgerBalance": { "amount": "100.00", "currency": "BRL" },
  "walletVersion": 1,
  "expectedWalletVersion": 1,
  "ledgerEntriesChecked": 1,
  "transactionsChecked": 1,
  "divergences": [],
  "checkedAt": "2026-10-07T12:00:00Z"
}
```

## Interpretação

| HTTP / status | Significado |
| --- | --- |
| 200 / CONSISTENT | Todas as verificações financeiras passaram no snapshot |
| 200 / DIVERGENT | Auditoria completa encontrou inconsistências; consultar divergences |
| 401 | Token ausente/inválido |
| 403 | Identidade sem autorização internal; ambos os providers são proibidos |
| 404 | Wallet inexistente |
| 400 | walletId sintaticamente inválido |
| 503 | Dependência, leitura, cancelamento ou prazo falhou; sem relatório parcial |

Cada divergência contém code, expected, actual, message sanitizada e, quando
aplicável, transactionId/ledgerEntryId. Amount/chain usam centavos textuais;
saldo agregado usa decimal e versões usam inteiros textuais. Códigos completos e
regras estão em [ARCHITECTURE.md](ARCHITECTURE.md#phase-10-wallet-reconciliation).

Saldo é reconstruído desde zero com Money exato e proteção de overflow. A cadeia
before/after é verificada separadamente da soma; o último after e a soma são
comparados com wallet.balance. Currency do ledger é implícita na wallet e deve
corresponder à transaction.currency. A versão esperada é 1 + movimentos válidos
PROCESSED após criação, sem contar OPENING ou LOSS. OPENING positivo permanece
version 1; wallet criada com zero e primeiro WIN passa para version 2.

PENDING/PENDING_REFERENCE, REJECTED e FAILED não exigem ledger e não podem ter
movimento. REFUND/ROLLBACK reutilizam a validação de referência do processamento.
`ledgerBalance` pode ser negativo quando histórico está corrompido; é null se
amount/direction inválidos ou overflow impedirem reconstrução segura.
`expectedWalletVersion` é null quando semântica corrompida impede derivação segura.
Esses casos retornam DIVERGENT com evidências, sem inventar resultados.

O histórico é completo, sem o limite 50 do GET ledger. `transactionsChecked`
conta o conjunto deduplicado da wallet e de registros relacionados inspecionados.
Chamadas repetidas mantêm a conclusão se os dados não mudaram; checkedAt é UTC de
conclusão, e não a identidade do snapshot. Escritas posteriores podem atualizar
a wallet imediatamente. A auditoria usa REPEATABLE READ READ ONLY, sem lock de
linha/global; DDL concorrente pode aguardar locks normais de tabela. O orçamento
HTTP de 10s e memória proporcional ao histórico são preservados.

Logs de conclusão incluem correlationId, walletId, reconciliationStatus,
ledgerEntriesChecked, transactionsChecked, divergenceCount e durationMs, sem
histórico completo, tokens ou secrets. Falha técnica usa ERROR, sem conclusão
financeira. Não há persistência do relatório ou auto-repair.

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
docker compose --env-file .env.example ps
docker compose --env-file .env.example exec -T postgres psql -U wagering -d wagering -c 'SELECT version,dirty FROM schema_migrations;'
```

Race no Windows exige um compilador C no PATH. O validador é ferramenta externa
versionada e não altera go.mod. Testes usam schemas descartáveis e fixtures de
corrupção isolada, nunca desabilitam proteções nas tabelas financeiras do operador.
Não se deve tratar integrações SKIP sem as variáveis como verificação real.

Escopo encerrado na fase 10: auto-repair, tracing, métricas finais e auditoria
final não foram implementados.
