#!/bin/sh
set -eu

# Run on the test VM from the Compose project directory. The test binary is
# copied into the MySQL container and can only reset this dedicated database.
test_bin=/tmp/like-batch-integration.test

cleanup() {
    docker compose exec -T mysql sh -c \
        'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -e "DROP DATABASE IF EXISTS agora_like_opt_test_20261026_batch"'
}
trap cleanup EXIT

docker compose exec -T mysql sh -c \
    'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -e "CREATE DATABASE IF NOT EXISTS agora_like_opt_test_20261026_batch"'
docker cp "$test_bin" agora-mysql-1:"$test_bin"
docker compose exec -T mysql sh -c \
    'chmod +x /tmp/like-batch-integration.test && AGORA_LIKE_TEST_DSN="root:$MYSQL_ROOT_PASSWORD@tcp(127.0.0.1:3306)/agora_like_opt_test_20261026_batch?parseTime=true" /tmp/like-batch-integration.test -test.run TestLikeBatchMySQL -test.v'
