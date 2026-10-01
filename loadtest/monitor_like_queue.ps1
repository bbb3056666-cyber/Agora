param(
    [int]$Samples = 36,
    [int]$IntervalSeconds = 5,
    [string]$OutputPath = "loadtest/results/like_queue_samples.csv"
)

# 每次采样读取 RabbitMQ 中 Like Intent 队列的积压，以及 MySQL Outbox 待发布数。
# 测试期间只做观察，不改变队列或业务数据。
$outputDirectory = Split-Path -Parent $OutputPath
New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null

$records = for ($index = 0; $index -lt $Samples; $index++) {
    $queues = docker compose exec -T rabbitmq rabbitmqctl list_queues name messages_ready messages_unacknowledged --formatter json |
        ConvertFrom-Json
    $likeQueue = $queues | Where-Object { $_.name -eq "like.intent.events" }
    $notificationQueue = $queues | Where-Object { $_.name -eq "notification.like" }

    $outboxRows = docker compose exec -T mysql mysql -uroot -p123456 Agora -N -e "SELECT COUNT(*) FROM outbox_msgs WHERE status='pending' AND event_type IN ('like_intent','like_applied');"
    [pscustomobject]@{
        elapsed_seconds = $index * $IntervalSeconds
        timestamp       = (Get-Date).ToString("o")
        like_ready      = [int]$likeQueue.messages_ready
        like_unacked    = [int]$likeQueue.messages_unacknowledged
        notification_ready   = [int]$notificationQueue.messages_ready
        notification_unacked = [int]$notificationQueue.messages_unacknowledged
        outbox_pending  = [int]($outboxRows | Select-Object -Last 1)
    }

    if ($index -lt $Samples - 1) {
        Start-Sleep -Seconds $IntervalSeconds
    }
}

$records | Export-Csv -NoTypeInformation -Encoding utf8 -Path $OutputPath
