#!/usr/bin/env bash
# Тяжёлые тесты (make test, test-db, test-js) при многих потоках: одновременно идут не больше GO24_TEST_SLOTS прогонов
# (3), а если доступной памяти меньше GO24_TEST_MEM_GB (3 ГБ) или процессор перегружен (загрузка за минуту больше
# GO24_TEST_LOAD на ядро, 2) — по одному. Распоряжение Бориса от 2026-10-27; предел — после загрузки 110 на 12 ядрах.
# Makefile на сервере зовёт этот файл из /srv/go24, так что правка сразу действует во всех потоках.
#   memgate.sh команда [аргументы]
avail=$(awk '/MemAvailable/ {print int($2 / 1048576)}' /proc/meminfo)
load=$(awk '{print $1}' /proc/loadavg)
busy=$(awk -v l="$load" -v c="$(nproc)" -v k="${GO24_TEST_LOAD:-2}" 'BEGIN {print (l > c * k) ? 1 : 0}')
slots=${GO24_TEST_SLOTS:-3}
if [ "$avail" -lt "${GO24_TEST_MEM_GB:-3}" ] || [ "$busy" = 1 ]; then
  slots=1
  echo "memgate: доступно ${avail} ГБ, загрузка $load — тесты по одному" >&2
fi
while :; do
  for i in $(seq "$slots"); do
    exec 8>"/tmp/go24-heavy-tests.$i.lock"
    flock -n 8 && break 2
  done
  sleep 5
done
"$@"
