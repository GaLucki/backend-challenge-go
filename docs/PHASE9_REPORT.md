# Relatório da fase 9

API financeira HTTP concluída em 2026-10-07, reutilizando os casos de uso e a
autorização das fases 0–8. Operação e exemplos: [PHASE9_OPERATIONS.md](PHASE9_OPERATIONS.md).
Contrato: [openapi.yaml](openapi.yaml). Decisões: [ARCHITECTURE.md](ARCHITECTURE.md#phase-9-financial-http-contracts-and-read-queries).

## 1. Arquivos criados

- `internal/adapter/http/financial.go`
- `internal/adapter/http/wallets.go`
- `internal/adapter/http/transactions.go`
- `internal/adapter/http/financial_test.go`
- `internal/adapter/postgres/financial_reads.go`
- `internal/ports/financial_reads.go`
- `internal/application/financial/reads.go`
- `internal/application/financial/reads_test.go`
- `internal/application/http_financial_integration_test.go`
- `docs/openapi.yaml`
- `docs/PHASE9_OPERATIONS.md`
- `docs/PHASE9_REPORT.md`

## 2. Arquivos alterados

- `internal/adapter/http/server.go`: rotas, timeouts, status logging e metadata.
- `internal/adapter/http/auth.go`: identidade validada enriquece logs da request.
- `internal/adapter/http/errors.go`: correlationId no envelope existente.
- `internal/application/app.go`: Fx injeta queries/read service e handlers.
- `internal/identity/principal.go`: helper central de permissão para leitura de transação.
- `README.md`: acesso à API, OpenAPI, operação e relatório da fase 9.
- `docs/ARCHITECTURE.md`: contratos, query snapshots, paginação e decisões.

Sem alteração de go.mod/go.sum, Docker Compose, `.env.example`, schema/migrations
ou core financeiro. Nenhum teste/worker anterior removido ou enfraquecido.

## 3–5. Endpoints, autenticação e autorização

| Endpoint | Autenticação | Autorização / resultado |
| --- | --- | --- |
| POST /wallets | Bearer OIDC | Internal-only; 201 |
| GET /wallets/{walletId} | Bearer OIDC | Internal-only; 200 ou 404 |
| GET /wallets/{walletId}/ledger | Bearer OIDC | Internal-only; 200 com keyset pagination |
| POST /wagering/transactions | Bearer OIDC | Provider próprio; internal não pode impersonar |
| GET /wagering/transactions/{transactionId} | Bearer OIDC | Own provider ou internal; foreign 403; ausente 404 |
| GET /providers/{providerId}/wagering/transactions/{externalTransactionId} | Bearer OIDC | Own provider ou internal; path comparado com principal |
| POST /wallets/{walletId}/reconciliation | Bearer OIDC | Internal-only; 501 sem lógica financeira |
| GET /health/live | Público | Liveness do processo |
| GET /health/ready | Público | Inicialização + PostgreSQL, conforme fase 8 |

Os três probes `/auth/*` da fase 8 permanecem registrados. A autorização de
ownership também existe no caso de uso/query service; não depende do router.

## 6. POST /wallets

Body: `{playerId,currency,initialBalance?:{amount,currency}}`. Saldo inicial
omitido/null vira 0.00 na moeda da carteira. Valores positivos delegam ao
CreateWallet existente e criam OPENING, crédito ledger e Outbox atomicamente;
zero cria somente carteira. Provider comum recebe 403. Resposta 201 inclui
walletId/playerId/balance/walletVersion e openingTransactionId quando criado;
Location aponta para GET wallet. Unicidade player/currency existente gera 409.

## 7–8. POST /wagering/transactions e providerId

Body obrigatório: externalTransactionId, playerId, walletId, type, money e roundId;
referenceExternalTransactionId quando REFUND/ROLLBACK. Money é string decimal
com duas casas + moeda, sem float. Handler rejeita OPENING e apenas mapeia DTO.
LOSS, positividade, reversões, pendências, locks e eventos continuam no core.

ProviderId é opcional no body. O valor efetivo vem de `Principal.ProviderID`
validado por JWT; se fornecido, exige igualdade antes do comando/idempotência.
Não se cria associação provider-wallet nova. GET wallet e ledger permanecem
internal-only, pois dados persistidos não provam acesso direto de um provider
à carteira inteira. Provider pode consultar suas transações, nunca as de outro.

## 9–11. Idempotency-Key, replay e observed balance

Exatamente um header Idempotency-Key, obrigatório, não vazio, até 256 bytes e
conforme formato textual documentado. Não é gerado pelo servidor. O namespace
por provider da fase 8 e o hash/replay persistente das fases 4–5 são reutilizados.
Mesma key/payload retorna WagerResult salvo com idempotentReplay=true. Payload
válido diferente gera 409; JSON/DTO inválido falha no boundary. Outra key com
mesmo provider/external ID gera 409, sem repetir efeito.

Replay preserva observedBalance e walletVersion originais. Prova HTTP real:
BET retorna 80.00, WIN leva saldo a 130.00, replay do BET ainda retorna 80.00.
Restart de Fx/listener/pool não altera esse resultado. Pendências mantêm a
atualização terminal do snapshot pelo worker existente, no mesmo commit.

## 12–14. Consultas de transação e wallet

GET transaction por ID responde identidade, provider/external quando aplicáveis,
playerId/walletId, type/state, money, roundId, referência/failureCode, createdAt
e updatedAt UTC. `result` opcional é o resultado HTTP persistido, sem reconstrução
com saldo atual. Uma única consulta SQL lê transação + snapshot salvo para evitar
misturar estados durante resolução pendente. SQS/OPENING sem registro HTTP
podem omitir result. Ownership é verificado antes de devolver/decodificar dados.

Lookup externo compara o provider da URL com a identidade antes da query,
reutilizando o helper da fase 8. Internal pode consultar providers e OPENING.
GET wallet retorna walletId/playerId/currency/balance/version, com Money decimal.
Carteira inexistente é 404, e provider sem privilégio interno é 403.

## 15–16. Ledger e cursor

Ledger read-only paginado: default 50, mínimo 1, máximo 100. Items incluem
ledgerEntryId/walletId/transactionId/direction, Money de amount/balanceBefore/
balanceAfter, walletVersion e occurredAt UTC. Não há PUT/PATCH/DELETE financeiro.

Ordering crescente `(wallet_version, id COLLATE "C")`; keyset sem offset.
Cursor opaco, versionado, Base64 URL-safe canônico, com walletId + posição da
última entrada. Binding da wallet não substitui autorização. Busca limit+1;
nextCursor ausente significa fim e items [] indica ledger vazio. Não é snapshot
congelado: novos movimentos podem aparecer nas próximas páginas sem repetir
entradas já vistas. Cursor malformado, de outra wallet, vazio ou limite inválido
retorna 400. Queries ficam no adapter PostgreSQL, sem SQL no HTTP.

## 17–20. Envelope, status, pending e rejection

Erro técnico/contratual: `{error:{code,message},correlationId}`. Mantém envelope
anterior e códigos estáveis, sem SQL, stack trace, tokens, secrets ou payload.

| Situação | Status |
| --- | --- |
| Wallet criada / nova transação PROCESSED | 201 + Location |
| Replay PROCESSED | 200 |
| Novo/replay PENDING_REFERENCE | 202 com resultado persistido |
| Novo/replay REJECTED ou FAILED | 422 com resultado persistido e failureCode |
| Request/IDs/Money/cursor/limit/key inválidos | 400 |
| Token ausente/inválido | 401 + WWW-Authenticate: Bearer |
| Sem permissão / mismatch / transação foreign existente | 403 |
| Recurso ausente | 404 |
| Método não suportado | 405 + Allow |
| Wallet existente / key com outro payload / external ID duplicado | 409 |
| Body acima de 64 KiB / Content-Type incompatível | 413 / 415 |
| Dependência/contexto temporariamente indisponível | 503 |
| Falha não classificada | 500 |
| Reconciliation internal | 501 |

Rejeição insuficiente/inválida de negócio é commit financeiro com state e
failureCode, não erro de infraestrutura que apaga o registro. No teste real,
insufficient funds e replay preservam a mesma transação com 422. Missing reference
é 202; após o worker resolver, replay e GET exibem o resultado terminal.

## 21–24. OPENING, JSON, correlation e logging

OPENING externo é rejeitado no boundary, além da proteção existente do domínio.
Content-Type application/json obrigatório, charset UTF-8 opcional. Body limitado
a 64 KiB; campos desconhecidos, JSON inválido/vazio e trailing garbage/segundo
JSON são 400. Money não aceita número JSON, sinal, notação científica, negativos,
NaN/Infinity ou overflow; formato tem exatamente duas casas. IDs reais são texto,
sem impor UUID, com os limites/separadores documentados.

Correlation usa o middleware existente e é propagada para application/Outbox,
header, errors e logs. Integração consulta a Outbox real e comprova o mesmo ID.
Logs JSON incluem method, route template, status, durationMs, correlationId,
providerId/transactionId/walletId e idempotentReplay quando disponíveis. Consultas
internas registram provider do recurso quando há. Não registram Authorization,
token, secrets, Idempotency-Key, payload completo ou URL/query arbitrária.

## 25–27. Health, reconciliation e OpenAPI

Health público mantém liveness independente das dependências e readiness
conforme startup + PG. Timeouts: header 5s, read 10s, write 15s, idle 60s,
request context 10s; header máximo 32 KiB. Shutdown gracioso preservado, nosniff
nas respostas e nenhum CORS wildcard adicionado.

Reconciliation é apenas auth internal + validação sintática de walletId → 501.
Não consulta existência, não soma ledger, não reconcilia nem gera relatório/evento.
Comportamento funcional pertence à fase 10.

OpenAPI **3.0.3** documenta endpoints, Bearer, DTOs, Money, key, pagination,
results/errors/status e placeholder. Validada com kin-openapi **v0.149.0**,
inclusive schemas/referências. Validador é ferramenta externa opcional; nenhuma
dependência nova foi adicionada ao projeto, sem geração de código.

## 28–31. Testes HTTP, isolamento, replay e ledger

Unit tests: DTOs válidos/invalidos, role/internal/ownership, cinco tipos externos,
OPENING bloqueado, keys, status/replay/rejection/pending, lookup/404, content type,
limite de body, JSON desconhecido/trailing, identifiers, cursor/limits, envelope/
correlation, logs sanitizados, health e reconciliation 501. Read-service tests
provam cursor wallet-bound/posição e autorização antes de devolver snapshot.

`TestIntegrationFinalFinancialHTTP`: listener Fx real, PostgreSQL e três tokens
Keycloak reais via client_credentials. Exercita wallet positive/zero e unicidade,
wallet/ledger internal-only para ambos os providers, BET/WIN/LOSS/REFUND/ROLLBACK,
consultas ID/external, A↔B 403, body spoof, shared key/external separados por
provider, key conflict, external conflict, observed balance original, rejeição e
replay, pendência/resolução/replay, OPENING proibido, reconciliation protegida,
health público e restart sem perda de idempotência.

Ledger começa vazio/OPENING, é paginado enquanto uma entrada nova é anexada e
não repete IDs. Ao final do cenário: saldo **90.00**, soma ledger **90.00**, oito
movimentos e nove records HTTP persistidos. Rejeições, requests negados e replay
não acrescentam movimentos. GET usa snapshot salvo original e corrente do
worker conforme estado persistido, sem substituí-lo pelo saldo atual.

## 32–33. Cross-transport e concurrency regression

`TestIntegrationFinalHTTPCrossTransport`: SDK e LocalStack reais exercitam HTTP
primeiro → SQS duplicado, e SQS primeiro → HTTP 409. Final: saldo 60.00, três
transações (inclui OPENING), três entradas ledger, dois Inbox e seis Outbox.
Nenhuma duplicata reaplica financeiramente. Recebimentos SQS reais têm atributos
FIFO, e ACK ocorre após commit pelo consumer existente.

`TestIntegrationHTTPConcurrentThreeInstances`: três aplicações/pools/listeners
Fx independentes compartilham PostgreSQL. 50 requests com mesmo payload/key
produzem uma resposta nova + 49 replays, mesma transactionId, um DEBIT e saldo
80.00. Duas BET de 80.00 sobre outra wallet de 100.00 produzem uma PROCESSED,
uma REJECTED, um DEBIT e saldo 20.00. Testes anteriores de wallets independentes,
multi-instance, Inbox/Outbox/retries/DLQ/recovery continuam e foram executados.

## 34–41. Qualidade, Compose, serviços e migrations

| Verificação | Resultado |
| --- | --- |
| gofmt | Arquivos Go novos/alterados formatados |
| `go test ./... -count=1` com quatro variáveis TEST documentadas | **PASS** com PostgreSQL, LocalStack e Keycloak reais |
| `go vet ./...` | **PASS** |
| `CGO_ENABLED=1; go test -race ./...` | Não compilou: gcc ausente no PATH do Windows; detector não executado |
| `docker compose --env-file .env.example config --quiet` | **PASS** |
| `docker compose --env-file .env.example up -d --build --wait --wait-timeout 240` | **PASS** |
| PostgreSQL | **healthy**, migrations versão **5**, dirty **false** |
| LocalStack | **healthy**, as três filas FIFO anteriores preservadas |
| Keycloak 26.7.5 | **healthy**, realm/client credentials existentes funcionando |
| OpenAPI validator v0.149.0 | **PASS** |
| Migrations desta fase | Nenhuma; versões 1–5 intactas, sem necessidade de 000006 |

Schemas/filas dos testes são descartáveis e removidos no cleanup, sem usar o
backlog financeiro do operador. Não se confunde teste de integração pulado com
validação real: as quatro variáveis TEST estavam definidas na suíte completa.

## 42–43. Correções anteriores, decisões e limitações

Não foi necessário corrigir bug financeiro das fases anteriores. Core, canonical
hash, namespace provider, UoW, Inbox, Outbox, pending e SQS foram preservados.
A fase adiciona apenas queries/read service e adapter HTTP; sem refactor amplo.

- Wallet/ledger ficam internal-only pela ausência de ownership provider-wallet
  seguro no schema. Não se inventa relacionamento a partir de uma aposta.
- GET sem registro HTTP omite result; não cria saldo observado fictício.
- Cursor é posição opaca, não segredo/grant nem snapshot congelado entre requests.
- JSON contratualmente inválido é 400 antes da idempotência; payload válido
  alterado na mesma key é 409. Sem key gerada automaticamente.
- Reconciliation 501 não verifica existência nem realiza cálculo.
- Fixtures Keycloak/LocalStack continuam locais, com mesmas decisões da fase 8.
- Race detector permanece limitado pela ausência de compilador C no ambiente.

## 44. Escopo encerrado

**Reconciliation real, tracing, métricas finais e auditoria final NÃO foram
implementados.** Trabalho encerrado ao final da fase 9.
