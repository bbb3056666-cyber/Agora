#!/bin/sh
set -eu

# Named volume 覆盖镜像内目录后默认由 root 挂载；启动时修正上传目录权限，
# 再降权运行 Go 进程，确保文件上传可持久化且主进程不以 root 身份运行。
mkdir -p /app/.run/uploads
chown -R app:app /app/.run/uploads

exec su-exec app "$@"
