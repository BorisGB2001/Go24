#!/usr/bin/env bash
# make test (go vet + go test без базы) на srv-pve3 (agent: 32 ядра, 125 ГБ) вместо сервера go24 — просьба Бориса от
# 2026-10-28 перенести часть компиляции и тестов на agent. Дерево копируется rsync в ~/build/NAME (NAME — поток или
# main), там Go 1.27 без Docker; кэш сборки и модулей общий. На agent одновременно не больше GO24_REMOTE_SLOTS (6)
# прогонов. Если agent недоступен — код 3, и Makefile запускает тесты здесь, как раньше.
# Тесты на базе (test-db), test-js и выкладка остаются на go24: Docker на agent недоступен.
#   remote-test.sh [каталог]
set -uo pipefail
A=agent@10.147.10.23
SRC=$(cd "${1:-.}" && pwd)
KEY=${GO24_STREAM:-$(basename "$SRC")}
SLOTS=${GO24_REMOTE_SLOTS:-6}
ssha() { ssh -o BatchMode=yes -o ConnectTimeout=5 -o ServerAliveInterval=15 "$A" "$@"; }

ssha true 2>/dev/null || { echo "remote-test: agent недоступен — тесты здесь" >&2; exit 3; }
# только файлы git (без игнорируемых .gitignore): deploy/.env, бэкапы и данные на agent не попадают; удалённые и
# переименованные в дереве файлы удаляются и там (с --files-from --delete их не трогал: старая миграция с тем же
# номером ломала TestEmbeddedMigrations)
ssha "mkdir -p build/$KEY" &&
  rsync -a --delete --exclude=.git --filter=':- .gitignore' -e "ssh -o BatchMode=yes" "$SRC/" "$A:build/$KEY/" ||
  { echo "remote-test: rsync не прошёл — тесты здесь" >&2; exit 3; }
# boris.gb 2026-10-28 21:20 - --files-from не удаляет на agent файлы, которых больше нет в дереве (переименованная
#   миграция ломала TestEmbeddedMigrations): лишние удаляем сами
git -C "$SRC" ls-files -co --exclude-standard | sort >"${TMPDIR:-/tmp}/go24-rt.$$"
ssha "cd build/$KEY && find . -type f | sed 's|^\./||' | sort" | comm -13 "${TMPDIR:-/tmp}/go24-rt.$$" - |
  ssha "cd build/$KEY && xargs -r -d '\n' rm -f --"
rm -f "${TMPDIR:-/tmp}/go24-rt.$$"
echo "remote-test: go vet + go test на agent (~/build/$KEY)"
ssha "export PATH=\$HOME/.local/go/bin:\$PATH GOTOOLCHAIN=local
  while :; do
    for i in \$(seq $SLOTS); do exec 8>/tmp/go24-remote-test.\$i.lock; flock -n 8 && break 2; done
    sleep 3
  done
  cd ~/build/$KEY && go vet ./... && go test ./..."
rc=$?
[ "$rc" = 255 ] && { echo "remote-test: связь с agent оборвалась — тесты здесь" >&2; exit 3; }
exit "$rc"
