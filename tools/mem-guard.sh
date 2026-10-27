#!/usr/bin/env bash
# Сторож нагрузки сервера разработки: сам меняет число потоков Claude (решение Бориса от 2026-10-27: «разрешаю
# увеличивать или уменьшать количество потоков в зависимости от нагрузки системы»). Cron раз в 2 минуты; токенов не тратит.
#  - памяти мало или процессор перегружен — один поток на паузу (сначала наименее важный, по ORDER) и его окно
#    закрывается; работа остаётся в дереве ~/streams/NAME;
#  - запас памяти и процессора есть 10 минут подряд — один поток с паузы снова запускается (сначала самый важный);
#  - не чаще одного изменения в 10 минут, рост — не раньше чем через 30 минут после сокращения; от MIN до MAX потоков.
set -uo pipefail
exec 8>/tmp/go24-mem-guard.lock; flock -n 8 || exit 0  # запуск потока бывает дольше 2 минут
SESSION=claude
LOW_MB=${LOW_MB:-3072}      # доступно меньше — сокращать (поток, который сейчас ничего не делает)
CRIT_MB=${CRIT_MB:-1536}    # доступно меньше — сокращать и работающий
HIGH_MB=${HIGH_MB:-8192}    # доступно больше (и процессор свободен) — можно добавить поток
MIN_STREAMS=${MIN_STREAMS:-4}
MAX_STREAMS=${MAX_STREAMS:-20}  # при 32 потоках сервер перестал отвечать (2026-10-27)
# порядок сокращения: первые — первыми на паузу; запуск с паузы — в обратном порядке
ORDER="bi kb calendar openlines store docs admin bp company feed calls tasks im crm drive mail"
ST=$HOME/.cache/go24-mem-guard; mkdir -p "$ST"
LOG=$HOME/.cache/go24-mem-guard.log
PAUSED=$HOME/.config/go24/streams-paused
now=$(date +%s)

avail=$(awk '/^MemAvailable:/ {print int($2 / 1024)}' /proc/meminfo)
cores=$(nproc)
load=$(awk '{print $2}' /proc/loadavg)  # средняя за 5 минут
overload=$(awk -v l="$load" -v c="$cores" 'BEGIN {print (l > c * 6) ? 1 : 0}')
idle=$(awk -v l="$load" -v c="$cores" 'BEGIN {print (l < c * 0.7) ? 1 : 0}')
running=$(tmux list-windows -t "$SESSION" -F '#W' 2>/dev/null | grep -vx -e main -e tg)
count=$(wc -w <<<"$running")
last=$(cat "$ST/last" 2>/dev/null || echo 0)
lastdown=$(cat "$ST/lastdown" 2>/dev/null || echo 0)

tell() {  # в журнал, Борису в Telegram и диспетчеру
  echo "$(date '+%F %T') $1" >>"$LOG"
  (cd /srv/go24 && python3 tools/tg.py send "$1" >/dev/null 2>&1) || true
  tmux send-keys -t "$SESSION:main" -l "Сторож нагрузки: $1"
  sleep 1; tmux send-keys -t "$SESSION:main" Enter
}
busy() { tmux capture-pane -p -t "$SESSION:$1" | tail -4 | grep -q "esc to interrupt"; }

# сокращение
if [ "$avail" -lt "$LOW_MB" ] || [ "$overload" = 1 ]; then
  rm -f "$ST/calm"
  [ "$count" -gt "$MIN_STREAMS" ] || exit 0
  [ $((now - last)) -ge 600 ] || [ "$avail" -lt "$CRIT_MB" ] || exit 0
  pick=""
  for s in $ORDER $running; do
    grep -qx "$s" <<<"$running" || continue
    if [ "$avail" -lt "$CRIT_MB" ] || ! busy "$s"; then pick=$s; break; fi
  done
  [ -n "$pick" ] || exit 0
  /srv/go24/tools/stream.sh pause "$pick" >/dev/null
  tmux kill-window -t "$SESSION:$pick"
  echo "$now" >"$ST/last"; echo "$now" >"$ST/lastdown"
  tell "памяти доступно $avail МБ, нагрузка $load на $cores ядер — поток $pick на паузе (работа в ~/streams/$pick), потоков $((count - 1))."
  exit 0
fi

# рост: запас держится 10 минут подряд
if [ "$avail" -gt "$HIGH_MB" ] && [ "$idle" = 1 ] && [ "$count" -lt "$MAX_STREAMS" ]; then
  [ -f "$ST/calm" ] || echo "$now" >"$ST/calm"
  [ $((now - $(cat "$ST/calm"))) -ge 600 ] || exit 0
  [ $((now - last)) -ge 600 ] && [ $((now - lastdown)) -ge 1800 ] || exit 0
  pick=""
  for s in $(tr ' ' '\n' <<<"$ORDER" | tac); do grep -qx "$s" "$PAUSED" 2>/dev/null && { pick=$s; break; }; done
  [ -n "$pick" ] || exit 0
  /srv/go24/tools/stream.sh resume "$pick" >/dev/null
  /srv/go24/tools/stream.sh start "$pick" "Поток снова запущен сторожем нагрузки. Сначала git status и git rebase main: доделай и выложи начатое в дереве; свой раздел мог временно вести объединённый поток — сверься с devlog.py mytasks и docs/streams.md." >/dev/null
  echo "$now" >"$ST/last"; rm -f "$ST/calm"
  tell "запас есть (памяти $avail МБ, нагрузка $load на $cores ядер) — поток $pick снова запущен, потоков $((count + 1))."
else
  rm -f "$ST/calm"
fi
