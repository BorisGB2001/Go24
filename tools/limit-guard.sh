#!/usr/bin/env bash
# Правило Бориса (2026-10-29, повторено 2026-11-04): если 5-часовой или недельный лимит Claude израсходован на 70% и
# больше — потоки доделывают текущие задачи, новых не берут; лимиты обновились — работа продолжается сама, без Бориса.
# Раз в 5 минут (cron) читает расход плана (тот же запрос, что /usage в Claude Code; токенов модели не тратит) и
#  - >= 70%: ставит паузу ~/.config/go24/hold (stream.sh, agy.sh и limit-watch.sh её уважают), говорит диспетчеру и в Telegram;
#  - < 70% и пауза из-за лимита: снимает её, запускает 4 главных потока (остальные — mem-guard.sh) и будит диспетчера.
# Паузу по другой причине (в файле нет слова «лимит») не снимает. Журнал: ~/.cache/go24-limit-guard.log
#   limit-guard.sh [status]
set -uo pipefail
cd /srv/go24
HOLD=$HOME/.config/go24/hold
MAX=${GO24_LIMIT_MAX:-70}
SESSION=claude

usage=$(python3 - <<'EOF'
import json, os, urllib.request
tok = json.load(open(os.path.expanduser("~/.claude/.credentials.json")))["claudeAiOauth"]["accessToken"]
r = urllib.request.Request("https://api.anthropic.com/api/oauth/usage",
                           headers={"Authorization": "Bearer " + tok, "anthropic-beta": "oauth-2025-04-20"})
d = json.load(urllib.request.urlopen(r, timeout=30))
f, w = d.get("five_hour") or {}, d.get("seven_day") or {}
print(int(f.get("utilization") or 0), int(w.get("utilization") or 0), f.get("resets_at") or "-", w.get("resets_at") or "-")
EOF
) || { echo "$(date '+%F %T') расход не прочитан"; exit 0; }
read -r h w hr wr <<<"$usage"
when() { [ "$1" = - ] && echo "?" || date -d "$1" '+%d.%m %H:%M'; }
[ "${1:-}" = status ] && { echo "5 часов: $h% (до $(when "$hr")), неделя: $w% (до $(when "$wr")), порог $MAX%, пауза: $(cat "$HOLD" 2>/dev/null || echo нет)"; exit 0; }

say() {  # сообщение диспетчеру (окно main)
  tmux list-windows -t "$SESSION" -F '#W' 2>/dev/null | grep -qx main || return 0
  tmux send-keys -t "$SESSION:main" C-u
  tmux send-keys -t "$SESSION:main" -l "$1"
  sleep 1
  tmux send-keys -t "$SESSION:main" Enter
}
tg() { timeout 30 python3 tools/tg.py send <<<"$1" >/dev/null 2>&1 || true; }

if [ "$h" -ge "$MAX" ] || [ "$w" -ge "$MAX" ]; then
  [ -f "$HOLD" ] && exit 0
  if [ "$w" -ge "$MAX" ]; then why="недельный лимит $w% (обновится $(when "$wr"))"; else why="5-часовой лимит $h% (обновится $(when "$hr"))"; fi
  echo "$(date '+%F %H:%M') авто: $why >= $MAX% — пауза до обновления лимита (limit-guard.sh)" > "$HOLD"
  echo "$(date '+%F %T') пауза: $why"
  say "limit-guard: $why >= $MAX%. По правилу Бориса — пауза: потоки и agy доделывают текущие задачи до deploy, новых не берут, NEXT не выдавать. Новые задачи не начинай. Когда лимит обновится, limit-guard сам снимет паузу и запустит потоки."
  tg "go24: $why ≥ $MAX% — потоки доделывают текущие задачи, новые не начинают. После обновления лимита продолжу сам."
  exit 0
fi

[ -f "$HOLD" ] || exit 0
grep -q "лимит" "$HOLD" || exit 0  # пауза не из-за лимита — решает Борис
rm -f "$HOLD"
echo "$(date '+%F %T') пауза снята: 5 часов $h%, неделя $w%"
started=""
# сразу — самые важные потоки (конец ORDER в mem-guard.sh), остальные добавит сторож нагрузки mem-guard.sh по памяти и процессору
for s in ${GO24_RESUME:-mail drive crm im}; do
  tools/stream.sh resume "$s" >/dev/null 2>&1
  tools/stream.sh start "$s" "Лимиты обновились, сессия новая. Прочитай CLAUDE.md и docs/streams.md, git pull/rebase на main, devlog.py inbox и mytasks; продолжи задачи своего раздела." >/dev/null 2>&1 && started="$started $s"
done
say "limit-guard: лимиты обновились (5 часов $h%, неделя $w%) — пауза снята, запущены потоки:${started:- нет} (остальные добавит mem-guard по нагрузке). Первая задача — GO24DEV-1016 (обновление по кнопке из GitHub, ТЗ в задаче), назначь её потоку. Заведи в Go24-Dev задачи на падающие в main тесты TestOpenChats (internal/matrixsync, openchats_test.go:157), TestCalendarSettingsHTTP (internal/web, calsettings_test.go:133, зависит от даты), TestHRRights (internal/web, hr_test.go:157) — они пока в SKIP публичного выпуска. Загрузи agy (6–10 задач). Коротко сообщи Борису в Telegram."
tg "go24: лимиты обновились (5 часов $h%, неделя $w%) — пауза снята, потоки запущены:${started:- нет}. Первая задача — обновление по кнопке из GitHub (GO24DEV-1016)."
