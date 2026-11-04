#!/usr/bin/env bash
# События питания от гипервизора pve (ноутбук, на котором работает эта VM): его pve-battery-guard.sh заходит сюда по
# ssh ключом root@pve, а authorized_keys разрешает этому ключу только этот скрипт (command=…). Просьба Бориса от
# 2026-10-28: при разряде батареи до 70% завершить все сессии Claude и не запускать их, пока не вернётся питание от сети.
#   power.sh notify < текст   сообщение Борису в Telegram
#   power.sh low              питания нет, батарея садится: флаг ~/.config/go24/on-battery, все окна Claude закрываются
#                             (работа остаётся в деревьях ~/streams/NAME); stream.sh, mem-guard и boot.sh их не запускают
#   power.sh ok               питание вернулось: флаг снимается, диспетчер и потоки запускаются (tools/boot.sh)
#   power.sh status           есть ли флаг
#   power.sh flush            отправить отложенные сообщения (вызывает mem-guard.sh раз в 2 минуты)
# Без сети у хоста обычно пропадает и интернет: сообщение, которое не ушло в Telegram, ложится в очередь
# ~/.cache/go24-power-queue (переживает выключение) и уходит при первой возможности с пометкой «отложено».
set -uo pipefail
SESSION=claude
FLAG=$HOME/.config/go24/on-battery
LOG=$HOME/.cache/go24-power.log
cmd=${1:-status}
# вход по ssh от pve: команда — из SSH_ORIGINAL_COMMAND, и только из этого списка
if [ "$cmd" = remote ]; then
  set -f; set -- ${SSH_ORIGINAL_COMMAND:-}
  cmd=${1:-}
  case $cmd in notify|low|ok|status|flush) ;; *) echo "power: нет команды «$cmd»" >&2; exit 1 ;; esac
fi
QUEUE=$HOME/.cache/go24-power-queue   # отложенные сообщения: каждое — файл, имя — время постановки
log() { echo "$(date '+%F %T') $*" >>"$LOG"; }
send() { (cd /srv/go24 && timeout 30 python3 tools/tg.py send "$1" >/dev/null 2>&1); }
flush() {  # сначала старые, по порядку; на первой неудаче — стоп до следующего раза
  for f in $(ls "$QUEUE" 2>/dev/null | sort); do
    send "(отложено с $(date -d "@${f%%.*}" '+%d.%m %H:%M')) $(cat "$QUEUE/$f")" || return 1
    rm -f "$QUEUE/$f"; log "flush: отправлено $f"
  done
}
tg() {
  if flush && send "$1"; then return 0; fi
  mkdir -p "$QUEUE"; printf '%s' "$1" >"$QUEUE/$(date +%s).$$.$RANDOM"
  log "Telegram недоступен — сообщение в очереди"
}

case $cmd in
notify)
  text=$(head -c 4000)
  [ -n "$text" ] && { tg "$text"; log "notify: ${text%%$'\n'*}"; } ;;
low)
  [ -f "$FLAG" ] && { echo "уже на батарее"; exit 0; }
  mkdir -p "$(dirname "$FLAG")"; date '+%F %T' >"$FLAG"
  wins=$(tmux list-windows -t "$SESSION" -F '#W' 2>/dev/null | grep -vx tg | tr '\n' ' ')
  for w in $wins; do tmux send-keys -t "$SESSION:$w" Escape; done  # прервать текущий ответ
  sleep 3
  for w in $wins; do tmux send-keys -t "$SESSION:$w" -l "/exit"; sleep 0.5; tmux send-keys -t "$SESSION:$w" Enter; done
  sleep 15
  for w in $wins; do tmux kill-window -t "$SESSION:$w" 2>/dev/null; done
  log "low: закрыты окна $wins"
  tg "go24: питание на батарее — сессии Claude закрыты (${wins:-нет}), работа осталась в деревьях. Запущу снова, когда вернётся сеть." ;;
ok)
  [ -f "$FLAG" ] || { echo "флага нет"; exit 0; }
  rm -f "$FLAG"
  log "ok: флаг снят, запуск boot.sh"
  BOOT_DELAY=0 setsid nohup /srv/go24/tools/boot.sh >>"$HOME/.cache/go24-boot.log" 2>&1 </dev/null &
  tg "go24: питание от сети вернулось — запускаю диспетчера и потоки." ;;
flush)
  [ -n "$(ls "$QUEUE" 2>/dev/null)" ] && flush ;;
status)
  if [ -f "$FLAG" ]; then echo "на батарее с $(cat "$FLAG")"; else echo "питание от сети"; fi ;;
*) echo "команды: notify | low | ok | status | flush" >&2; exit 1 ;;
esac
