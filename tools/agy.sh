#!/usr/bin/env bash
# agy (Antigravity CLI) на srv-pve3 как ещё одни руки разработки: у него свой лимит, отдельный от Claude. Решение Бориса
# от 2026-10-28: «пусть agy кодят, но ты всё проверяй, прежде чем заливать в основную ветку; грузи его по полной».
#
#   agy.sh run KEY [MODEL] < task.md   поставить задачу: свежий main → srv-pve3, ветка agy/KEY, agy работает в своём окне tmux
#   agy.sh list                        задачи: идёт / готово / ошибка, сколько коммитов
#   agy.sh report KEY                  отчёт agy и журнал
#   agy.sh fetch KEY                   забрать ветку agy/KEY в /srv/go24 (для проверки: git diff main...agy/KEY)
#   [AGY_LOCAL=1] agy.sh accept KEY     после проверки: rebase на main, make test и test-db в отдельном дереве, ff в main,
#                                      push, make up — под замком выкладки потоков
#   agy.sh drop KEY                    убрать задачу (ветки и дерево) на обеих сторонах
# Модели: claude-opus-4-6-thinking (по умолчанию: код), claude-sonnet-4-6, gemini-3.8-flash-high (тексты, переводы).
set -euo pipefail
A=agent@10.147.10.23
MAIN=/srv/go24
MAX=${AGY_MAX:-12}  # одновременно на srv-pve3 (32 ядра, 125 ГБ)
ssha() { ssh -o BatchMode=yes -o ServerAliveInterval=15 "$A" "$@"; }

cmd=${1:-list}; shift || true
case $cmd in
run)
  KEY=${1:?ключ задачи, например GO24DEV-123}; MODEL=${2:-claude-opus-4-6-thinking}
  [ ! -f "$HOME/.config/go24/hold" ] || { echo "пауза работ (~/.config/go24/hold) — задачи не ставлю" >&2; exit 2; }
  task=$(cat); [ -n "$task" ] || { echo "задача — в stdin" >&2; exit 1; }
  busy=$(ssha 'grep -l "^running" ~/jobs/*/status 2>/dev/null | wc -l')
  [ "$busy" -lt "$MAX" ] || { echo "на srv-pve3 уже $busy задач (предел $MAX) — подожди" >&2; exit 2; }
  git -C "$MAIN" push -q "$A:go24.git" main:main
  ssha "mkdir -p ~/jobs/$KEY && cat > ~/jobs/$KEY/task.md" <<<"$task"
  ssha "tmux has-session -t agy 2>/dev/null || tmux new-session -d -s agy -n idle; tmux new-window -d -t agy -n '$KEY' 'bash ~/bin/agy-job.sh $KEY $MODEL'"
  echo "agy: задача $KEY поставлена ($MODEL)" ;;
list)
  ssha 'for d in ~/jobs/*/; do k=$(basename $d); s=$(cat $d/status 2>/dev/null); n=$(git -C ~/work/go24 rev-list --count origin/main..agy/$k 2>/dev/null || echo 0); printf "%-16s %-22s коммитов %s\n" "$k" "$s" "$n"; done' ;;
report)
  KEY=${1:?ключ}; ssha "cat ~/jobs/$KEY/report.md; echo; echo '--- журнал'; tail -5 ~/jobs/$KEY/job.log" ;;
fetch)
  KEY=${1:?ключ}
  git -C "$MAIN" fetch -q -f "$A:go24.git" "agy/$KEY:agy/$KEY"
  git -C "$MAIN" log --oneline "main..agy/$KEY"; git -C "$MAIN" diff --stat "main...agy/$KEY" | tail -3 ;;
accept)
  KEY=${1:?ключ}; T=$HOME/streams/agy-$KEY
  git -C "$MAIN" worktree remove --force "$T" 2>/dev/null || true  # дерево прошлой неудачной попытки держит ветку
  # AGY_LOCAL=1 — принять ветку в том виде, как её поправили здесь (конфликт с main разрешён вручную), без повторной загрузки
  [ "${AGY_LOCAL:-}" = 1 ] || git -C "$MAIN" fetch -q -f "$A:go24.git" "agy/$KEY:agy/$KEY"
  exec 9>/tmp/go24-deploy.lock; flock 9
  git -C "$MAIN" pull -q --ff-only
  rm -rf "$T"; git -C "$MAIN" worktree add -q "$T" "agy/$KEY"
  cd "$T"
  git rebase -q main || { git rebase --abort; echo "конфликт с main — поправь ветку agy/$KEY (или задачу заново)"; exit 1; }
  dup=$(ls internal/db/migrations | grep -oE '^[0-9]+' | uniq -d)
  [ -z "$dup" ] || { echo "номер миграции $dup занят — переименуй в ветке"; exit 1; }
  # проверка по тому, что изменилось: только .md — без тестов; только новые *_test.go — vet и тесты этих пакетов
  # (минуты вместо часов make test-db в общей очереди); остальное — make test, make test-db и выкладка
  files=$(git diff --name-only --diff-filter=AMDR main...HEAD)
  code=$(grep -vE '\.md$' <<<"$files" || true)
  if [ -z "$code" ]; then
    echo "только документация — без тестов"; deploy=0
  elif ! grep -vE '_test\.go$' <<<"$code" | grep -q . && [ -z "$(git diff --name-only --diff-filter=MDR main...HEAD -- '*.go')" ] \
       && ! grep -l '^//go:build' $code >/dev/null 2>&1; then
    pk=$(xargs -n1 dirname <<<"$code" | sort -u | sed 's|^|./|')
    go vet $pk && go test -race -count=1 $pk || { echo "тесты не прошли — в main не беру"; exit 1; }
    deploy=0
  else
    GO24_STREAM=agy make test && GO24_STREAM=agy make test-db || { echo "тесты не прошли — в main не беру"; exit 1; }
    deploy=1
  fi
  git -C "$MAIN" merge -q --ff-only "agy/$KEY"
  git -C "$MAIN" push -q
  cd "$MAIN" && git worktree remove --force "$T"
  [ "$deploy" = 0 ] || make up
  echo "agy/$KEY принят в main: $(git -C "$MAIN" log --oneline -1)" ;;
drop)
  KEY=${1:?ключ}
  ssha "tmux kill-window -t 'agy:$KEY' 2>/dev/null; git -C ~/work/go24 worktree remove --force ~/work/jobs/$KEY 2>/dev/null; git -C ~/work/go24 branch -qD agy/$KEY 2>/dev/null; git -C ~/go24.git branch -qD agy/$KEY 2>/dev/null; rm -rf ~/jobs/$KEY; true"
  git -C "$MAIN" branch -qD "agy/$KEY" 2>/dev/null || true
  echo "agy: задача $KEY убрана" ;;
*) echo "команды: run KEY [MODEL] < task | list | report KEY | fetch KEY | accept KEY | drop KEY" >&2; exit 1 ;;
esac
