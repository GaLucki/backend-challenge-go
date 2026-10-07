# Relatório da fase 8

Fase concluída em 2026-10-07: autenticação OAuth2/OIDC real, provisioning automático
do Keycloak e autorização de providers/internal nos casos de uso. As garantias e
testes financeiros, SQS, Inbox, Outbox/publisher e recovery das fases 0–7 foram
preservados. Execução: [PHASE8_OPERATIONS.md](PHASE8_OPERATIONS.md).
Decisões: [ARCHITECTURE.md](ARCHITECTURE.md#phase-8-oauth2oidc-and-authenticated-financial-access).

## 1. Arquivos criados

- `infra/keycloak/jungle-gaming-realm.json`
- `internal/identity/principal.go`
- `internal/identity/principal_test.go`
- `internal/adapter/oidc/verifier.go`
- `internal/adapter/oidc/verifier_test.go`
- `internal/adapter/oidc/keycloak_integration_test.go`
- `internal/adapter/http/auth.go`
- `internal/adapter/http/auth_test.go`
- `internal/application/financial/authorized.go`
- `internal/application/financial/authorized_test.go`
- `internal/application/auth_integration_test.go`
- `internal/config/oidc.go`
- `internal/config/oidc_test.go`
- `docs/PHASE8_OPERATIONS.md`
- `docs/PHASE8_REPORT.md`

## 2. Arquivos alterados

- `.env.example`: configuração OIDC e Keycloak local.
- `docker-compose.yml`: serviço Keycloak e health check.
- `go.mod` / `go.sum`: versões reproduzíveis das bibliotecas.
- `internal/config/config.go`: carregar e validar OIDC.
- `internal/application/app.go`: compor autenticação, autorização e facade via Fx.
- `internal/adapter/http/server.go`: registrar somente probes de autenticação.
- `internal/adapter/http/errors.go`: códigos estáveis UNAUTHORIZED/FORBIDDEN.
- `docs/ARCHITECTURE.md`: decisões, políticas e evidências desta fase.
- `README.md`: apontar operação e relatório da fase 8.

Nenhuma migration existente, core financeiro, worker ou teste anterior foi removido
ou enfraquecido. A fase não alterou as regras de processamento financeiro.

## 3. Dependências adicionadas

| Módulo | Versão | Uso |
| --- | --- | --- |
| `github.com/coreos/go-oidc/v3` | `v3.21.0` | Discovery, validação criptográfica e JWKS/cache |
| `github.com/go-jose/go-jose/v4` | `v4.1.4` | Dependência do verifier; testes assinam JWTs e servem JWKS |
| `golang.org/x/oauth2` | `v0.36.0` | Dependência OIDC; testes usam clientcredentials para tokens reais |

## 4–7. Compose, versão, realm e clients

Compose adiciona `quay.io/keycloak/keycloak:26.7.5`, `start-dev --import-realm`,
arquivo versionado montado read-only, H2 `dev-file` separado do banco financeiro,
limite de memória de 1 GiB e health check por socket bash na porta management.
Portas no host: loopback 8081/9090. PostgreSQL e LocalStack continuam preservados.

Realm: **jungle-gaming**. Clients service-to-service: **provider-a**, **provider-b**,
**internal-service**, cada um com service account própria e fluxo client_credentials.
Client de recurso: **wagering-api**, bearer-only, para a audience. Secrets são
fixtures exclusivamente locais `local-dev-...-secret`, claramente documentadas.
Não há segredo real nem necessidade de client secret no backend para validação.
Login humano, password grant e implicit flow estão desabilitados nos callers.

Versão verificada: [Keycloak 26.7.5](https://www.keycloak.org/2026/09/keycloak-2675-released).

## 8–10. Mapping, roles/scopes e internal-service

`provider_id` assinado, configurado por mapper fixo do IdP, determina o provider
autorizado: provider-a → provider-a; provider-b → provider-b. Nenhum valor de
body/path/query/header customizado substitui essa identidade. Quando o input
omite ProviderID, o caso de uso deriva do principal; mismatch é Forbidden.

Roles mínimas em `realm_access.roles`: **provider** e **internal**. Os callers
possuem role scope mappings restritos; não precisam de scopes extras. Internal
não recebe provider_id nem role provider. Pode criar/ler carteiras e consultar
transações de providers ou OPENING; não pode escrever operação externa fingindo
ser provider. Carteiras pertencem a jogadores, portanto leituras são internal-only.

## 11–14. JWT, issuer, audience e discovery/JWKS

Biblioteca madura [go-oidc](https://github.com/coreos/go-oidc), sem crypto JWT manual.
Valida RS256, assinatura, issuer exato, audience configurada, exp e nbf. O adapter
requer acesso `typ=Bearer`, sub/azp válidos, provider_id para role provider, exp
estritamente futuro e nbf estrito (sem os cinco minutos de tolerância da biblioteca).
Tokens ambíguos provider+internal, alg=none, chave desconhecida, assinatura errada,
issuer/audience errados, claims ausentes/inválidas ou token expirado falham.

Issuer local: `http://localhost:8081/realms/jungle-gaming`. Audience padrão e de
provisioning: `wagering-api`; somente valor explicitamente vazio desliga o check
de audience. Assinatura/issuer/exp continuam obrigatórios. Em ambientes diferentes
de development/test, a configuração exige issuer HTTPS.

Discovery e JWKS vêm do IdP real. Há um verifier/cache reutilizado por processo,
HTTP timeout e refresh para chave nova; não se copia chave para o código. Teste
unitário prova cache e rotação com JWKS local de teste. Integração usa discovery
e chaves do Keycloak real. Durante outage, chave conhecida em cache pode validar
token válido; chave desconhecida sem refresh retorna 401.

## 15–18. Principal, middleware e authorization

Principal tipado possui Subject, ClientID, ProviderID, Roles e Internal. O contexto
copia a lista de roles e nunca guarda o Bearer bruto. Authenticator é um contrato
independente de SDK. Parsing de claims fica centralizado no adapter OIDC.

Middleware exige um único Authorization Bearer bem formado, valida JWT e coloca
Principal no contexto. Authorizer separado fornece RequireProvider, RequireInternal
e AuthorizeProvider. Fx injeta os componentes, com discovery no OnStart antes do
listener HTTP. Não há globais. Domínio não importa autenticação/OIDC; o caso de
uso não importa SDK do Keycloak.

`financial.AuthorizedService` protege a fronteira financeira: consultas por ID e
external ID, criação de operação, replay e carteiras. As checks não dependem do
router. Chaves HTTP são persistidas no namespace provider-scoped
`oidc:v1:` + SHA-256(JSON([providerId autenticado, chave enviada])). A e B podem
usar a mesma chave sem compartilhar resultado. O core permanece para o canal
interno/broker-authenticated, e futuros handlers HTTP devem usar a facade.

## 19–20. HTTP 401 e 403

**401**: header ausente, malformado, múltiplo/comma-joined, Bearer vazio, token
inválido/expirado, falha de assinatura/claims/issuer/audience ou verifier desabilitado.
Usa `WWW-Authenticate: Bearer`, envelope existente e mensagem sanitizada.

**403**: principal válido sem role apropriada, provider mismatch, consulta a
transação de outro provider, provider tentando internal, interno tentando escrita
externa. Lookup de ID inexistente é 404; recurso existente foreign é 403, sem
retornar a linha/dados. Mismatch explícito é 403 antes de consultar storage. Essa
política aceita inferência de existência de ID conhecido e está documentada.

## 21–25. Testes e integração real

| Cenário | Resultado observado |
| --- | --- |
| Provider-a client_credentials | Token real, subject/client/mapping válidos; own lookup/replay permitido |
| Provider-b client_credentials | Identidade distinta, own lookup/replay permitido |
| A → B e B → A | 403 nas consultas por ID/external ID e nos probes |
| Spoof de providerId no body | 403 antes de processamento/replay, ambos os sentidos |
| Mesma chave e external ID por A e B | Duas transações/records separados; own replay sem novo efeito |
| Payload alterado no own replay | Conflito de idempotência preservado |
| Provider → internal/wallet/opening | 403 |
| Internal-service | Token real, role internal, internal probe e wallet reads permitidos; cross-provider reads permitidos |
| Sem token / inválido | 401 |
| Expiração de token real | 401, relógio injetado após exp real, JWT/JWKS intactos |
| Expected issuer / audience errados | Token real rejeitado |
| Discovery indisponível | Startup Fx falha antes do HTTP listener |
| JWKS cache/rotation/outage | Reuso, refresh e falha fechada comprovados em unit tests |

`TestIntegrationKeycloakTokensAndValidation` comprova IdP health, realm, três
client_credentials, identities e validações reais. `TestIntegrationOIDCProviderIsolation`
usa PostgreSQL real em schema descartável e aplicação Fx com tokens Keycloak
reais. O router financeiro existe somente no teste e realiza autenticação; a
autorização de ownership acontece no caso de uso. Ao final: saldo 9800 cents,
versão 3, três transações (inclui OPENING), três entradas ledger, seis eventos
Outbox e dois records de idempotência; chamadas negadas e replays não alteram
esses efeitos. Migrations 1–5 aplicadas no schema de teste e cleanup realizado.

## 26–32. Qualidade e ambiente

| Verificação | Resultado |
| --- | --- |
| `gofmt` | Arquivos Go criados/alterados formatados |
| `go test ./... -count=1` com quatro variáveis TEST documentadas | **PASS**, incluindo integrações reais das fases anteriores e fase 8 |
| `go vet ./...` | **PASS** |
| `CGO_ENABLED=1; go test -race ./...` | Não compilou: `gcc` ausente no PATH do Windows; detector não executado |
| `docker compose --env-file .env.example config --quiet` | **PASS** |
| `docker compose --env-file .env.example up -d --build` | **PASS**; imagem fixada obtida e ambiente iniciado |
| PostgreSQL | **healthy**; schema_migrations versão **5**, dirty **false** |
| LocalStack | **healthy**; filas `wager-transactions.fifo`, `wager-transactions-dlq.fifo`, `wager-events.fifo` presentes |
| Keycloak | **healthy**, realm importado automaticamente, readiness HTTP testado |

A primeira execução iniciou durante o bootstrap/reimport do Keycloak e falhou
por indisponibilidade, sem bypass. Após health, testes focados e suíte completa
passaram. O procedimento reproduzível documenta `up --wait` antes de testar.

## 33. Correções anteriores

Não foi necessário corrigir bug do core das fases anteriores. A chave global
de idempotência do core foi adaptada na nova facade autenticada ao cenário
multi-provider; o contrato interno/repositório/migrations foi preservado. A
regressão `TestAuthorizedIdempotencyIsScopedByProvider` e a integração real provam
separação/replays sem movimentação extra.

## 34. Decisões e limitações

- Dev Keycloak com H2/HTTP/fixtures locais; não é deployment de produção.
- Discovery habilitado deve funcionar no startup. Runtime ready verifica PG,
  não faz chamada ao IdP por probe; live não depende do IdP. Tokens conhecidos
  em cache continuam sendo validados criptograficamente durante outage.
- Recriação do container IdP perde H2/chaves; obtenha novos tokens e reinicie
  verifier/backend. Rotação normal usa refresh JWKS da biblioteca.
- Não há introspection/revogação instantânea por request; token local dura 60s.
- OIDC off não fornece acesso anônimo às rotas protegidas.
- API pública somente health e três probes `/auth/*`; futuras rotas financeiras
  devem usar o serviço autorizado. Rotas `/test/*` não existem no binário final.
- SQS mantém broker credentials/policies separados de OAuth. ProviderID em
  mensagem continua dado de negócio de um canal interno; nenhum Bearer na fila.
- Compilador C ausente impediu race detector; não há declaração de race PASS.

## 35. Limite de escopo

**Não foram implementados todos os endpoints HTTP finais, reconciliation,
tracing, métricas finais ou auditoria final.** Trabalho encerrado na fase 8.
