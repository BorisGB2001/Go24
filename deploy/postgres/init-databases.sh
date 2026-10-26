#!/bin/sh
# boris.gb 2026-10-11 13:32 - создаёт базы go24 и authentik с отдельными пользователями (выполняется postgres только при пустом томе)
set -eu

psql -v ON_ERROR_STOP=1 --username postgres \
  -v go24_pw="$GO24_DB_PASSWORD" -v ak_pw="$AUTHENTIK_DB_PASSWORD" <<'SQL'
CREATE USER go24 WITH PASSWORD :'go24_pw';
CREATE DATABASE go24 OWNER go24;
CREATE USER authentik WITH PASSWORD :'ak_pw';
CREATE DATABASE authentik OWNER authentik;
SQL

# boris.gb 2026-10-12 07:16 - база Synapse (Matrix): Synapse требует локаль C
psql -v ON_ERROR_STOP=1 --username postgres -v syn_pw="$SYNAPSE_DB_PASSWORD" <<'SQL'
CREATE USER synapse WITH PASSWORD :'syn_pw';
CREATE DATABASE synapse OWNER synapse ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0;
SQL
