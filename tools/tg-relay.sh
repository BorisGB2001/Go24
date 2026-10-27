#!/usr/bin/env bash
# Telegram → диспетчер: слушает бота (tg.py watch) вне Claude Code и вставляет каждое сообщение Бориса в окно main
# tmux-сессии claude. Фоновую прослушку внутри Claude Code останавливает его защита от нехватки памяти — отсюда это окно
# (claude:tg). Решение Бориса от 2026-10-26; запускается tools/boot.sh, вручную — tools/tg-relay.sh в окне tg.
cd /srv/go24
while true; do
  msg=$(python3 tools/tg.py watch 2>>"$HOME/.cache/go24-tg-relay.log") || { sleep 10; continue; }
  [ -n "$msg" ] || continue
  text="Сообщение Бориса в Telegram: $(tr "\n" " " <<<"$msg")"
  tmux send-keys -t claude:main -l "$text"
  sleep 1
  tmux send-keys -t claude:main Enter
  echo "$(date "+%F %T") передано: ${msg:0:80}" >> "$HOME/.cache/go24-tg-relay.log"
done
