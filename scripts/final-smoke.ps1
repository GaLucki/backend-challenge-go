# Local Compose fixtures only. Creates a unique wallet and preserves its audit history.
param([string]$Base = 'http://localhost:8080')
$ErrorActionPreference = 'Stop'
$tokenUrl = 'http://localhost:8081/realms/jungle-gaming/protocol/openid-connect/token'
$tokens = @{}
foreach ($client in @('provider-a','provider-b','internal-service')) {
    $tokens[$client] = (Invoke-RestMethod -Method Post -Uri $tokenUrl -Body @{
        grant_type='client_credentials'; client_id=$client; client_secret="local-dev-$client-secret"
    }).access_token
}
$internal = @{Authorization="Bearer $($tokens['internal-service'])"}
$id = 'final-audit-' + [guid]::NewGuid().ToString('N')
$provider = @{Authorization="Bearer $($tokens['provider-a'])"; 'Idempotency-Key'=$id}
function Assert-Audit($condition, $message) { if (-not $condition) { throw $message } }
function Status-Audit($uri, $headers) {
    try { return [int](Invoke-WebRequest -UseBasicParsing $uri -Headers $headers).StatusCode }
    catch { if ($_.Exception.Response) { return [int]$_.Exception.Response.StatusCode }; throw }
}
Assert-Audit ((Status-Audit "$Base/health/live" @{}) -eq 200) 'liveness failed'
Assert-Audit ((Status-Audit "$Base/health/ready" @{}) -eq 200) 'readiness failed'
Assert-Audit ((Status-Audit "$Base/metrics" @{}) -eq 401) 'anonymous metrics accepted'
Assert-Audit ((Status-Audit "$Base/metrics" $provider) -eq 403) 'provider metrics accepted'
$metrics = (Invoke-WebRequest -UseBasicParsing "$Base/metrics" -Headers $internal).Content
Assert-Audit ($metrics -match 'wagering_http_requests_total') 'Prometheus exposition missing'
$wallet = Invoke-RestMethod -Method Post "$Base/wallets" -Headers $internal -ContentType 'application/json' -Body (@{
    playerId=$id; initialBalance=@{amount='100.00'; currency='BRL'}
} | ConvertTo-Json -Depth 5)
$data = @{providerId='provider-a'; externalTransactionId=$id; playerId=$id; walletId=$wallet.id; roundId='audit-round'; gameId='audit-game'; kind='BET'; money=@{amount='25.00'; currency='BRL'}}
$body = $data | ConvertTo-Json -Depth 5
$bet = Invoke-RestMethod -Method Post "$Base/wagering/transactions" -Headers $provider -ContentType 'application/json' -Body $body
Assert-Audit ($bet.status -eq 'PROCESSED' -and $bet.balance.amount -eq '75.00') 'original HTTP wager contract failed'
Assert-Audit ((Status-Audit "$Base/wagering/transactions/$($bet.transactionId)" @{}) -eq 401) 'anonymous transaction accepted'
Assert-Audit ((Status-Audit "$Base/wagering/transactions/$($bet.transactionId)" @{Authorization="Bearer $($tokens['provider-b'])"}) -eq 403) 'foreign transaction disclosed'
Assert-Audit ((Status-Audit "$Base/providers/provider-a/wagering/transactions/$id" $provider) -eq 200) 'provider lookup failed'
Assert-Audit ((Status-Audit "$Base/wallets/$($wallet.id)" $provider) -eq 403) 'provider wallet access accepted'
function Digest-Audit([string]$value) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($value)))).Replace('-','').ToLowerInvariant() }
    finally { $sha.Dispose() }
}
$data.idempotencyKey = $id
$messageId = 'message-' + $id
$envelope = @{messageId=$messageId; type='WagerTransactionRequested'; occurredAt=[DateTime]::UtcNow.ToString('o'); data=$data} | ConvertTo-Json -Depth 6
New-Item -ItemType Directory -Force artifacts | Out-Null
$commandFile = Join-Path (Get-Location).Path 'artifacts/phase12-smoke-command.json'
[IO.File]::WriteAllText($commandFile, $envelope, (New-Object Text.UTF8Encoding($false)))
docker compose --env-file .env.example cp $commandFile localstack:/tmp/phase12-smoke-command.json | Out-Null
Assert-Audit ($LASTEXITCODE -eq 0) 'command fixture copy failed'
$queue = docker compose --env-file .env.example exec -T localstack awslocal sqs get-queue-url --queue-name wager-transactions.fifo --query QueueUrl --output text
docker compose --env-file .env.example exec -T localstack awslocal sqs send-message --queue-url $queue --message-body file:///tmp/phase12-smoke-command.json --message-group-id ('wallet:' + (Digest-Audit $wallet.id)) --message-deduplication-id ('delivery:' + (Digest-Audit $messageId)) | Out-Null
Assert-Audit ($LASTEXITCODE -eq 0) 'SQS publish failed'
$deadline = [DateTime]::UtcNow.AddSeconds(30)
do {
    $complete = docker compose --env-file .env.example exec -T postgres psql -U wagering -d wagering -At -c "SELECT count(*) FROM inbox_messages WHERE consumer_name='wager-financial-v1' AND message_id='$messageId' AND completed_at IS NOT NULL;"
    Assert-Audit ($LASTEXITCODE -eq 0) 'Inbox probe failed'
    if ($complete.Trim() -eq '1') { break }
    Start-Sleep -Milliseconds 100
} while ([DateTime]::UtcNow -lt $deadline)
Assert-Audit ($complete.Trim() -eq '1') 'original SQS envelope was not committed'
$replay = Invoke-RestMethod -Method Post "$Base/wagering/transactions" -Headers $provider -ContentType 'application/json' -Body $body
Assert-Audit ($replay.idempotentReplay -and $replay.balance.amount -eq '75.00') 'HTTP/SQS idempotency failed'
$audit = Invoke-RestMethod -Method Post "$Base/wallets/$($wallet.id)/reconciliation" -Headers $internal
Assert-Audit ($audit.consistent -and $audit.checkedEntries -eq 2 -and $audit.difference.amount -eq '0.00') 'reconciliation failed'
$ledger = Invoke-RestMethod "$Base/wallets/$($wallet.id)/ledger?limit=50" -Headers $internal
Assert-Audit ($ledger.items.Count -eq 2) 'duplicate ledger effect'
do {
    $published = docker compose --env-file .env.example exec -T postgres psql -U wagering -d wagering -At -c "SELECT count(*) FROM outbox_events WHERE aggregate_id IN ('$($wallet.id)','$($wallet.openingTransactionId)','$($bet.transactionId)') AND published_at IS NOT NULL;"
    Assert-Audit ($LASTEXITCODE -eq 0) 'Outbox probe failed'
    if ($published.Trim() -eq '4') { break }
    Start-Sleep -Milliseconds 100
} while ([DateTime]::UtcNow -lt $deadline)
Assert-Audit ($published.Trim() -eq '4') 'Outbox publication incomplete'
$report = @{status='PASS'; identities=3; health=200; anonymous=401; foreignProvider=403; internalMetrics=200; balance=$audit.storedBalance.amount; walletVersion=$audit.walletVersion; ledgerEntries=$audit.checkedEntries; inboxCompleted=1; publishedEvents=4; httpSqsReplay=$replay.idempotentReplay}
$report | ConvertTo-Json | Set-Content -Encoding utf8 artifacts/phase12-smoke-result.json
$report | ConvertTo-Json
# Tokens are never printed or persisted. Fixture payload contains no credentials.
