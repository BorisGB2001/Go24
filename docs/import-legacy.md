# Импорт задач из корпоративного портала (коробка)

Импорт переносит задачи, подзадачи, соисполнителей, наблюдателей и комментарии из MySQL коробочного корпоративного портала в проект go24. Личные данные обезличиваются:

| Что | Как |
|---|---|
| ФИО сотрудников | Случайные ФИО того же пола, одинаковые для человека везде. Подменяются и в тексте, со склонениями: «Иванову» → «Белову» |
| Упоминания `[USER=17]…[/USER]` | `@Псевдоним` этого пользователя |
| Телефоны | Случайные цифры с сохранением формата (`+7 (9xx) …`, `8-9xx-…`, городские `xx-xx-xx`) |
| Почта | Сотрудники — `user<ID>@portal.import`; прочие адреса — `contact<хеш>@<тот же домен>` |
| Комментарии без автора (письма, гости) | Общий пользователь «Внешний автор», имя отправителя не переносится |
| Компании, тексты задач | Остаются как есть |

**Ограничение.** Имена внешних людей (клиентов), которых нет среди пользователей корпоративного портала, в текстах не распознаются и не заменяются. Телефоны и почта заменяются у всех.

Номер задачи сохраняется: задача 123 из корпоративного портала становится `EXT-123`. Импорт идёт одной транзакцией и запускается повторно без дублей: записи сопоставляются по `legacy_id`. Служебные сообщения форума корпоративного портала (`SERVICE_TYPE`) не переносятся.

## Порядок

1. **Выгрузка на сервере корпоративного портала** (MySQL, база `sitemanager`). Тексты кодируются в base64, папка доступна только root:

```bash
D=/root/go24-export; install -d -m 700 $D; cd $D
B64='REPLACE(TO_BASE64(IFNULL(%s,"")),"\n","")'   # шаблон для текстовых колонок
mysql sitemanager -N -B -e 'select ID, <base64 NAME>, <base64 LAST_NAME>, <base64 SECOND_NAME>, IFNULL(PERSONAL_GENDER,""), <base64 EMAIL>, <base64 LOGIN> from b_user where ID in (select USER_ID from b_tasks_member union select AUTHOR_ID from b_forum_message where XML_ID like "TASK_%" and SERVICE_TYPE is null)' > users.tsv
mysql sitemanager -N -B -e 'select ID, <base64 TITLE>, <base64 DESCRIPTION>, STATUS, PRIORITY, CREATED_BY, RESPONSIBLE_ID, IFNULL(PARENT_ID,0), TASK_CONTROL, IFNULL(DEADLINE,""), IFNULL(START_DATE_PLAN,""), IFNULL(END_DATE_PLAN,""), CREATED_DATE, IFNULL(CHANGED_DATE,""), IFNULL(CLOSED_DATE,""), IFNULL(CLOSED_BY,0) from b_tasks where ZOMBIE="N" order by ID' > tasks.tsv
mysql sitemanager -N -B -e 'select TASK_ID, USER_ID, TYPE from b_tasks_member' > members.tsv
mysql sitemanager -N -B -e 'select m.ID, cast(substring(m.XML_ID,6) as unsigned), m.AUTHOR_ID, m.POST_DATE, <base64 POST_MESSAGE> from b_forum_message m where m.XML_ID like "TASK_%" and m.SERVICE_TYPE is null order by m.ID' > comments.tsv
chmod 600 *.tsv
```

`<base64 X>` означает `REPLACE(TO_BASE64(IFNULL(X,"")),"\n","")`.

2. **Перенос** файлов в `/srv/go24-import/` на сервер go24 (права 700, вне кода — `make sync` эту папку не трогает).
3. **Импорт**: `make import-legacy`. При первом запуске в `/srv/go24-import/seed` создаётся случайный seed обезличивания. Сохраните его: с ним повторный импорт даст те же псевдонимы.
4. **Удалите исходные выгрузки** везде (`users.tsv`, `tasks.tsv`, `members.tsv`, `comments.tsv`), оставьте только `seed`.

Проект `EXT` создаётся с владельцем — первым пользователем, вошедшим в go24 через Authentik (или `-owner <email>`). Все импортированные сотрудники добавляются участниками. Войти под импортированными пользователями нельзя: у них `oidc_sub = legacy:<ID>`.

## Пользователи в Authentik и первый вход

Импортированные сотрудники создаются и в Authentik — командой `make authentik-users`. Она пишет blueprint `deploy/authentik/blueprints/generated/legacy-users.yaml` из базы go24 (только псевдонимы), Authentik применяет его сам. Пользователи лежат в разделе `go24/portal`, их логины — `portal-user<ID>`, адреса — `user<ID>@portal.import`. Пароля у них нет: администратор задаёт его в Authentik или отправляет ссылку восстановления.

При первом входе go24 находит импортированную запись с той же почтой и привязывает её к учётной записи Authentik. Все задачи и комментарии этого сотрудника становятся его, новый пользователь не создаётся. Когда сотрудникам выдадут настоящие учётные записи, достаточно указать в Authentik их настоящую почту и одновременно поменять её в go24 — или войти под псевдонимом и сменить данные в Authentik.

## Вложения

`make import-legacy-files` переносит вложения задач и комментариев **пустыми**: сохраняются обезличенное имя, тип и привязка к задаче, содержимое не копируется. Причина: документы (таблицы, PDF, сканы) обезличить нельзя. Вложения из комментариев прикрепляются к задаче. Для пустых файлов объект в хранилище не создаётся, скачивание отдаёт пустой файл.

Выгрузка — `users.tsv` (как выше) и `files.tsv`:

```
ATTACHED_ID, TASK_ID, <base64 NAME>, CONTENT_TYPE, CREATED_BY, task|comment, CREATE_TIME
```

Строки берутся из `b_utm_tasks_task` (поле `UF_TASK_WEBDAV_FILES`) → `b_disk_attached_object` → `b_disk_object`, а для комментариев — из `b_disk_attached_object` с `ENTITY_TYPE` `ForumMessageConnector` → `b_forum_message.XML_ID = TASK_<id>`.
