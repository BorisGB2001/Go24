# boris.gb 2026-10-11 13:32 - команды разработки go24; код правим на Mac, собираем и запускаем в Docker на сервере
# boris.gb 2026-10-11 14:33 - отдельный сервер разработки go24 (Debian 13, Docker 29), деплой от пользователя go24 (группа docker)
SERVER     ?= go24@192.168.1.15
REMOTE_DIR ?= /srv/go24
COMPOSE    = docker compose -f deploy/docker-compose.yml --env-file deploy/.env

# boris.gb 2026-10-13 05:52 - на самом сервере разработки (Claude Code на сервере) команды выполняются на месте: без ssh и без rsync
# boris.gb 2026-10-20 02:10 - потоки (docs/streams.md): на сервере make работает в любом рабочем дереве (~/streams/NAME), тесты —
#   в этом дереве (SRC) и на базе потока go24_test_NAME (GO24_STREAM); выкладка (up/build/down) — только из /srv/go24
ON_SERVER := $(shell [ -d $(REMOTE_DIR)/deploy ] && [ "$$(id -un)" = go24 ] && echo 1)
ifeq ($(ON_SERVER),1)
RUN = sh -c
SRC = $(CURDIR)
else
RUN = ssh $(SERVER)
SRC = $(REMOTE_DIR)
endif
TEST_DB  ?= go24_test$(if $(GO24_STREAM),_$(GO24_STREAM))
MAILTEST ?= go24-mailtest$(if $(GO24_STREAM),-$(GO24_STREAM))
S3TEST   ?= go24-s3test$(if $(GO24_STREAM),-$(GO24_STREAM))

.PHONY: deploy-only sync up down ps logs build test test-js test-db test-mail test-storages import-legacy import-legacy-files authentik-users secrets tunnel

## sync: залить код на сервер (без .git и без локальных секретов)
# boris.gb 2026-10-11 15:53 - тема установки (deploy/theme/theme.css, icons/) на сервере не перезаписывается и не удаляется
# boris.gb 2026-10-25 23:55 - и резервные копии deploy/backups (GO24DEV-569)
sync:
ifeq ($(ON_SERVER),1)
	@echo "на сервере: код уже здесь, синхронизация не нужна"
else
	rsync -az --delete --exclude .git --exclude deploy/.env --exclude deploy/rclone/rclone.conf --exclude deploy/backups \
	  --exclude deploy/theme/theme.css --exclude deploy/theme/icons/ --exclude "deploy/locales/*.json" \
	  --exclude deploy/authentik/blueprints/generated/ \
	  --exclude deploy/matrix/homeserver.yaml --exclude deploy/matrix/element-config.json ./ $(SERVER):$(REMOTE_DIR)/
endif

## deploy-only: на сервере выкладывать можно только из /srv/go24 (потоки — через tools/stream.sh deploy)
deploy-only:
ifeq ($(ON_SERVER),1)
	@[ "$(CURDIR)" = "$(REMOTE_DIR)" ] || { echo "выкладка — только из $(REMOTE_DIR): tools/stream.sh deploy"; exit 1; }
endif

## up: залить код, пересобрать и запустить стек на сервере
up: deploy-only sync
	$(RUN) 'cd $(REMOTE_DIR) && $(COMPOSE) up -d --build'

down: deploy-only
	$(RUN) 'cd $(REMOTE_DIR) && $(COMPOSE) down'

ps:
	$(RUN) 'cd $(REMOTE_DIR) && $(COMPOSE) ps'

## logs: make logs s=go24
logs:
	$(RUN) 'cd $(REMOTE_DIR) && $(COMPOSE) logs --tail=100 -f $(s)'

## build: только собрать образ go24 на сервере (проверка компиляции)
build: deploy-only sync
	$(RUN) 'cd $(REMOTE_DIR) && $(COMPOSE) build go24'

## test: go vet + go test в контейнере golang на сервере (кэш модулей в томе go24-gomod)
# boris.gb 2026-10-11 15:20 - проверка кода без Go на Mac
test: sync
	$(RUN) 'cd $(SRC) && docker run --rm -v "$$PWD":/src -v go24-gomod:/go/pkg/mod -w /src golang:1-alpine sh -c "go vet ./... && go test ./..."'

## test-js: сценарии перетаскивания на досках (static/js/boards.js) в jsdom
# boris.gb 2026-10-11 17:03 - Node только в контейнере; jsdom ставится во временный том go24-npm
# boris.gb 2026-10-14 20:19 - добавлен widget.test.mjs (виджет на сайт)
# boris.gb 2026-10-21 23:57 - добавлен drivepick.test.mjs («Файл с Диска» в задаче и ленте, GO24DEV-282)
# boris.gb 2026-10-25 09:59 - добавлен kb.test.mjs (картинки и файлы в страницах базы знаний, GO24DEV-345)
# boris.gb 2026-10-25 14:12 - добавлен kbtree.test.mjs (дерево страниц базы знаний с перетаскиванием, GO24DEV-507)
# boris.gb 2026-10-25 16:14 - добавлен kbedit.test.mjs (база знаний: панель форматирования и предпросмотр, GO24DEV-395)
# boris.gb 2026-10-25 13:19 - добавлен widths.test.mjs (ширина столбцов и панелей перетаскиванием, GO24DEV-477)
# boris.gb 2026-10-25 13:53 - добавлен crmdocsend.test.mjs («Отправить» документ CRM письмом, GO24DEV-357)
# boris.gb 2026-10-25 14:40 - добавлен imshare.test.mjs (меню «…» сообщения: реакции, «по почте», «Поделиться картинкой», GO24DEV-508)
# boris.gb 2026-10-25 14:04 - добавлен imthreads.test.mjs (непрочитанные ответы в тредах, GO24DEV-497)
# boris.gb 2026-10-25 11:31 - добавлен imquote.test.mjs («Цитировать» и «Сохранить на Диск» в меню сообщения, GO24DEV-353)
# boris.gb 2026-10-25 11:40 - добавлен booking.test.mjs (диалог записи клиента в «Онлайн-записи», GO24DEV-432)
# boris.gb 2026-10-25 00:29 - добавлен imkeys.test.mjs (↑, двойной клик, «Прочитать всё» в мессенджере, GO24DEV-323)
# boris.gb 2026-10-21 03:19 - добавлен call.test.mjs (страница звонка: участники, устройства, раскладка)
# boris.gb 2026-10-21 19:07 - добавлен presence.test.mjs (статус «в сети» и «Не беспокоить», GO24DEV-276)
# boris.gb 2026-10-22 10:22 - добавлен immention.test.mjs (упоминания «@» в мессенджере, GO24DEV-314)
# boris.gb 2026-10-24 23:13 - добавлен imlink.test.mjs (предпросмотр ссылок в мессенджере, GO24DEV-320)
# boris.gb 2026-10-21 02:59 - добавлен ring.test.mjs (входящий звонок «Звонит …»)
# boris.gb 2026-10-25 16:28 - добавлен phone.test.mjs (софтфон со страниц: tel:-ссылки, входящий звонок, GO24DEV-502)
# boris.gb 2026-10-21 02:34 - добавлен imrooms.test.mjs (закрепить, без звука, непрочитанный в списке чатов)
# boris.gb 2026-10-21 02:24 - добавлен imread.test.mjs (прочтение и «печатает…» в мессенджере)
# boris.gb 2026-10-21 02:14 - добавлен impins.test.mjs (закреплённые сообщения и поиск в мессенджере)
# boris.gb 2026-10-21 01:41 - добавлен immenu.test.mjs (меню сообщения мессенджера)
# boris.gb 2026-10-20 21:00 - добавлен imfiles.test.mjs (файлы в мессенджере)
# boris.gb 2026-10-13 09:38 - добавлен im.test.mjs (счётчики непрочитанных мессенджера)
# boris.gb 2026-10-17 23:48 - добавлен people.test.mjs (выбор сотрудника с поиском, GO24DEV-131)
# boris.gb 2026-10-18 16:21 - добавлен mail.test.mjs (почта: область чтения, перетаскивание в папки, GO24DEV-134)
# boris.gb 2026-10-18 21:04 - добавлен collab.test.mjs (вкладка «Коллабы» в мессенджере, GO24DEV-142)
# boris.gb 2026-10-19 21:35 - добавлен guest.test.mjs (выбор людей у гостя коллаба: без «Отделов», метка «гость», GO24DEV-182)
# boris.gb 2026-10-18 22:32 - добавлен compose.test.mjs (почта: редактор письма, адресаты, черновик, отмена отправки, GO24DEV-136)
# boris.gb 2026-10-20 16:20 - добавлен mailcrm.test.mjs (почта: поиск карточки CRM для привязки письма, GO24DEV-139)
# boris.gb 2026-10-20 16:01 - добавлен mailx.test.mjs (почта: новые письма, просмотр вложений, «С Диска», GO24DEV-140)
# boris.gb 2026-10-20 20:59 - добавлен calendar.test.mjs (календарь: планировщик занятости во встрече, GO24DEV-233)
# boris.gb 2026-10-21 01:41 - добавлен calmove.test.mjs (календарь: перетаскивание встреч, GO24DEV-235)
# boris.gb 2026-10-20 21:03 - добавлен disk.test.mjs (Диск: выбор, перемещение, загрузка перетаскиванием, GO24DEV-225)
# boris.gb 2026-10-21 06:48 - добавлен imsaved.test.mjs (мессенджер: «Избранное» и «Напомнить о сообщении», GO24DEV-259)
# boris.gb 2026-10-21 07:05 - добавлен impoll.test.mjs (мессенджер: опросы в чате, GO24DEV-260)
# boris.gb 2026-10-21 07:27 - добавлен imemoji.test.mjs (мессенджер: смайлы в поле ввода, GO24DEV-262)
# boris.gb 2026-10-21 18:27 - добавлен immedia.test.mjs (мессенджер: медиа, файлы и ссылки чата, GO24DEV-266)
# boris.gb 2026-10-21 18:30 - добавлен mailquick.test.mjs (почта: форма «Быстрых действий», GO24DEV-268)
# boris.gb 2026-10-22 09:52 - добавлен imdraft.test.mjs (мессенджер: черновики чата и треда, GO24DEV-305)
# boris.gb 2026-10-25 06:48 - добавлен crmrequired.test.mjs (CRM: окно «Заполните обязательные поля» при смене стадии, GO24DEV-330)
# boris.gb 2026-10-25 08:18 - добавлен imcalls.test.mjs (мессенджер: вкладки «Каналы» и «Звонки», GO24DEV-332)
# boris.gb 2026-10-25 13:42 - добавлен openlines.test.mjs (открытые линии: быстрые ответы по «/», живое обновление, GO24DEV-376)
# boris.gb 2026-10-25 16:30 - добавлен imview.test.mjs (просмотр файлов чата, GO24DEV-526)
# boris.gb 2026-10-25 18:11 - добавлен structure.test.mjs (структура компании: поиск и перетаскивание, GO24DEV-420)
# boris.gb 2026-10-25 19:09 - добавлен crmcols.test.mjs («Настроить колонки» в списках CRM, GO24DEV-532)
# boris.gb 2026-10-25 22:59 - добавлен pop.test.mjs (одно всплывающее окно за раз, щелчок вне и Esc закрывают, GO24DEV-527)
test-js: sync
	$(RUN) 'cd $(SRC) && docker run --rm -v "$$PWD":/src:ro -v go24-npm:/deps -w /deps node:22-alpine \
	  sh -c "test -d node_modules/jsdom || npm install --silent --no-audit --no-fund jsdom >/dev/null; cp -r /src/test /src/internal /tmp/ && ln -s /deps/node_modules /tmp/node_modules && node /tmp/test/js/boards.test.mjs && node /tmp/test/js/task.test.mjs && node /tmp/test/js/list.test.mjs && node /tmp/test/js/feed.test.mjs && node /tmp/test/js/bell.test.mjs && node /tmp/test/js/presence.test.mjs && node /tmp/test/js/mention.test.mjs && node /tmp/test/js/forms.test.mjs && node /tmp/test/js/people.test.mjs && node /tmp/test/js/mail.test.mjs && node /tmp/test/js/im.test.mjs && node /tmp/test/js/imfiles.test.mjs && node /tmp/test/js/immenu.test.mjs && node /tmp/test/js/impins.test.mjs && node /tmp/test/js/imsaved.test.mjs && node /tmp/test/js/immedia.test.mjs && node /tmp/test/js/impoll.test.mjs && node /tmp/test/js/imemoji.test.mjs && node /tmp/test/js/imread.test.mjs && node /tmp/test/js/imrooms.test.mjs && node /tmp/test/js/imdraft.test.mjs && node /tmp/test/js/immention.test.mjs && node /tmp/test/js/imlink.test.mjs && node /tmp/test/js/imkeys.test.mjs && node /tmp/test/js/ring.test.mjs && node /tmp/test/js/phone.test.mjs && node /tmp/test/js/call.test.mjs && node /tmp/test/js/callguests.test.mjs && node /tmp/test/js/callrecord.test.mjs && node /tmp/test/js/callext.test.mjs && node /tmp/test/js/collab.test.mjs && node /tmp/test/js/imcalls.test.mjs && node /tmp/test/js/imsettings.test.mjs && node /tmp/test/js/imquote.test.mjs && node /tmp/test/js/imthreads.test.mjs && node /tmp/test/js/imshare.test.mjs && node /tmp/test/js/imview.test.mjs && node /tmp/test/js/guest.test.mjs && node /tmp/test/js/compose.test.mjs && node /tmp/test/js/widget.test.mjs && node /tmp/test/js/mailx.test.mjs && node /tmp/test/js/mailcrm.test.mjs && node /tmp/test/js/calendar.test.mjs && node /tmp/test/js/disk.test.mjs && node /tmp/test/js/drivepick.test.mjs && node /tmp/test/js/calmove.test.mjs && node /tmp/test/js/mailquick.test.mjs && node /tmp/test/js/crmrequired.test.mjs && node /tmp/test/js/kb.test.mjs && node /tmp/test/js/kbtree.test.mjs && node /tmp/test/js/kbedit.test.mjs && node /tmp/test/js/widths.test.mjs && node /tmp/test/js/crmdocsend.test.mjs && node /tmp/test/js/crmcols.test.mjs && node /tmp/test/js/openlines.test.mjs && node /tmp/test/js/booking.test.mjs && node /tmp/test/js/structure.test.mjs && node /tmp/test/js/pop.test.mjs"'

## test-db: все тесты, включая интеграционные, на отдельной базе go24_test (рабочую базу go24 не трогает);
##          -p 1 — пакеты по очереди, т.к. тесты разных пакетов пересоздают одну и ту же базу
# boris.gb 2026-10-11 15:22 - база go24_test пересоздаётся тестом; пароль суперпользователя берётся из deploy/.env на сервере
# boris.gb 2026-10-20 20:41 - временное S3 (rclone serve s3, пустая папка, удаляется после теста) — файлы письма копируются в задачу (GO24DEV-203)
test-db: sync
	$(RUN) 'cd $(SRC) && . $(REMOTE_DIR)/deploy/.env && \
	  docker exec go24-postgres-1 psql -U postgres -tc "SELECT 1 FROM pg_database WHERE datname = '"'"'$(TEST_DB)'"'"'" | grep -q 1 || \
	  docker exec go24-postgres-1 createdb -U postgres $(TEST_DB); \
	  docker rm -f $(S3TEST) >/dev/null 2>&1; \
	  docker run -d --rm --name $(S3TEST) --network go24_default rclone/rclone serve s3 /data --addr :8333 --auth-key go24testkey,go24testsecret >/dev/null; \
	  docker run --rm --network go24_default -v "$$PWD":/src -v go24-gomod:/go/pkg/mod -w /src \
	    -e GO24_TEST_DATABASE_URL="postgres://postgres:$$PG_SUPERUSER_PASSWORD@postgres:5432/$(TEST_DB)?sslmode=disable" \
	    -e GO24_TEST_S3=$(S3TEST):8333 \
	    golang:1-alpine go test -p 1 -count=1 ./...; status=$$?; docker rm -f $(S3TEST) >/dev/null; exit $$status'

## test-storages: сквозной тест «Хранилищ» через настоящий rclone rcd и временный WebDAV-сервер
# boris.gb 2026-10-11 16:31 - временный WebDAV (rclone serve webdav) живёт только на время теста в сети go24_storages
test-storages: sync
	$(RUN) 'cd $(SRC) && . $(REMOTE_DIR)/deploy/.env && \
	  docker run -d --rm --name go24-test-webdav --network go24_storages rclone/rclone serve webdav /data --addr :8081 --user t --pass tp >/dev/null && \
	  C=$$(docker create --rm --network go24_default -v "$$PWD":/src -v go24-gomod:/go/pkg/mod -w /src \
	    -e GO24_TEST_DATABASE_URL="postgres://postgres:$$PG_SUPERUSER_PASSWORD@postgres:5432/$(TEST_DB)?sslmode=disable" \
	    -e GO24_TEST_RCLONE_RC_URL=http://rclone-rcd:5572 -e GO24_TEST_RCLONE_RC_PASS="$$RCLONE_RC_PASS" \
	    -e GO24_TEST_WEBDAV_URL=http://go24-test-webdav:8081/ \
	    golang:1-alpine go test -count=1 -v -run TestRcloneEndToEnd ./internal/storages/) && \
	  docker network connect go24_storages $$C && docker start -a $$C; rc=$$?; docker stop go24-test-webdav >/dev/null; exit $$rc'

## import-legacy: импорт задач из выгрузки корпоративного портала в /srv/go24-import (см. docs/import-legacy.md), с обезличиванием
# boris.gb 2026-10-11 16:04 - seed обезличивания создаётся один раз и хранится в /srv/go24-import/seed
import-legacy: sync
	$(RUN) 'cd $(REMOTE_DIR) && . deploy/.env && \
	  test -f /srv/go24-import/seed || (openssl rand -hex 16 > /srv/go24-import/seed && chmod 600 /srv/go24-import/seed); \
	  docker run --rm --network go24_default -v "$$PWD":/src -v go24-gomod:/go/pkg/mod -v /srv/go24-import:/import:ro -w /src \
	    -e GO24_DATABASE_URL="postgres://go24:$$GO24_DB_PASSWORD@postgres:5432/go24?sslmode=disable" \
	    golang:1-alpine go run ./cmd/go24-import-legacy -dir /import -project EXT -seed "$$(cat /srv/go24-import/seed)" \
	      -authentik-blueprint /src/deploy/authentik/blueprints/generated/legacy-users.yaml'

## import-portal: сотрудники (и отделы) с портала по вебхуку из связки ключей Mac, с искажением всех личных данных
# boris.gb 2026-10-12 06:51 - вебхук передаётся через stdin (не в аргументах и не в env), на сервере не сохраняется
IMPORT_PORTAL_WHAT ?= users
import-portal: sync
	security find-generic-password -s go24-portal-webhook -w | $(RUN) 'cd $(REMOTE_DIR) && . deploy/.env && \
	  docker run --rm -i --network go24_default -v "$$PWD":/src -v go24-gomod:/go/pkg/mod -v /srv/go24-import:/import:ro -w /src \
	    -e GO24_DATABASE_URL="postgres://go24:$$GO24_DB_PASSWORD@postgres:5432/go24?sslmode=disable" \
	    golang:1-alpine go run ./cmd/go24-import-portal -portal acme -seed-file /import/seed -what $(IMPORT_PORTAL_WHAT)'

## import-legacy-files: вложения из корпоративного портала (files.tsv + users.tsv в /srv/go24-import) — пустые, только имя и тип
# boris.gb 2026-10-11 16:51 - содержимое документов не переносится: обезличить его нельзя (решение заказчика)
import-legacy-files: sync
	$(RUN) 'cd $(REMOTE_DIR) && . deploy/.env && \
	  docker run --rm --network go24_default -v "$$PWD":/src -v go24-gomod:/go/pkg/mod -v /srv/go24-import:/import:ro -w /src \
	    -e GO24_DATABASE_URL="postgres://go24:$$GO24_DB_PASSWORD@postgres:5432/go24?sslmode=disable" \
	    golang:1-alpine go run ./cmd/go24-import-legacy -files -dir /import -seed "$$(cat /srv/go24-import/seed)"'

## authentik-users: создать в Authentik обезличенных пользователей из базы go24 (импорт из корпоративного портала), без выгрузки
authentik-users: sync
	$(RUN) 'cd $(REMOTE_DIR) && . deploy/.env && \
	  docker run --rm --network go24_default -v "$$PWD":/src -v go24-gomod:/go/pkg/mod -w /src \
	    -e GO24_DATABASE_URL="postgres://go24:$$GO24_DB_PASSWORD@postgres:5432/go24?sslmode=disable" \
	    golang:1-alpine go run ./cmd/go24-import-legacy -only-blueprint \
	      -authentik-blueprint /src/deploy/authentik/blueprints/generated/legacy-users.yaml'

## tunnel: проброс портов go24 (8080) и Authentik (9000) с сервера на Mac
# boris.gb 2026-10-11 13:42 - замена NetBird на время разработки
tunnel:
	ssh -N -L 8080:127.0.0.1:8080 -L 9000:127.0.0.1:9000 $(SERVER)

## matrix-config: собрать deploy/matrix/homeserver.yaml и element-config.json из шаблонов и deploy/.env (на сервере)
# boris.gb 2026-10-12 07:16 - секреты Synapse живут только в deploy/.env и в сгенерированном файле на сервере, в git их нет
# boris.gb 2026-10-12 07:20 - homeserver.yaml пишется контейнером с владельцем synapse (uid 991) и правами 600
MATRIX_VARS = \$$MATRIX_SERVER_NAME \$$MATRIX_PUBLIC_URL \$$MATRIX_ELEMENT_URL \$$SYNAPSE_DB_PASSWORD \$$SYNAPSE_REGISTRATION_SECRET \
  \$$SYNAPSE_MACAROON_SECRET \$$SYNAPSE_FORM_SECRET \$$GO24_OIDC_ISSUER_BASE \$$MATRIX_OIDC_CLIENT_ID \$$MATRIX_OIDC_CLIENT_SECRET \$$MATRIX_OIDC_SKIP_VERIFICATION
matrix-config: sync
	$(RUN) 'cd $(REMOTE_DIR)/deploy && set -a && . ./.env && set +a && \
	  envsubst "$(MATRIX_VARS)" < matrix/homeserver.yaml.tmpl | docker run --rm -i -v "$$PWD/matrix":/m alpine:3.20 \
	    sh -c "cat > /m/homeserver.yaml && chown 991:991 /m/homeserver.yaml && chmod 600 /m/homeserver.yaml" && \
	  envsubst "$(MATRIX_VARS)" < matrix/element-config.json.tmpl > matrix/element-config.json && echo "matrix: конфиги собраны"'

## matrix-db: база synapse в уже работающем postgres (на новом томе её создаёт init-databases.sh)
matrix-db: sync
	$(RUN) 'cd $(REMOTE_DIR)/deploy && . ./.env && docker exec -i -e SYNAPSE_DB_PASSWORD="$$SYNAPSE_DB_PASSWORD" go24-postgres-1 sh < postgres/matrix-db.sh'

## matrix-bot: создать бота go24 (администратор Matrix) и записать его токен в deploy/.env — один раз
# boris.gb 2026-10-12 07:32 - токен бота не выводится на экран: сразу в .env на сервере
matrix-bot: sync
	$(RUN) 'cd $(REMOTE_DIR) && grep -q "^GO24_MATRIX_TOKEN=." deploy/.env && echo "бот уже есть" || { set -a && . deploy/.env && set +a && \
	  tok=$$(docker run --rm --network go24_default -v "$$PWD":/src -v go24-gomod:/go/pkg/mod -w /src -e MATRIX_URL=http://synapse:8008 \
	    -e SYNAPSE_REGISTRATION_SECRET golang:1-alpine go run ./cmd/go24-matrix-bot) && test -n "$$tok" && \
	  printf "%s\n%s\n" "# boris.gb 2026-10-12 07:32 - токен бота go24 в Matrix (make matrix-bot)" "GO24_MATRIX_TOKEN=$$tok" >> deploy/.env && echo "бот создан"; }'

## dev-ca: скачать корневой сертификат dev-центра Caddy на Mac (добавить в доверенные — вручную, см. вывод)
# boris.gb 2026-10-12 07:30 - без доверия к этому сертификату браузер предупреждает о https Element/Synapse
dev-ca:
	$(RUN) 'docker exec go24-caddy-1 cat /data/caddy/pki/authorities/local/root.crt' > deploy/go24-dev-ca.crt
	@echo "Сертификат: deploy/go24-dev-ca.crt — добавьте его в доверенные (Связка ключей → Система → Всегда доверять)"

## secrets: сгенерировать пароли в deploy/.env на сервере, если его ещё нет
# boris.gb 2026-10-25 14:10 - пароль ARI Asterisk (телефония, GO24DEV-501)
secrets:
	$(RUN) 'cd $(REMOTE_DIR)/deploy && test ! -f .env && cp .env.example .env && \
	  for k in PG_SUPERUSER_PASSWORD GO24_DB_PASSWORD AUTHENTIK_DB_PASSWORD S3_ACCESS_KEY S3_SECRET_KEY GO24_OIDC_CLIENT_ID SYNAPSE_DB_PASSWORD MATRIX_OIDC_CLIENT_ID; do \
	    sed -i "s|^$$k=$$|$$k=$$(openssl rand -hex 16)|" .env; done && \
	  for k in AUTHENTIK_SECRET_KEY GO24_SESSION_SECRET GO24_OIDC_CLIENT_SECRET RCLONE_RC_PASS GO24_AUTHENTIK_TOKEN SYNAPSE_REGISTRATION_SECRET SYNAPSE_MACAROON_SECRET SYNAPSE_FORM_SECRET MATRIX_OIDC_CLIENT_SECRET ONLYOFFICE_JWT_SECRET ASTERISK_SECRET; do \
	    sed -i "s|^$$k=$$|$$k=$$(openssl rand -hex 32)|" .env; done && \
	  chmod 600 .env && echo "deploy/.env создан" || echo "deploy/.env уже есть, не трогаю"'

## test-mail: почта go24 против временного почтового сервера GreenMail (IMAP 3143, SMTP 3025, без TLS) и базы go24_test
# boris.gb 2026-10-13 10:56 - GreenMail поднимается только на время теста и удаляется; рабочие ящики не трогаются
test-mail: sync
	$(RUN) 'cd $(SRC) && . $(REMOTE_DIR)/deploy/.env && \
	  docker rm -f $(MAILTEST) >/dev/null 2>&1; \
	  docker run -d --name $(MAILTEST) --network go24_default -e GREENMAIL_OPTS="-Dgreenmail.setup.test.smtp -Dgreenmail.setup.test.imap -Dgreenmail.hostname=0.0.0.0 -Dgreenmail.auth.disabled" greenmail/standalone:2.0.1 >/dev/null && \
	  sleep 8; docker run --rm --network go24_default -v "$$PWD":/src -v go24-gomod:/go/pkg/mod -w /src \
	    -e GO24_TEST_MAIL_HOST=$(MAILTEST) \
	    -e GO24_TEST_DATABASE_URL="postgres://postgres:$$PG_SUPERUSER_PASSWORD@postgres:5432/$(TEST_DB)?sslmode=disable" \
	    golang:1-alpine go test -count=1 ./internal/mail; status=$$?; docker rm -f $(MAILTEST) >/dev/null; exit $$status'
