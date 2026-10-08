# Fase 8: operação de OAuth2/OIDC

## Subir as dependências

Pré-requisitos: Go na versão de `go.mod`, Docker Desktop com engine Linux e
`golang-migrate` para aplicar as migrations existentes. Execute da raiz:

```powershell
docker compose --env-file .env.example config --quiet
docker compose --env-file .env.example up -d --build --wait --wait-timeout 240
docker compose --env-file .env.example ps
migrate -path migrations -database 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable' up
```

O primeiro bootstrap do Keycloak pode levar cerca de um minuto. Aguarde os três
serviços ficarem **healthy** antes de iniciar o backend ou executar integração.
`docker compose up --build` também funciona quando as variáveis PostgreSQL estão
definidas em `.env`, como nas fases anteriores. Compose não carrega variáveis no
processo Go executado no host.

Keycloak **26.7.5**, realm `jungle-gaming`, HTTP em `http://localhost:8081`, readiness
em `http://localhost:9090/health/ready`. As duas portas Keycloak estão limitadas a
loopback. Keycloak usa H2 `dev-file` próprio, sem tabelas no PostgreSQL financeiro.
O realm é importado automaticamente de `infra/keycloak/jungle-gaming-realm.json`.
Não há dependência de configuração manual pelo painel.

Esse ambiente usa `start-dev`, HTTP e credenciais locais públicas exclusivamente
para o desafio. Produção exige configuração própria de TLS, armazenamento do IdP
e secrets. O backend exige issuer HTTPS fora de `development`/`test`. Recriar o
container Keycloak recria seu banco de desenvolvimento e suas chaves; reiniciar
mantém o banco no container. O import de startup ignora realms já existentes.
Para aplicar mudanças no JSON de desenvolvimento, recrie **somente Keycloak**:

```powershell
docker compose --env-file .env.example up -d --force-recreate --wait keycloak
```

PostgreSQL e suas migrations/volumes, LocalStack e suas três filas continuam com
as configurações anteriores. Os tokens previamente emitidos ficam inválidos
após recriar o IdP; reinicie também o backend para reinicializar discovery/JWKS.

## Identidades provisionadas

| Client ID | Secret exclusivamente local | `provider_id` | Role |
| --- | --- | --- | --- |
| `provider-a` | `local-dev-provider-a-secret` | `provider-a` | `provider` |
| `provider-b` | `local-dev-provider-b-secret` | `provider-b` | `provider` |
| `internal-service` | `local-dev-internal-service-secret` | ausente | `internal` |

`wagering-api` é um resource client bearer-only para a audience, sem credenciais
de chamada. Os três callers confidenciais possuem service accounts distintas,
somente `client_credentials`; login interativo, password grant e implicit flow
estão desabilitados. Não precisam solicitar scopes adicionais. As roles são
emitidas em `realm_access.roles`, e a audience é `wagering-api`. O `provider_id`
é um mapper fixo administrado pelo IdP, nunca um atributo enviado pelo caller.
Access tokens locais duram 60 segundos; obtenha outro token quando necessário.

## Executar o backend no host

As variáveis abaixo são suficientes para os probes desta fase. Para executar
também SQS e publisher, use os valores já documentados nas fases 6 e 7.

```powershell
$env:APP_ENV = 'development'
$env:DATABASE_URL = 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
$env:OIDC_ENABLED = 'true'
$env:OIDC_ISSUER_URL = 'http://localhost:8081/realms/jungle-gaming'
$env:OIDC_AUDIENCE = 'wagering-api'
$env:OIDC_HTTP_TIMEOUT = '5s'
go run ./cmd/api
```

| Variável | Comportamento |
| --- | --- |
| `OIDC_ENABLED` | Default `false`; `.env.example` usa `true`. Desabilitar não libera rotas: todos os probes protegidos continuam exigindo autenticação e respondem `401`. |
| `OIDC_ISSUER_URL` | Issuer completo e exato do realm; obrigatório quando habilitado. |
| `OIDC_AUDIENCE` | Default `wagering-api`; valor explicitamente vazio desliga somente a validação da audience em development/test. Desde a fase 11, valor vazio é rejeitado fora desses ambientes. |
| `OIDC_HTTP_TIMEOUT` | Default `5s`; limite das requisições discovery/JWKS. |
| `KEYCLOAK_PORT` / `KEYCLOAK_MANAGEMENT_PORT` | Portas locais de Compose: defaults `8081` / `9090`. Ajuste também issuer e URL de testes se mudar a porta. |

O backend não precisa de client secret, senha de admin ou segredo dos providers
para validar tokens. `KEYCLOAK_URL` legado não é usado como issuer. A configuração
de `localhost` corresponde ao backend executado no host; deployment em outra
rede deve definir um issuer canônico acessível pelo backend e pelos callers.

## Obter tokens com PowerShell

Em outro terminal:

```powershell
$tokenUrl = 'http://localhost:8081/realms/jungle-gaming/protocol/openid-connect/token'
$tokenA = (Invoke-RestMethod -Method Post -Uri $tokenUrl -Body @{
  grant_type = 'client_credentials'
  client_id = 'provider-a'
  client_secret = 'local-dev-provider-a-secret'
}).access_token
$tokenB = (Invoke-RestMethod -Method Post -Uri $tokenUrl -Body @{
  grant_type = 'client_credentials'
  client_id = 'provider-b'
  client_secret = 'local-dev-provider-b-secret'
}).access_token
$tokenInternal = (Invoke-RestMethod -Method Post -Uri $tokenUrl -Body @{
  grant_type = 'client_credentials'
  client_id = 'internal-service'
  client_secret = 'local-dev-internal-service-secret'
}).access_token

Invoke-RestMethod http://localhost:8080/auth/me -Headers @{Authorization="Bearer $tokenA"}
Invoke-RestMethod http://localhost:8080/auth/providers/provider-a -Headers @{Authorization="Bearer $tokenA"}
Invoke-RestMethod http://localhost:8080/auth/internal -Headers @{Authorization="Bearer $tokenInternal"}
```

Com `$tokenA` em `/auth/providers/provider-b` ou `/auth/internal`, o resultado é
`403`. Com `$tokenB`, a regra é simétrica. Sem header ou com token inválido,
o resultado é `401` com `WWW-Authenticate: Bearer`. Os erros usam o envelope JSON
existente, sem token, secret, claims brutas ou detalhes criptográficos.

## Obter tokens com curl (shell POSIX)

```sh
TOKEN_URL=http://localhost:8081/realms/jungle-gaming/protocol/openid-connect/token
TOKEN_A=$(curl -fsS "$TOKEN_URL" -d grant_type=client_credentials -d client_id=provider-a -d client_secret=local-dev-provider-a-secret | jq -r .access_token)
TOKEN_B=$(curl -fsS "$TOKEN_URL" -d grant_type=client_credentials -d client_id=provider-b -d client_secret=local-dev-provider-b-secret | jq -r .access_token)
TOKEN_INTERNAL=$(curl -fsS "$TOKEN_URL" -d grant_type=client_credentials -d client_id=internal-service -d client_secret=local-dev-internal-service-secret | jq -r .access_token)
curl -i http://localhost:8080/auth/me -H "Authorization: Bearer $TOKEN_A"
curl -i http://localhost:8080/auth/providers/provider-b -H "Authorization: Bearer $TOKEN_A"
curl -i http://localhost:8080/auth/internal -H "Authorization: Bearer $TOKEN_INTERNAL"
```

## Probes e política financeira

Os únicos probes novos são `GET /auth/me`, `GET /auth/providers/{providerId}` e
`GET /auth/internal`. Retornam a identidade e comprovam autenticação/permissões;
não implementam a API financeira da fase 9. Health live/ready continuam públicos.

Futuros handlers devem extrair `identity.Principal` do contexto e usar
`financial.AuthorizedService`, nunca chamar o core `financial.Service`
diretamente com uma identidade originada do HTTP:

- Provider pode criar operações e consultar somente suas transações. ProviderID
  ausente no input é derivado do principal; um valor divergente é `Forbidden`
  antes de qualquer acesso financeiro ou idempotência.
- Chaves HTTP recebem namespace persistente por provider; A e B podem usar a
  mesma chave sem compartilhar resultado. Autorização acontece antes do replay.
- Interno pode consultar qualquer provider, criar carteira/OPENING e ler carteiras.
  Não pode escrever operações externas fingindo ser um provider.
- Carteiras são de jogadores, sem ownership por provider. Leitura permanece
  internal-only nesta fase; não inferimos acesso à carteira a partir de uma aposta.
- Lookup de transação existente de outro provider retorna `403`; ID inexistente
  retorna `404`. Mismatch explícito de provider sempre retorna `403`, inclusive
  antes de consultar o repositório. Essa política pode revelar existência de um
  ID conhecido, mas não seus dados; foi escolhida para autorização explícita.

Com OIDC habilitado, falha de discovery/issuer no startup impede o listener HTTP
e Fx encerra os recursos já iniciados. Após startup, readiness segue verificando
inicialização e PostgreSQL; não consulta o IdP em cada probe. Liveness independe
do IdP. Durante indisponibilidade, assinatura ainda pode ser validada com chaves
em cache; chave desconhecida e refresh malsucedido falham com `401`. Não há
fallback que aceite token sem validação. A biblioteca reutiliza JWKS e atualiza
o conjunto ao encontrar uma nova chave; a rotação é testada com servidor JWKS
local de teste, sem construir mecanismo de rotação próprio.

SQS continua como canal interno com credenciais/configuração do broker. Seu
`providerId` é dado de negócio, e não principal HTTP. Mensagens não carregam
Bearer OIDC. LocalStack usa `test/test`; produção deve restringir produtores,
consumidores e filas com IAM/policies. Os workers mantêm o core financeiro.

## Testes reproduzíveis

Depois de `up --wait`, em PowerShell:

```powershell
$env:TEST_DATABASE_URL = 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
$env:TEST_SQS_ENDPOINT = 'http://localhost:4566'
$env:TEST_OIDC_ISSUER_URL = 'http://localhost:8081/realms/jungle-gaming'
$env:TEST_KEYCLOAK_HEALTH_URL = 'http://localhost:9090/health/ready'
go test ./... -count=1
go vet ./...
go test -race ./...
```

Sem as variáveis `TEST_*`, integrações correspondentes são explicitamente
puladas. A health URL opcional testa também o endpoint real de readiness do IdP.
Os testes de isolamento criam schemas PostgreSQL descartáveis, aplicam migrations
1–5 e fazem cleanup. Não consomem o backlog financeiro do operador.

`TestIntegrationKeycloakTokensAndValidation` obtém três access tokens reais via
client_credentials e valida discovery/JWKS, identidades distintas, internal,
missing/invalid, audience e issuer errados. Expiração é determinística: injeta
um relógio posterior ao `exp` do token real, sem alterar token, assinatura ou
chaves. Os testes unitários usam tokens RS256 assinados para cobrir assinatura,
claims, algoritmo, `nbf`, audience, cache/rotation e falhas.

`TestIntegrationOIDCProviderIsolation` usa Fx, PostgreSQL e tokens Keycloak reais.
Prova consultas/replays A e B, spoof no body, lookup por ID e external ID, wallet
internal-only, e ausência de efeitos indevidos sobre saldo/ledger/outbox. Suas
rotas financeiras `/test/*` existem somente no código de teste; a autorização
dos recursos acontece no caso de uso, sem filtro de ownership no router.

No Windows, `-race` exige CGO e compilador C (por exemplo gcc) disponível no PATH.
Sem esse pré-requisito, não é possível afirmar que o detector de corrida rodou.

Referências: [Keycloak containers](https://www.keycloak.org/server/containers),
[realm import](https://www.keycloak.org/server/importExport),
[health](https://www.keycloak.org/observability/health),
[go-oidc](https://github.com/coreos/go-oidc).
