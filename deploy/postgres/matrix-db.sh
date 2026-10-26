#!/bin/sh
# boris.gb 2026-10-12 07:16 - база Synapse в уже работающем postgres (make matrix-db); на пустом томе её создаёт init-databases.sh
set -eu
if psql -U postgres -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'synapse'" | grep -q 1; then
  echo "synapse: уже есть"; exit 0
fi
psql -v ON_ERROR_STOP=1 -U postgres -v syn_pw="$SYNAPSE_DB_PASSWORD" <<'SQL'
CREATE USER synapse WITH PASSWORD :'syn_pw';
CREATE DATABASE synapse OWNER synapse ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0;
SQL
echo "synapse: база создана"
