param(
    [int]$Samples = 30,
    [int]$IntervalSeconds = 5,
    [string]$OutputPath = "loadtest/results/like_diagnose_samples.csv"
)

# 点赞压测诊断采样：只读取运行状态，不改动队列、数据库或业务数据。
$outputDirectory = Split-Path -Parent $OutputPath
New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null

for ($index = 0; $index -lt $Samples; $index++) {
    $queues = docker compose exec -T rabbitmq rabbitmqctl list_queues name messages_ready messages_unacknowledged --formatter json |
        ConvertFrom-Json
    $likeQueue = $queues | Where-Object { $_.name -eq "like.intent.events" }
    $notificationQueue = $queues | Where-Object { $_.name -eq "notification.like" }

    $apiDB = Invoke-RestMethod "http://127.0.0.1:6060/debug/db/stats"
    $mysql = docker compose exec -T mysql mysql -uroot -p123456 -N -e "SHOW GLOBAL STATUS WHERE Variable_name IN ('Threads_connected','Threads_running','Innodb_row_lock_waits','Innodb_row_lock_time');"
    $mysqlValues = @{}
    foreach ($line in $mysql) {
        $parts = $line -split "`t"
        if ($parts.Length -eq 2) { $mysqlValues[$parts[0]] = $parts[1] }
    }
    $outboxRows = docker compose exec -T mysql mysql -uroot -p123456 Agora -N -e "SELECT COUNT(*) FROM outbox_msgs WHERE status='pending' AND event_type IN ('like_intent','like_applied');"

    $stats = docker stats --no-stream --format "{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}" agora-backend-1 agora-worker-1 agora-mysql-1 agora-redis-1 agora-rabbitmq-1
    $containerValues = @{}
    foreach ($line in $stats) {
        $parts = $line -split "\|"
        if ($parts.Length -eq 3) { $containerValues[$parts[0]] = $parts[1] + " / " + $parts[2] }
    }

    [pscustomobject]@{
        timestamp                   = (Get-Date).ToString("o")
        api_open_connections        = $apiDB.open_connections
        api_in_use_connections      = $apiDB.in_use
        api_wait_count              = $apiDB.wait_count
        api_wait_duration_ms        = $apiDB.wait_duration_ms
        mysql_threads_connected     = $mysqlValues['Threads_connected']
        mysql_threads_running       = $mysqlValues['Threads_running']
        mysql_row_lock_waits        = $mysqlValues['Innodb_row_lock_waits']
        mysql_row_lock_time_ms      = $mysqlValues['Innodb_row_lock_time']
        like_ready                  = [int]$likeQueue.messages_ready
        like_unacked                = [int]$likeQueue.messages_unacknowledged
        notification_ready          = [int]$notificationQueue.messages_ready
        notification_unacked        = [int]$notificationQueue.messages_unacknowledged
        outbox_pending              = [int]($outboxRows | Select-Object -Last 1)
        backend_resources           = $containerValues['agora-backend-1']
        worker_resources            = $containerValues['agora-worker-1']
        mysql_resources             = $containerValues['agora-mysql-1']
        redis_resources             = $containerValues['agora-redis-1']
        rabbitmq_resources          = $containerValues['agora-rabbitmq-1']
    } | Export-Csv -NoTypeInformation -Encoding utf8 -Append -Path $OutputPath

    if ($index -lt $Samples - 1) { Start-Sleep -Seconds $IntervalSeconds }
}
