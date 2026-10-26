# Разбор API: корпоративный портал, OpenProject, Jira, Confluence

Цель: спроектировать таск-трекер go24, максимально близкий к задачам корпоративного портала и к ведению проектов в OpenProject. Также заложить импорт данных из этих систем и совместимость с ними.

Источники проверены 11.10.2026: openproject.org/docs/api, developer.atlassian.com. Где страницы обрезались, сведения дополнены известным устройством API, такие места помечены *(уточнить)*.

## 1. корпоративный портал — задачи и проекты

**Протокол:** REST поверх HTTP, вызов `https://<портал>/rest/<метод>`. Авторизация — входящий вебхук (`/rest/<user>/<token>/`) или OAuth 2.0 приложения. Старые методы используют scope `task`, новые методы REST 3.0 — scope `tasks`. Списки отдаются по 50 записей, следующая страница запрашивается через `start`.

| Сущность | Методы |
|---|---|
| Задача | `tasks.task.add/update/get/list/delete`, `getFields`, `getaccess`, `history.list`, `counters.get`; в REST 3.0 те же методы и `access.*`, `field.*` |
| Смена статуса | `tasks.task.start/pause/defer/complete/renew`, `delegate`, `approve/disapprove` (контроль постановщика) |
| Личное | `startwatch/stopwatch`, `favorite.add/remove`, `pin/unpin`, `mute/unmute` |
| Результат задачи | `tasks.task.result.add/addFromComment/list/update/delete` |
| Чек-листы | `task.checklistitem.add/update/get/getlist/moveafteritem/complete/renew/delete` |
| Комментарии | `task.commentitem.*` (в новой карточке задачи комментарии заменены чатом: `tasks.task.chat.message.send`) |
| Учёт времени | `task.elapseditem.add/update/get/getlist/delete` |
| Связи для Ганта | `task.dependence.add/delete`, `tasks.task.gantt.link.list` |
| Канбан / «Мой план» | `task.stages.add/update/get/movetask/canmovetask/delete` |
| Пользовательские поля | `task.item.userfield.*` |
| Потоки | `tasks.flow.Flow.create/get/update/delete/activate` |
| Группы и проекты | `sonet_group.create/update/get/delete`, `socialnetwork.api.workgroup.list/get`, `sonet_group.user.add/update/delete/get` (роли участников), `sonet_group.setowner` |
| Скрам | Группа со `SCRUM_MASTER_ID`; методы `tasks.api.scrum.*`: бэклог, спринты, эпики, канбан спринта *(уточнить)* |
| События | `OnTaskAdd/OnTaskUpdate/OnTaskDelete`, `onSonetGroupAdd/Update/Delete`, исходящие вебхуки |

**Модель задачи:**
- **Люди:** постановщик `CREATED_BY`, исполнитель `RESPONSIBLE_ID` (ровно один), соисполнители `ACCOMPLICES[]`, наблюдатели `AUDITORS[]`.
- **Связи:** `GROUP_ID` (группа или проект), `PARENT_ID` (подзадачи), `DEADLINE`, план `START_DATE_PLAN/END_DATE_PLAN`, `PRIORITY` (0/1/2 — низкий, средний, высокий).
- **Статусы:** 2 — ждёт выполнения, 3 — выполняется, 4 — ждёт контроля, 5 — завершена, 6 — отложена.
- **Флаги:** `TASK_CONTROL` (принять работу), `ALLOW_CHANGE_DEADLINE`, `ALLOW_TIME_TRACKING`, `TIME_ESTIMATE`, `TAGS`.
- **Привязки:** `UF_CRM_TASK` (`C_3`, `CO_5` …), `UF_TASK_WEBDAV_FILES` (файлы Диска), `UF_MAIL_MESSAGE`.
- **Стадии канбана** привязаны к группе, к «Моему плану» или к спринту отдельно от статуса задачи.

**Что взять в go24:** роли участников задачи (постановщик, исполнитель, соисполнители, наблюдатели), контроль результата и «Результат задачи», чек-листы, учёт времени, канбан-стадии независимо от статуса, «Мой план», счётчики («просрочено», «ждёт контроля», «новые комментарии»).

## 2. OpenProject — проекты и рабочие пакеты (API v3)

**Протокол:** REST, HAL+JSON (`application/hal+json`). Связи передаются в `_links`, вложенные объекты — в `_embedded`, коллекции имеют `_type: Collection` и поля `total`, `count`, `pageSize`, `offset`. Авторизация — API-ключ (Basic `apikey:<key>`) или OAuth 2.0. Эндпоинты внутри проекта объявлены устаревшими, вместо них теперь `/api/v3/workspaces/{id}/...`.

| Сущность | Эндпоинты |
|---|---|
| Рабочие пакеты | `GET/POST /api/v3/work_packages`, `GET/PATCH/DELETE /work_packages/{id}`, `/form` (проверка до сохранения), `/schemas` |
| Комментарии и история | `GET/POST /work_packages/{id}/activities` |
| Связи | `POST /api/v3/relations`. Типы: relates, duplicates, blocks, precedes/follows (с лагом), includes, requires |
| Наблюдатели | `/work_packages/{id}/watchers`, `available_watchers` |
| Вложения | `/attachments`, прямая загрузка, файловые ссылки на Nextcloud/OneDrive |
| Учёт времени | `/api/v3/time_entries` (+ виды деятельности) |
| Справочники | `/types`, `/statuses`, `/priorities`, `/categories`, `/versions`, `/roles`, `/memberships` |
| Проекты | `/api/v3/projects` (иерархия через parent), статусы, участники с ролями |
| Сохранённые запросы | `/api/v3/queries`: фильтры, колонки, группировка, суммы — это «виды» (таблица, Гант, доска) |
| Прочее | `watch/unwatch`, `copy`, `move`, `customActions`, напоминания, `previewMarkup` |

**Модель рабочего пакета:**
- **Поля:** `subject`, `description` (`format`, `raw`, `html`), `startDate`/`dueDate`/`date` (вехи), `scheduleManually`, `estimatedTime`/`remainingTime`/`spentTime`, `percentageDone`, `storyPoints`, `lockVersion` (оптимистическая блокировка), вычисляемые от потомков `derived*`.
- **Связи (`_links`):** `type`, `status`, `priority`, `project` — обязательные; `assignee`, `responsible` (ответственный — отдельная роль), `version`, `parent`, `children`, `category`, `sprint`, `projectPhase`, `relations`, `watchers`, `attachments`, `timeEntries`, `customFieldN`.
- **Фильтры:** JSON-массив вида `[{"status_id":{"operator":"o","values":null}}]`. Сортировка: `sortBy=[["id","asc"]]`. Также `groupBy`, `showSums`, `select`.

**Что взять в go24:**
- типы рабочих пакетов (задача, веха, фаза, эпик, баг) с настраиваемым набором полей;
- workflow статусов по ролям и типам;
- связи с планированием, где precedes/follows сдвигают даты, и диаграмма Ганта;
- версии и спринты, иерархия проектов, роли и права на уровне проекта;
- сохранённые запросы как основа всех представлений;
- `lockVersion` и `/form`-валидация; вычисляемые значения родителя.

## 3. Jira Cloud (REST v3 + Agile)

**Протокол:** REST, `/rest/api/3/...`. Текст описаний и комментариев хранится в **ADF** (Atlassian Document Format, JSON-дерево). Авторизация — Basic (email + API token) или OAuth 2.0 (3LO). Поиск по JQL: `GET/POST /rest/api/3/search/jql` с постраничной выдачей через `nextPageToken`; старый `/rest/api/3/search` выведен из эксплуатации *(уточнить дату)*.

| Сущность | Эндпоинты |
|---|---|
| Задачи | `POST /issue`, `GET/PUT/DELETE /issue/{key}`, `POST /issue/bulk`, `GET /issue/createmeta/{project}/issuetypes` |
| Переходы по workflow | `GET/POST /issue/{key}/transitions` |
| Комментарии, учёт времени | `/issue/{key}/comment`, `/issue/{key}/worklog` |
| Связи | `POST /issueLink`, `/issueLinkType` |
| Вложения, наблюдатели | `/issue/{key}/attachments`, `/issue/{key}/watchers` |
| Проекты и схемы | `/project`, `/issuetype`, `/priority`, `/status`, `/workflow/search`, `/field` (кастомные поля) |
| Agile | `/rest/agile/1.0/board`, `/board/{id}/sprint`, `/sprint/{id}/issue`, `/epic`, `/backlog/issue` |
| События | Вебхуки `jira:issue_created/updated/deleted`, `comment_*`, `sprint_*` |

**Что взять в go24:**
- человекочитаемые ключи задач `PROJ-123`;
- язык запросов, как JQL (свой упрощённый «GQL»);
- workflow как набор переходов с условиями и валидаторами;
- доски Scrum и Kanban, бэклог, эпики;
- курсорная пагинация и массовые операции.

## 4. Confluence Cloud (REST v2) — база знаний

**Протокол:** REST, `/wiki/api/v2/...`. Курсорная пагинация: `limit` + `cursor`, ссылка на следующую страницу — в `Link: rel="next"` и `_links.next`. Авторизация — Basic (API token) или OAuth 2.0. Поиск по CQL остаётся в v1: `/wiki/rest/api/search?cql=...`.

| Сущность | Эндпоинты |
|---|---|
| Пространства | `/spaces`, права и свойства пространств |
| Страницы | `GET/POST /pages`, `GET/PUT/DELETE /pages/{id}`, `PUT /pages/{id}/title`, `/spaces/{id}/pages`, `/labels/{id}/pages` |
| Дерево | `/pages/{id}/children`, `/ancestors`, `/descendants`, папки (`/folders`) |
| История | `/pages/{id}/versions`; при обновлении нужно передать `version.number` (+1) |
| Прочее | блог-посты, комментарии (footer/inline), вложения, метки, лайки, задачи на страницах, whiteboards, databases |

**Формат тела:** `body-format=storage` (XHTML) или `atlas_doc_format` (ADF). Новая страница по умолчанию публикуется, черновик задаётся через `status: draft`. Страница хранит `spaceId`, `parentId` и `parentType`.

**Что взять в go24:**
- модуль «Знания»: пространства, дерево страниц и версионирование;
- ссылки «задача ↔ страница»;
- формат документа на JSON-дереве, как ADF или ProseMirror, общий для описаний задач, комментариев и страниц.

## 5. Сводная модель go24

| Понятие go24 | корпоративный портал | OpenProject | Jira | Confluence |
|---|---|---|---|---|
| Пространство (workspace) | портал | экземпляр | site | site |
| Проект (иерархия) | группа или проект (`sonet_group`) | project | project | space |
| Задача (тип, ключ `ПРЕФИКС-N`) | task | work package | issue | — |
| Тип задачи | — | type | issue type | — |
| Статус + workflow | 5 фиксированных статусов | status + workflow по ролям | status + transitions | — |
| Стадия доски | `task.stages` | board column (query) | board column | — |
| Исполнитель | `RESPONSIBLE_ID` | assignee | assignee | — |
| Ответственный | — | responsible | — | — |
| Соисполнители / наблюдатели | `ACCOMPLICES` / `AUDITORS` | watchers | watchers | watchers |
| Постановщик + контроль | `CREATED_BY` + `TASK_CONTROL` | author | reporter | — |
| Подзадачи | `PARENT_ID` | parent/children | sub-task / parent | дерево страниц |
| Связи | `task.dependence` (Гант) | relations (precedes и др.) | issue links | — |
| Чек-лист | `checklistitem` | — | (плагины) | task list |
| Учёт времени | `elapseditem` | time entries | worklog | — |
| Версии / спринты / эпики | scrum | versions, sprints | sprints, epics | — |
| Комментарии и история | чат задачи, `history.list` | activities | comments, changelog | comments, versions |
| Вложения | Диск | attachments | attachments | attachments |
| Кастомные поля | `UF_*` | `customFieldN` | `customfield_N` | content properties |
| Сохранённые фильтры / виды | фильтры списка | queries | JQL filters | CQL |
| События | `OnTask*` | webhooks | webhooks | webhooks |

## 6. Выводы для API go24

1. **Собственный API** — REST + JSON, ресурсы как в таблице выше:
   - курсорная пагинация, как в Jira и Confluence;
   - `version` для оптимистической блокировки, как `lockVersion`;
   - фильтры JSON-структурой, как в OpenProject, и строковым запросом для людей, как JQL;
   - вебхуки на все изменения.
2. **Документ (описание, комментарий, страница)** храним JSON-деревом в духе ProseMirror или ADF, Markdown и HTML отдаём как представления.
3. **Импорт** — отдельный сервис `importer` с адаптерами `portal`, `openproject` и `jira+confluence`. Каждый адаптер приводит данные к сводной модели. Первым идёт корпоративного портала: на портале 335 задач, их можно взять прямо из MySQL `sitemanager` или через REST.
4. **Совместимость.** Позже можно добавить слой, который принимает вызовы `tasks.task.*` по образцу корпоративного портала, чтобы существующие интеграции работали без переписывания.

## 7. Связь между продуктами по API-ключам

Решение от 11.10.2026: интеграции go24 с другими системами и обратно работают по API-ключам. OAuth-приложения не нужны.

| Система | Ключ | Как передаётся |
|---|---|---|
| корпоративный портал | Входящий вебхук: пользователь + токен, права по scope (`task`, `tasks`, `sonet`, `crm`, `disk`) | В URL: `https://<портал>/rest/<user_id>/<token>/<метод>` |
| OpenProject | API-ключ пользователя (Моя учётная запись → Токены доступа) | `Authorization: Basic base64("apikey:<key>")` |
| Jira Cloud | API token Atlassian-аккаунта | `Authorization: Basic base64("<email>:<token>")` |
| Confluence Cloud | Тот же API token Atlassian | Так же, как Jira |
| **go24 (входящие запросы)** | Собственные ключи go24 | `Authorization: Bearer go24_<id>_<secret>` |

**Ключи go24:**
- Создаются в интерфейсе: у пользователя или у сервисной учётной записи. Действуют от имени владельца со scope (`tasks:read`, `tasks:write`, `projects:*`, `files:*`, `admin`) и сроком действия.
- В базе лежит только хеш (SHA-256 + соль), секрет показывается один раз при создании. Префикс `go24_<id>_` позволяет найти ключ без перебора и видеть его в логах без секрета.
- Есть журнал использования: когда, с какого IP, какой метод. Ключ можно отозвать.

**Ключи внешних систем в go24:**
- Хранятся в таблице `integrations` (тип, базовый URL, логин или email, ключ). Ключ шифруется AES-GCM, мастер-ключ передаётся через переменную окружения или `docker secret`.
- Ими пользуются `importer` (разовый перенос) и `sync` (двусторонняя синхронизация по вебхукам).
- Входящие вебхуки от корпоративного портала, Jira и OpenProject проверяются по секрету в URL или по подписи.

**Внутри go24** сервисы общаются по служебным ключам или по закрытой сети, вход пользователей идёт только через Authentik.
