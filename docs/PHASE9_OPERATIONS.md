# Fase 9: API financeira HTTP

Provisioning, tokens client_credentials, OIDC e variáveis de execução continuam
em [PHASE8_OPERATIONS.md](PHASE8_OPERATIONS.md). Contratos completos:
[openapi.yaml](openapi.yaml), seguindo [OpenAPI 3.0.3](https://spec.openapis.org/oas/v3.0.3.html).

## Preparar o ambiente

```powershell
docker compose --env-file .env.example up -d --build --wait --wait-timeout 240
migrate -path migrations -database 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable' up
$env:APP_ENV = 'development'
$env:DATABASE_URL = 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
$env:OIDC_ENABLED = 'true'
$env:OIDC_ISSUER_URL = 'http://localhost:8081/realms/jungle-gaming'
$env:OIDC_AUDIENCE = 'wagering-api'
go run ./cmd/api
```

Não há migration nova nesta fase: versão 5 continua correta. Valores SQS/publisher
da `.env.example` podem ser definidos no processo Go para ativar os fluxos das
fases 6–7; Compose não exporta essas variáveis para o host. A API persiste Outbox
mesmo quando o publisher está desabilitado.

## Fluxo reproduzível em outro terminal

Secrets abaixo são fixtures **somente locais**. Todos os tokens vêm de
client_credentials; não há password grant, login humano ou token próprio.

```powershell
$base = 'http://localhost:8080'
$tokenUrl = 'http://localhost:8081/realms/jungle-gaming/protocol/openid-connect/token'
$tokenA = (Invoke-RestMethod -Method Post -Uri $tokenUrl -Body @{
  grant_type='client_credentials'; client_id='provider-a'; client_secret='local-dev-provider-a-secret'
}).access_token
$tokenInternal = (Invoke-RestMethod -Method Post -Uri $tokenUrl -Body @{
  grant_type='client_credentials'; client_id='internal-service'; client_secret='local-dev-internal-service-secret'
}).access_token
$providerHeaders = @{Authorization="Bearer $tokenA"; 'Idempotency-Key'='demo-bet-key'; 'X-Correlation-ID'='demo-http'}
$internalHeaders = @{Authorization="Bearer $tokenInternal"; 'X-Correlation-ID'='demo-http'}
$playerId = 'demo-' + [guid]::NewGuid().ToString('N')

$wallet = Invoke-RestMethod -Method Post -Uri "$base/wallets" -Headers $internalHeaders -ContentType 'application/json' -Body (@{
  playerId=$playerId; currency='BRL'; initialBalance=@{amount='100.00'; currency='BRL'}
} | ConvertTo-Json -Depth 4)
$walletId = $wallet.walletId

$betBody = @{
  externalTransactionId='demo-bet-1'; playerId=$playerId; walletId=$walletId
  type='BET'; money=@{amount='20.00'; currency='BRL'}; roundId='demo-round'
} | ConvertTo-Json -Depth 4
$bet = Invoke-RestMethod -Method Post -Uri "$base/wagering/transactions" -Headers $providerHeaders -ContentType 'application/json' -Body $betBody

Invoke-RestMethod "$base/wagering/transactions/$($bet.transactionId)" -Headers @{Authorization="Bearer $tokenA"}
Invoke-RestMethod "$base/providers/provider-a/wagering/transactions/demo-bet-1" -Headers @{Authorization="Bearer $tokenA"}
Invoke-RestMethod "$base/wallets/$walletId" -Headers $internalHeaders
$page = Invoke-RestMethod "$base/wallets/$walletId/ledger?limit=1" -Headers $internalHeaders
if ($page.nextCursor) {
  Invoke-RestMethod "$base/wallets/$walletId/ledger?limit=1&cursor=$([uri]::EscapeDataString($page.nextCursor))" -Headers $internalHeaders
}

# Mesma key e body: 200, idempotentReplay=true, observedBalance=80.00.
Invoke-RestMethod -Method Post -Uri "$base/wagering/transactions" -Headers $providerHeaders -ContentType 'application/json' -Body $betBody
```

Tokens locais expiram em 60 segundos; obtenha outro quando necessário. Para
repetir o fluxo com outro player, use também outro externalTransactionId e outra
Idempotency-Key, pois estas identidades são persistentes por provider.

## Endpoints e permissões

Todos os endpoints financeiros requerem Bearer OIDC. Sem token/inválido: 401.

| Endpoint | Autorização |
| --- | --- |
| `POST /wallets` | Internal-only |
| `GET /wallets/{walletId}` | Internal-only |
| `GET /wallets/{walletId}/ledger` | Internal-only |
| `POST /wagering/transactions` | Provider; principal determina providerId |
| `GET /wagering/transactions/{transactionId}` | Own provider ou internal; OPENING internal-only |
| `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | Own provider ou internal |
| `POST /wallets/{walletId}/reconciliation` | Internal-only; auditoria **200** CONSISTENT/DIVERGENT desde a fase 10 |
| `GET /health/live`, `GET /health/ready` | Públicos |

Os probes `/auth/*` da fase 8 permanecem disponíveis. Wallet é identificada por
player/currency; não há associação persistida que prove acesso do provider à
carteira inteira. A existência de uma aposta não cria essa autorização. A política
restritiva internal-only vale também para ledger, inclusive quando o provider
possui transações nessa carteira. Providers continuam escrevendo transações no
core conforme player/wallet/currency, sem criação de ownership novo.

## Contratos e validação

`POST /wallets`: `{playerId,currency,initialBalance?}`. InitialBalance omitido ou
null significa 0.00 na moeda informada. Se positivo, o caso de uso existente
cria OPENING, ledger e Outbox na mesma transação; zero cria somente a wallet.
Moedas do saldo e da carteira devem corresponder. Resposta inclui walletId,
playerId, balance, walletVersion e openingTransactionId quando criado.

`POST /wagering/transactions`: externalTransactionId, playerId, walletId, type,
money, roundId, referenceExternalTransactionId para REFUND/ROLLBACK. ProviderId
é opcional; mismatch retorna 403 antes de acessar o core/idempotência. OPENING
externo é 400. Sem Idempotency-Key é 400; não existe key gerada automaticamente.
Header deve ter exatamente um valor não vazio, até 256 bytes, sem espaços,
controles ou `/`, `\`, `?`, `#`, conforme contrato de identificadores.

Money usa exclusivamente `{"amount":"25.00","currency":"BRL"}`. Exatamente
duas casas decimais, moeda com três letras maiúsculas, sem número JSON, sinal,
expoente, NaN/Infinity ou overflow int64 cents. O adapter valida formato e delega
conversão exata a Money. LOSS/positividade/referências/reversões continuam no
caso de uso; handler não calcula efeitos ou resolve pendências.

Content-Type obrigatório `application/json`, charset UTF-8 opcional. Campos
desconhecidos, JSON inválido, vazio, trailing garbage/múltiplos valores são 400.
Body máximo 64 KiB (413); tipo incompatível é 415. IDs são texto real do projeto,
não UUID: 1–256 bytes UTF-8, sem whitespace, controles ou separadores `/\?#`.
Os GETs não exigem Content-Type. Método não suportado retorna 405 com Allow.

## Resultado, replay e erros

| Situação | Status / body |
| --- | --- |
| Wallet criada / nova transação PROCESSED | 201, resultado; Location aponta ao recurso |
| Replay PROCESSED | 200, resultado salvo + idempotentReplay=true |
| PENDING_REFERENCE novo ou replay ainda pendente | 202, resultado persistido |
| Rejeição de negócio REJECTED/FAILED, inclusive replay | 422, resultado persistido + failureCode |
| Mesma key com payload diferente / external ID duplicado / wallet já existente | 409, erro estável |
| Request inválido / key ausente / limite/cursor inválido | 400, erro estável |
| Sem token ou token inválido | 401 + WWW-Authenticate: Bearer |
| Sem permissão / provider mismatch / transação foreign existente | 403 |
| Recurso ausente | 404 |
| Body grande / Content-Type errado | 413 / 415 |
| Dependência ou contexto temporariamente indisponível | 503 |
| Falha não classificada | 500, sem detalhes internos |
| Reconciliation autenticada como internal | 200 CONSISTENT/DIVERGENT; 404 para wallet inexistente |

Rejeição é resultado de negócio com transactionId/state/failureCode, não uma
falha que apaga o registro. Mesma key e payload preservam o saldo/version
originalmente observados, mesmo após WIN ou outros movimentos. Referências
pendentes mantêm o fluxo da fase 5: quando o worker resolve, seu resultado salvo
é atualizado atomicamente para o estado terminal. Não se reconstrói replay a
partir do saldo atual.

GET transaction retorna identidade, player/wallet, tipo/estado, money, round,
referência/failureCode quando presentes e createdAt/updatedAt UTC. `result`
opcional contém o snapshot HTTP salvo: observado original e versão. GET utiliza
uma consulta SQL única para a transação+resultado, evitando misturar estados
antes/depois de resolução pendente. Transações somente SQS/OPENING podem não
possuir registro HTTP e omitir `result`, sem inventar observedBalance atual.

Erro técnico segue o envelope existente, acrescido de correlationId:

```json
{"error":{"code":"IDEMPOTENCY_CONFLICT","message":"idempotency key was used with a different payload"},"correlationId":"demo-http"}
```

Não há SQL, stack trace, tokens, secrets ou payload completo em respostas/logs.
Correlation usa o middleware existente: recebe/gera X-Correlation-ID, retorna
header, propaga ao caso de uso/Outbox e aparece no erro e logs JSON. Logs incluem
method, route template, status, durationMs e IDs/replay disponíveis. A rota é
template, sem despejar parâmetros/query arbitrários. Idempotency-Key não é logada.

## Ledger paginado

Default `limit=50`, mínimo 1, máximo 100. Parâmetros cursor/limit duplicados,
limites inválidos, cursor vazio/malformado e query malformada/desconhecida são 400.
Ordering crescente **(walletVersion, ledgerEntryId)**, collation SQL C para ID.
Versão da carteira expressa a ordem financeira sob lock, sem depender da ordem
dos relógios dos processos. O ID desempata deterministamente.

Cursor opaco versionado URL-safe vincula carteira, versão e ID da última entrada
retornada. Não é permissão e não contém segredo; é uma posição keyset. Ele não
deve ser decodificado/construído pelo cliente. Busca limit+1 para informar se
há próxima página. `nextCursor` ausente significa fim; `items:[]` para ledger
vazio. Não há offset nem snapshot congelado: movimentos acrescentados podem
aparecer em páginas seguintes, sem repetir entradas anteriores. Imutabilidade
append-only permanece no banco; não existem endpoints UPDATE/DELETE.

## Timeouts e escopo

ReadHeaderTimeout 5s, ReadTimeout 10s, WriteTimeout 15s, IdleTimeout 60s,
MaxHeaderBytes 32 KiB e orçamento de contexto por request 10s. Shutdown gracioso
Fx existente preservado. X-Content-Type-Options: nosniff em todas as respostas;
não há CORS wildcard.

Desde a fase 10, reconciliation executa auditoria financeira read-only em
snapshot consistente. Não altera saldo/versão nem produz eventos. O antigo 501
foi substituído por 200 CONSISTENT/DIVERGENT, 404 ou erro técnico 503.
Veja [PHASE10_OPERATIONS.md](PHASE10_OPERATIONS.md) para contrato e exemplos.

## Verificação

Com dependências healthy:

```powershell
$env:TEST_DATABASE_URL = 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
$env:TEST_SQS_ENDPOINT = 'http://localhost:4566'
$env:TEST_OIDC_ISSUER_URL = 'http://localhost:8081/realms/jungle-gaming'
$env:TEST_KEYCLOAK_HEALTH_URL = 'http://localhost:9090/health/ready'
go test ./... -count=1
go vet ./...
go test -race ./...
go run github.com/getkin/kin-openapi/cmd/validate@v0.149.0 docs/openapi.yaml
```

Validador OpenAPI é ferramenta externa opcional; não adiciona dependências ao
go.mod da aplicação. Race no Windows exige CGO e um compilador C no PATH.
Testes usam schemas e filas descartáveis e preservam backlog/filas do operador.
Integração exercita listener HTTP/Fx, tokens reais, restart, ledger, três instâncias
e final HTTP ↔ SQS com SDK/LocalStack real. As regressões das fases anteriores
também continuam na suíte completa.
