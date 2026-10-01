param(
    [Parameter(Mandatory = $true)]
    [int]$DurationSeconds,
    [Parameter(Mandatory = $true)]
    [string]$OutputDirectory
)

# docker exec 中 MySQL 客户端会输出密码安全提示；该提示不应中断采集循环。
$ErrorActionPreference = 'Continue'
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

$containerFile = Join-Path $OutputDirectory 'container_stats_1s.tsv'
$mysqlFile = Join-Path $OutputDirectory 'mysql_status_1s.tsv'

"timestamp`tcontainer`tcpu_percent`tmem_usage" | Set-Content -LiteralPath $containerFile
"timestamp`tstatus" | Set-Content -LiteralPath $mysqlFile

for ($index = 0; $index -lt $DurationSeconds; $index++) {
    $timestamp = (Get-Date).ToString('o')
    docker stats --no-stream --format '{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}' agora-backend-1 agora-mysql-1 agora-redis-1 |
        ForEach-Object { "$timestamp`t$($_ -replace '\|', "`t")" } |
        Add-Content -LiteralPath $containerFile

    $status = docker exec agora-mysql-1 mysql -uroot -p123456 -N -B -e "SHOW GLOBAL STATUS WHERE Variable_name IN ('Connections','Threads_connected','Threads_running','Max_used_connections')" 2>$null |
        ForEach-Object { $_ -replace "`t", '=' }
    $status = $status -join ';'
    "$timestamp`t$status" | Add-Content -LiteralPath $mysqlFile
    Start-Sleep -Seconds 1
}
