#!/bin/sh
# boris.gb 2026-10-25 13:58 - телефония go24 (GO24DEV-501): запуск Asterisk. Конфигурация из этой папки кладётся поверх
# стандартной; ari.conf и http.conf собираются здесь из переменных (пароль ARI — ASTERISK_SECRET из deploy/.env,
# адрес — BIND_IP), учётки сотрудников пишет go24 в общий том /etc/asterisk/go24 и перечитывает через ARI.
# boris.gb 2026-10-25 17:03 - детализация звонков (GO24DEV-503): go24-globals.conf — куда план набора сообщает о звонках
# (go24 на BIND_IP:8080) и ключ (тот же ASTERISK_SECRET)
set -e
: "${ASTERISK_SECRET:?set ASTERISK_SECRET in deploy/.env}"
cp /etc/asterisk-go24/*.conf /etc/asterisk/
cat > /etc/asterisk/http.conf <<CONF
[general]
enabled = yes
bindaddr = ${BIND_IP:-127.0.0.1}
bindport = 8088
CONF
cat > /etc/asterisk/ari.conf <<CONF
[general]
enabled = yes
[go24]
type = user
read_only = no
password = ${ASTERISK_SECRET}
CONF
cat > /etc/asterisk/go24-globals.conf <<CONF
GO24_CDR=${GO24_CDR_URL:-http://${BIND_IP:-127.0.0.1}:8080/telephony/cdr}
GO24_KEY=${ASTERISK_SECRET}
CONF
chown asterisk:asterisk /etc/asterisk/*.conf
chmod 0640 /etc/asterisk/go24-globals.conf /etc/asterisk/ari.conf
exec /usr/sbin/asterisk -f -vvv -T -n -U asterisk -p
