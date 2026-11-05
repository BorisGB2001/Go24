# REST, совместимый с корпоративного портала

Интеграции, написанные для корпоративного портала (узел the portal в n8n, обмен с 1С, Zapier, скрипты на CRest и portalphpsdk, коннекторы
маркета), вызывают методы REST по именам. go24 отвечает на такие вызовы в формате корпоративного портала: интеграция работает с go24,
если сменить в ней адрес портала и ключ (GO24DEV-758). Английская версия — [docs/rest-portal.md](../rest-portal.md).

## Адреса

| Вызов | Адрес |
|---|---|
| Входящий вебхук | `https://<go24>/rest/<сотрудник>/<ключ>/<метод>` (`.json` в конце — по желанию) |
| Приложение (OAuth 2.0) | `https://<go24>/rest/<метод>?auth=<access_token>` или токен в `Authorization: Bearer` |

`<сотрудник>` — номер сотрудника (`ID` в `user.current` или его uuid в go24. Ключ — API-ключ go24 этого
сотрудника («Настройки → Ключи API»). Права ключа действуют: метод из области, которой у ключа нет, отвечает
`insufficient_scope`. В `auth` вызова приложения можно передать и API-ключ go24.

Путь без точки (`…/crm/deals`, `…/me`) — по-прежнему REST go24 `/api/v1/<путь>`.

## Параметры

В адресе, в форме (`application/x-www-form-urlencoded`, `multipart/form-data`) с массивами PHP —
`filter[>ID]=5&select[]=ID&select[]=TITLE&fields[PHONE][0][VALUE]=…` — или в JSON. Списки отдаются по 50 записей:
`start` — откуда начинается страница.

## Ответы

`{"result": …, "total": 120, "next": 50, "time": {…}}`. `total` и `next` бывают только у списков; на последней странице
`next` нет. Ошибки — `{"error", "error_description"}`:

| HTTP | `error` | Когда |
|---|---|---|
| 401 | `NO_AUTH_FOUND` | нет токена; ключ неверный, отозван или просрочен; адрес вебхука чужого сотрудника |
| 401 | `insufficient_scope` | у ключа нет области метода |
| 403 | `ACCESS_DENIED` | нет прав на объект, доступ по IP |
| 404 | `ERROR_METHOD_NOT_FOUND` | такого метода нет или его раздел отключён («Настройки → Инструменты») |
| 400 | `ERROR_NOT_FOUND` | такого объекта нет |
| 400 | `ERROR_CORE` | прочие ошибки (неверные данные) |
| 400 | `WRONG_AUTH_TYPE` | `app.info` вызван через вебхук |

`error_description` — на языке из `Accept-Language`.

## batch

`batch` выполняет до 50 команд: `cmd[<имя>]=<метод>?<параметры>`; `halt=1` останавливает на первой ошибке.
`$result[<имя>][ПОЛЕ]…` в команде — значение из результата другой команды. Команда выполняется после тех, на которые
ссылается, остальные — по порядку имён. Ответ: `result.result`, `result.result_error`, `result.result_total`,
`result.result_next`, `result.result_time` (пустая часть — `[]`. Пакет внутри пакета не выполняется.

## Методы

| Метод | Что |
|---|---|
| `server.time` | время сервера (ISO 8601 с поясом) |
| `profile` | вызывающий: `ID`, `ADMIN`, `NAME`, `LAST_NAME`, `PERSONAL_GENDER`, `PERSONAL_PHOTO`, `TIME_ZONE`, `TIME_ZONE_OFFSET` |
| `user.current` | вызывающий — с полями, как в `user.get` |
| `user.get` | сотрудники (область `company:read`): `FILTER` по `ID`, `ACTIVE`, `EMAIL`, `NAME`, `LAST_NAME`, `UF_DEPARTMENT`, `USER_TYPE`; `SORT`/`ORDER`; поля, `XML_ID` — uuid в go24, `UF_DEPARTMENT` — номера отделов, свои поля профиля `UF_USR_*`, `USER_TYPE` — `employee` или `extranet` (гость коллабы; гостей видят администраторы) |
| `scope` | области корпоративного портала, доступные ключу (`full=true` — все, какие есть в go24) |
| `methods` | методы, доступные ключу (`full=true` — все, `scope=crm` — одной области) |
| `method.get` | `name=` → `{isExisting, isAvailable}` |
| `app.info` | вызывающее приложение (только с токеном приложения) |
| `batch` | см. выше |

У сотрудников и отделов есть номера для совместимости с корпоративного портала (миграция 601). Номер выдаётся один раз, по порядку
создания, и повторно не используется.

## CRM

`crm.deal.*`, `crm.lead.*`, `crm.contact.*`, `crm.company.*` (область `crm`; `fields`, `list`, `get` — `crm:read`,
`add`, `update`, `delete` — `crm:write`) — GO24DEV-761. Права CRM действуют: список — карточки, которые вызывающий может
читать, изменения идут тем же путём, что REST go24 (те же проверки, роботы, события, исходящие вебхуки).

| Метод | Что |
|---|---|
| `crm.<сущность>.fields` | поля: `type`, `isRequired`, `isReadOnly`, `isImmutable`, `isMultiple`, `title`, `statusType` |
| `crm.<сущность>.list` | `filter`, `order`, `select`, `start` — страница по 50 с `total` и `next` |
| `crm.<сущность>.get` | `id` → карточка со всеми полями, в том числе `PHONE`, `EMAIL`, `WEB` |
| `crm.<сущность>.add` | `fields` → номер новой карточки |
| `crm.<сущность>.update` | `id`, `fields` → `true`; меняются только переданные поля |
| `crm.<сущность>.delete` | `id` → `true`; карточка — в корзину CRM |

Поля:

| Сущность | Поля |
|---|---|
| deal | `ID`, `TITLE`, `TYPE_ID`, `CATEGORY_ID`, `STAGE_ID`, `STAGE_SEMANTIC_ID`, `CLOSED`, `OPPORTUNITY`, `CURRENCY_ID`, `COMPANY_ID`, `CONTACT_ID`, `CONTACT_IDS`, `BEGINDATE`, `CLOSEDATE`, `SOURCE_ID`, `UTM_*` |
| lead | `ID`, `TITLE`, `NAME`, `SECOND_NAME`, `LAST_NAME`, `COMPANY_TITLE`, `SOURCE_ID`, `SOURCE_DESCRIPTION`, `STATUS_ID`, `STATUS_SEMANTIC_ID`, `OPPORTUNITY`, `CURRENCY_ID`, `IS_RETURN_CUSTOMER`, `CONTACT_ID`, `COMPANY_ID`, `HAS_PHONE`, `HAS_EMAIL`, `PHONE`, `EMAIL`, `UTM_*` |
| contact | `ID`, `NAME`, `SECOND_NAME`, `LAST_NAME`, `POST`, `TYPE_ID`, `SOURCE_ID`, `COMPANY_ID`, `HAS_PHONE`, `HAS_EMAIL`, `PHONE`, `EMAIL` |
| company | `ID`, `TITLE`, `COMPANY_TYPE`, `INDUSTRY`, `ADDRESS`, `WEB`, `HAS_PHONE`, `HAS_EMAIL`, `PHONE`, `EMAIL` |
| все | `ASSIGNED_BY_ID`, `CREATED_BY_ID`, `DATE_CREATE`, `DATE_MODIFY`, `COMMENTS` |

- Номера: у карточек, воронок и стадий есть номера (миграция 619), выдаются один раз и повторно не используются; из
  корзины карточка возвращается со своим номером. `CATEGORY_ID` 0 — основная воронка. `ASSIGNED_BY_ID`, `COMPANY_ID`,
  `CONTACT_ID` — номера (uuid go24 тоже принимается).
- Стадии: встроенные — кодами корпоративного портала (`NEW`, `PREPARATION`, `PREPAYMENT_INVOICE`, `EXECUTING`, `FINAL_INVOICE`, `WON`, `LOSE`;
  у лидов — `NEW`, `IN_PROCESS`, `PROCESSED`, `CONVERTED`, `JUNK`), свои — `UC_<номер>`; стадия другой воронки — с
  приставкой `C<CATEGORY_ID>:`. `add` со `STAGE_ID` `C2:NEW` создаёт сделку в воронке 2; `update` не переносит сделку в
  другую воронку.
- Списки (`SOURCE_ID`, `TYPE_ID`, `COMPANY_TYPE`, `INDUSTRY`) — встроенные коды (`CALL`, `CLIENT`, `IT`…), свои пункты —
  их id в go24.
- `PHONE`, `EMAIL`: `[{"ID", "VALUE", "VALUE_TYPE", "TYPE_ID"}]`, `ID` — место на карточке с 1; `update` с `ID` меняет
  этот (`"DELETE": "Y"` — удаляет), без `ID` — добавляет. `WEB` — сайт компании.
- Имя человека go24 хранит целиком: `NAME` отдаёт его полностью; переданные `NAME`, `SECOND_NAME`, `LAST_NAME`
  склеиваются.
- Значения —: числа и суммы строкой (`"1500.00"`), даты — ISO 8601 с зоной, `Y`/`N`.
- `filter`: `ПОЛЕ` (равно; список — любое из; пусто — не задано), `=`, `!`, `>`, `<`, `>=`, `<=`, `%` (содержит), `=%`
  (LIKE как передан), `@` (в списке), `!@`; `PHONE` и `EMAIL` находят карточку по телефону (в любом виде) или e-mail.
  Поля, которых в go24 нет, пропускаются.
- `select`: по умолчанию и с `*` — все, кроме `PHONE`, `EMAIL`, `WEB`, `CONTACT_IDS` и пользовательских полей; `UF_*` —
  пользовательские поля; `ID` — всегда. `get` отдаёт все.

### Пользовательские поля, связи, дубликаты

GO24DEV-957; область `crm` (`crm:read`, изменения — `crm:write`).

Свои поля сделок, лидов, контактов и компаний («Поля» в настройках CRM) — `UF_CRM_<код>` (код, заданный в
`userfield.add`) или `UF_CRM_<номер>` у поля, добавленного в go24 (миграция 903: у полей номера, выдаются один раз и
повторно не используются). Они есть в `crm.<сущность>.fields` (`isDynamic: true`, `title`, `items` у списка), `get`,
`list` (`select: ["UF_*"]` или по имени; `filter` — как по другим полям), `add` и `update` (меняются только переданные;
карточка, которую вызывающий может менять).

| Метод | Что |
|---|---|
| `crm.<сущность>.userfield.list` | `filter` (`ID`, `FIELD_NAME`, `USER_TYPE_ID`, `MANDATORY`) → `[{ID, ENTITY_ID, FIELD_NAME, USER_TYPE_ID, SORT, MULTIPLE, MANDATORY, EDIT_FORM_LABEL, LIST_COLUMN_LABEL, LIST_FILTER_LABEL, LIST, …}]` |
| `crm.<сущность>.userfield.get` | `id` — поле |
| `crm.<сущность>.userfield.add` | `fields {FIELD_NAME, USER_TYPE_ID, EDIT_FORM_LABEL, LIST, MANDATORY, SORT}` → номер; администратор |
| `crm.<сущность>.userfield.update` | `id`, `fields {EDIT_FORM_LABEL, SORT, MANDATORY, LIST}`; администратор |
| `crm.<сущность>.userfield.delete` | `id` — поле вместе со значениями во всех карточках; администратор |
| `crm.userfield.types`, `crm.userfield.fields`, `crm.userfield.enumeration.fields` | типы и поля пользовательского поля и элемента списка |
| `crm.deal.contact.items.get` | `id` → `[{CONTACT_ID, SORT, ROLE_ID, IS_PRIMARY}]`, основной контакт первым |
| `crm.deal.contact.items.set`, `items.delete` | `id`, `items [{CONTACT_ID, SORT, IS_PRIMARY}]` — контакты сделки вместо прежних; `delete` — без контактов |
| `crm.deal.contact.add`, `delete`, `fields` | `id`, `fields {CONTACT_ID, IS_PRIMARY}` — контактом больше (основным при `IS_PRIMARY=Y`) или меньше |
| `crm.contact.company.items.*`, `crm.contact.company.add/delete/fields` | то же с `COMPANY_ID` |
| `crm.company.contact.items.*`, `crm.company.contact.add/delete/fields` | контакты компании, `CONTACT_ID` |
| `crm.duplicate.findbycomm` | `type` (`PHONE`, `EMAIL`), `values` (до 20), `entity_type` (`LEAD`, `CONTACT`, `COMPANY`; не задан — все) → `{CONTACT: [номера], …}` карточек, которые вызывающий может читать; ничего — `[]` |
| `crm.multifield.fields` | поля `PHONE`, `EMAIL`, `WEB`: `ID`, `TYPE_ID`, `VALUE`, `VALUE_TYPE` |

- Типы: `string`, `double` (`integer` принимается как число), `date` (`datetime` хранится датой), `boolean` (`1`/`0`;
  принимаются `Y`, `1`, `true`), `enumeration`, `url`. go24 хранит одно значение поля: `MULTIPLE=Y` не принимается, из
  массива берётся первое значение.
- Значение списка — `ID` его элемента, место с 1 (принимается и текст элемента). `userfield.update` с `LIST`:
  `{ID, VALUE}` переименовывает элемент вместе со значениями в карточках, `{ID, DEL: "Y"}` удаляет его вместе с ними,
  `{VALUE}` добавляет; места элементов после удалённого сдвигаются.
- `FIELD_NAME` — с `UF_CRM_` или без: до 40 латинских букв, цифр и `_`; код из одних цифр — не короче 7, чтобы
  не спутать с номером поля, добавленного в go24. `EDIT_FORM_LABEL` (или `LIST_COLUMN_LABEL`, `LABEL`) — текст или тексты
  по языкам (`{"en": …, "ru": …}`): название поля.
- У контакта в go24 одна компания: `crm.contact.company.items.set` с несколькими оставляет основную (`IS_PRIMARY=Y` или
  с наименьшим `SORT`); `crm.contact.company.add` заменяет компанию. Изменения идут тем же путём, что `update` (права,
  роботы, история).

### Справочники, воронки, дела, комментарии

GO24DEV-834; область `crm` (`crm:read`, изменения — `crm:write`).

| Метод | Что |
|---|---|
| `crm.status.list` | `filter` (`ENTITY_ID`, `STATUS_ID`, любое поле; равно), `order` (одно поле) → `[{ID, ENTITY_ID, STATUS_ID, NAME, NAME_INIT, SORT, SYSTEM, CATEGORY_ID, COLOR, SEMANTICS, EXTRA}]` |
| `crm.status.get`, `crm.status.fields` | `id` — элемент |
| `crm.status.add` | администраторы: `fields {ENTITY_ID, NAME, SORT, COLOR, SEMANTICS}` → `ID` нового; стадия (`STATUS`, `DEAL_STAGE`, `DEAL_STAGE_<n>`, `DYNAMIC_<t>_STAGE_<n>`) — рабочая, `SEMANTICS` `F` — провальная, на месте по `SORT` среди стадий своего вида; `STATUS_ID` — свой у go24 (`UC_<ID>`) |
| `crm.status.update` | `id`, `fields {NAME, COLOR, SORT}` — название, цвет и место стадии; название пункта справочника |
| `crm.status.delete` | `id`; встроенный пункт — только с `params {FORCED: "Y"}`; стадия с карточками не удаляется — сначала перенесите их; успешная стадия и последняя стадия своего вида остаются |
| `crm.status.entity.types` | справочники: `STATUS` (стадии лида), `SOURCE`, `CONTACT_TYPE`, `COMPANY_TYPE`, `INDUSTRY`, `DEAL_TYPE`, `DEAL_STAGE` и `DEAL_STAGE_<n>` каждой воронки, `DYNAMIC_<t>_STAGE_<n>` смарт-процессов |
| `crm.status.entity.items` | `entityId` → `[{NAME, SORT, STATUS_ID}]` |
| `crm.dealcategory.list`, `get`, `fields` | воронки, кроме основной: `[{ID, CREATED_DATE, NAME, IS_LOCKED, SORT}]` |
| `crm.dealcategory.add`, `update`, `delete` | администраторы: `fields {NAME, SORT}` → `ID` новой (у новой воронки стадии, как у созданной в CRM), `id`; воронка со сделками и основная не удаляются |
| `crm.dealcategory.default.get` | `{ID: 0, NAME}` |
| `crm.dealcategory.stage.list` | `id` → стадии воронки `[{NAME, SORT, STATUS_ID}]` |
| `crm.category.list`, `get` | `entityTypeId` 2 или смарт-процесса (128…), `id` → `{categories: [{id, name, sort, entityTypeId, isDefault}]}`, `{category}` |
| `crm.category.add`, `update`, `delete`, `fields` | администраторы: `entityTypeId`, (`id`), `fields {name, sort}` → `{category}`; `delete` → `[]` |
| `crm.stagehistory.list` | `entityTypeId` 1, 2 или смарт-процесса, `filter` (`OWNER_ID`, `TYPE_ID`, `STAGE_ID`/`STATUS_ID`, `CATEGORY_ID`, `>=CREATED_TIME`…), `order`, `select`, по 50 → `{items: [{ID, TYPE_ID, OWNER_ID, CREATED_TIME, CATEGORY_ID, STAGE_SEMANTIC_ID, STAGE_ID}]}` (у лида — `STATUS_SEMANTIC_ID`, `STATUS_ID`) |
| `crm.currency.list`, `get`, `base.get` | `[{CURRENCY, AMOUNT_CNT, AMOUNT, SORT, BASE, FULL_NAME, DATE_UPDATE}]`; `AMOUNT` — курс к базовой |
| `crm.enum.ownertype`, `crm.enum.activitytype` | `[{ID, NAME, SYMBOL_CODE, SYMBOL_CODE_SHORT}]`: 1 лид, 2 сделка, 3 контакт, 4 компания; дела 1 встреча, 2 звонок, 6 дело |
| `crm.activity.fields`, `list`, `get` | дела: `filter` (`OWNER_TYPE_ID`, `OWNER_ID`, `COMPLETED`, `RESPONSIBLE_ID`, `>DEADLINE`…), `order`, `select`, по 50 |
| `crm.activity.add` | `fields` с `OWNER_TYPE_ID`, `OWNER_ID`, `SUBJECT`, `TYPE_ID` (или `PROVIDER_ID` `CRM_TODO`, `CRM_MEETING`, `VOXIMPLANT_CALL`), `DESCRIPTION`, `RESPONSIBLE_ID`, `DEADLINE` (`END_TIME`, `START_TIME`), `COMPLETED` → номер дела |
| `crm.activity.update`, `delete` | `id`, `fields` — меняются только переданные поля; `COMPLETED` `Y` завершает дело, `N` возобновляет |
| `crm.timeline.comment.list` | `filter[ENTITY_TYPE]` (`deal`, `lead`, `contact`, `company`; понимает и `2`, `D`), `filter[ENTITY_ID]` → `[{ID, CREATED, ENTITY_ID, ENTITY_TYPE, AUTHOR_ID, COMMENT}]`, новые первыми |
| `crm.timeline.comment.get`, `add`, `update`, `delete`, `fields` | `id`, `fields {ENTITY_TYPE, ENTITY_ID, COMMENT}`; `add` и `update` → номер комментария; менять — свой комментарий (или администратор) |

- Стадии и элементы справочников нумеруются из одной последовательности (миграция 663), так что `ID` в
  `crm.status.list` уникален. У дел тоже номера; номер комментария — номер строки истории.
- Дело карточки читает тот, кто может читать карточку, и его ответственный; добавить — на карточке, которую вызывающий
  может менять; менять и удалять — ответственный или тот, кто может менять карточку. Комментарий добавляет тот, кто
  может читать карточку.
- Без `DEADLINE` срок дела — сейчас; перенесённый срок напоминается заново.
- Справочники и воронки меняют администраторы, как в настройках CRM (GO24DEV-976). `SORT` пункта справочника через
  REST пока не задаётся.
- История стадий (GO24DEV-976, миграция 940) пишется go24, чем бы ни сдвинули карточку — канбан, робот, REST,
  удаление стадии. `TYPE_ID`: 1 создана, 2 переход на рабочую стадию, 3 на финальную (успех или провал), 5 переход в
  другую воронку. Числа в ней — числами. У карточек, созданных до миграции, одна строка: их стадия на тот
  момент, со временем создания. Отдаётся история только тех карточек, которые вызывающий может читать.

### Универсальные методы и смарт-процессы

GO24DEV-952; область `crm` (`crm:read`, изменения — `crm:write`). `crm.item.*` работают с карточками любого вида по
`entityTypeId`: 1 лиды, 2 сделки, 3 контакты, 4 компании, 128 и больше — смарт-процессы.

| Метод | Что |
|---|---|
| `crm.item.fields` | `entityTypeId` → `{fields: {title: {type, isRequired, isReadOnly, isMultiple, title, upperName}…}}` |
| `crm.item.list` | `entityTypeId`, `filter`, `order`, `select`, `start` → `{items: [...]}`, по 50; операторы фильтра — как в `crm.deal.list` |
| `crm.item.get` | `entityTypeId`, `id` → `{item}` |
| `crm.item.add`, `update` | `entityTypeId`, (`id`), `fields` → `{item}` в новом виде; `update` меняет переданные поля |
| `crm.item.delete` | `entityTypeId`, `id` — карточка в корзину → `[]` |
| `crm.type.list`, `get`, `getByEntityTypeId`, `fields` | смарт-процессы: `{types: [{id, entityTypeId, title, createdTime, isCategoriesEnabled…}]}`, `{type}` |
| `crm.type.add`, `update`, `delete` | администраторы: `fields {title}` → `{type}`; у нового — своя воронка и стадии; с карточками не удаляется |
| `crm.category.list`, `get` | `entityTypeId` 2 или смарт-процесса → его воронки |

- Поля — поля `crm.deal.*` и остальных в camelCase: `assignedById`, `categoryId`, `opportunity`, `contactIds`,
  `utmSource`; `stageId` — и `STATUS_ID` лида, `createdTime`/`updatedTime` — `DATE_CREATE`/`DATE_MODIFY`, `createdBy` —
  `CREATED_BY_ID`, `typeId` компании — `COMPANY_TYPE`. Числа и номера — числами. Телефоны, e-mail и сайты —
  `fm: [{id, typeId (PHONE, EMAIL, WEB), valueType, value}]`, так же и в `fields` (`id` меняет этот, `delete: "Y"` удаляет).
- Номер смарт-процесса (`entityTypeId`, миграция 925) — он же `id`. Его карточки нумеруются как сделки, воронки — как
  воронки сделок; стадии — `DT<entityTypeId>_<categoryId>:NEW` (`PREPARATION`, `CLIENT`, `SUCCESS`, `FAIL`, свои
  `UC_<n>`); `crm.status.list` отдаёт их с `ENTITY_ID` `DYNAMIC_<entityTypeId>_STAGE_<categoryId>`. Новая карточка без
  `categoryId` попадает в первую воронку.
- Права CRM на карточки смарт-процесса — права сделок.
- Пока нет: предложения (7) и счета (31) через `crm.item.*`, `crm.item.batchImport`, пользовательские поля `ufCrm…`.

### Товары и товарные позиции

GO24DEV-928; область `crm` (`crm:read`, изменения — `crm:write`). Каталог «Товары» и товары сделок и лидов.

| Метод | Что делает |
|---|---|
| `crm.product.fields`, `list`, `get` | `filter`, `order`, `select`, `start` → `ID`, `CATALOG_ID`, `NAME`, `XML_ID` (артикул), `ACTIVE`, `SECTION_ID`, `TYPE` (1 товар, 4 вариация, 7 услуга), `PRICE`, `CURRENCY_ID`, `VAT_ID`, `VAT_INCLUDED`, `MEASURE`, `DESCRIPTION`, `DATE_CREATE`, `CREATED_BY` |
| `crm.product.add`, `update`, `delete` | `fields` — только переданные поля; `TYPE` 7 — услуга; `add` → номер товара; `delete` удаляет товар с вариациями, строки карточек остаются |
| `crm.productsection.fields`, `list`, `get`, `add`, `update`, `delete` | разделы каталога: `ID`, `CATALOG_ID`, `SECTION_ID` (родитель), `NAME`, `SORT`; товары и подразделы удалённого раздела переходят к родителю |
| `crm.catalog.list`, `get`, `fields` | единственный каталог, `ID` 1 |
| `crm.measure.list`, `get` | единицы измерения: `ID`, `CODE` (ОКЕИ), `MEASURE_TITLE`, `SYMBOL_RUS`, `SYMBOL_INTL`, `IS_DEFAULT` |
| `crm.vat.list`, `get` | ставки НДС 0, 5, 7, 10, 20 %: `ID` 1–5, `RATE`, `NAME` |
| `crm.deal.productrows.get`, `crm.lead.productrows.get` | `id` → `[{ID, OWNER_ID, OWNER_TYPE, PRODUCT_ID, PRODUCT_NAME, PRICE, PRICE_EXCLUSIVE, PRICE_NETTO, PRICE_BRUTTO, QUANTITY, DISCOUNT_TYPE_ID, DISCOUNT_RATE, DISCOUNT_SUM, TAX_RATE, TAX_INCLUDED, MEASURE_CODE, MEASURE_NAME, SORT, TYPE, STORE_ID, RESERVE_QUANTITY, DATE_RESERVE_END}]` |
| `crm.deal.productrows.set`, `crm.lead.productrows.set` | `id`, `rows` — строки карточки заменяются, сумма считается по ним |
| `crm.productrow.fields`, `list` | `filter[OWNER_TYPE]` (`D`, `L`), `filter[OWNER_ID]` → строки карточки |
| `crm.item.productrow.fields`, `list`, `get` | те же строки в camelCase (`productName`, `priceExclusive`…): `filter {=ownerType, =ownerId}` → `{productRows}`, `id` → `{productRow}` |
| `crm.item.productrow.add`, `update`, `delete` | `fields {ownerType, ownerId, …, sort}` → `{productRow}` на месте по `sort` (иначе в конец); `update` — переданные поля; `delete` → `null` |
| `crm.item.productrow.set` | `ownerType`, `ownerId`, `productRows` → `{productRows}` |

- Товары, разделы, единицы и склады пронумерованы (миграция 851); `ID` строки — её номер, он сохраняется, пока строка
  есть (`ID` строки карточки, переданный в `set`, сохраняет его).
- Цены: `PRICE` — цена за единицу со скидкой и налогом, `PRICE_EXCLUSIVE` — со скидкой без налога, `PRICE_NETTO` — без
  скидки и налога, `PRICE_BRUTTO` — с налогом без скидки. Строка берёт первую переданную из `PRICE`, `PRICE_EXCLUSIVE`,
  `PRICE_NETTO`, `PRICE_BRUTTO`; строка товара каталога без них — цену товара, а также его название, единицу и НДС. go24
  хранит скидку в процентах: абсолютная (`DISCOUNT_TYPE_ID` 1, `DISCOUNT_SUM` от цены без налога) хранится своим
  процентом, с точностью до копейки.
- `MEASURE_CODE` — код единицы по ОКЕИ, `MEASURE_NAME` — её обозначение. `STORE_ID` и `RESERVE_QUANTITY` резервируют товар
  каталога в сделке на складе (не переданы — резерв как в «Складском учёте»).
- Каталог читают все сотрудники CRM, меняют администраторы, как на его страницах; строки карточки — те, кто может читать
  или менять карточку.
- Пока нет: строк предложений и счетов (`ownerType` `Q`, `SI`), `crm.product.property.*`, картинок (`PREVIEW_PICTURE`,
  `DETAIL_PICTURE`) и `catalog.*` нового API каталога — собственный REST товаров go24 — `/api/v1/crm/products`.

### Генератор документов

GO24DEV-980; scope `documentgenerator` (`documents:read`, изменения — `documents:write`). Шаблоны CRM, документы
карточки и «Нумераторы»; карточка — `entityTypeId` (1 лиды, 2 сделки, 3 контакты, 4 компании, с 128 — смарт-процессы)
и `entityId`. У шаблонов, документов и нумераторов есть номера (миграция 955).

| Метод | Что делает |
|---|---|
| `crm.documentgenerator.template.list` | шаблоны, по которым вызывающий может создавать документы («Кому доступен»; с правом «Изменение шаблонов» — все); `filter` по `id`, `name` (`%name`), `entityTypeId`, `numeratorId`; `start` → `{templates: […]}` |
| `crm.documentgenerator.template.get` | `id` → `{template: {id, name, entityTypeId: ["2"], numeratorId, users, fileName, download, createTime, …}}` |
| `crm.documentgenerator.template.getfields` | `id`, карточка (`entityTypeId`, `entityId`), `values` → `{templateFields: {имя: {title, value, default, required, hideRow}}}` — поля шаблона со значениями карточки (или значением поля по умолчанию), подписи и настройки из «Полей» шаблона |
| `crm.documentgenerator.template.add` | `fields[name]`, `fields[file]` (DOCX: base64, `[имя, base64]` или `{fileData: …}`), `fields[entityTypeId]` (один вид), `fields[numeratorId]`, `fields[users]`, `fields[active]`, `fields[sort]`, `fields[withStamps]` → `{template}` (право «Изменение шаблонов») |
| `crm.documentgenerator.template.update` | `id`, `fields[name]`, `fields[file]`, `fields[numeratorId]`, `fields[users]`, `fields[active]`, `fields[sort]`, `fields[withStamps]` → `{template}` (право «Изменение шаблонов») |
| `crm.documentgenerator.template.delete` | `id` (право «Изменение шаблонов»); созданные по нему документы остаются |
| `crm.documentgenerator.document.list` | `filter[entityTypeId]`, `filter[entityId]` (обязательны) и по `templateId`, `number`, `title`; `start` → `{documents: […]}`, новые первыми |
| `crm.documentgenerator.document.get` | `id` → `{document: {id, title, number, templateId, entityTypeId, entityId, values, stampsEnabled, downloadUrl, pdfUrl, publicUrl, createdBy, createTime, updateTime}}` |
| `crm.documentgenerator.document.getfields` | `id`, `values` → `{documentFields}` — поля шаблона со значениями, с которыми создан документ |
| `crm.documentgenerator.document.add` | `templateId`, `entityTypeId`, `entityId`, `values` (поверх значений карточки), `stampsEnabled` (по умолчанию `Y`) → `{document}` |
| `crm.documentgenerator.document.update` | `id`, `values`, `stampsEnabled` → `{document}`, созданный заново; номер прежний |
| `crm.documentgenerator.document.delete` | `id` |
| `crm.documentgenerator.document.enablepublicurl` | `id`, `status` (`1` — включить, `0` — выключить) → `{publicUrl}` (`null` — выключена) |
| `crm.documentgenerator.numerator.list`, `get` | `{numerators: […]}`, `{numerator: {id, name, template, settings}}` (право «Изменение шаблонов» или «Изменение настроек») |
| `crm.documentgenerator.numerator.add`, `update` | `fields[name]`, `fields[template]` (`{NUMBER}`, `{YEAR}`, `{MONTH}`, `{DAY}` и текст), `fields[settings][Legacy_Main_Numerator_Generator_SequentNumberGenerator]` — `start`, `step`, `periodicBy` (`day`, `month`, `year`; пусто — не обнулять), `length` (число цифр `{NUMBER}`) → `{numerator}` (право «Изменение настроек») |
| `crm.documentgenerator.numerator.delete` | `id` — нумератор, который не выбран ни в одном шаблоне (право «Изменение настроек») |
| `documentgenerator.role.list`, `get` | `{roles: […]}`, `{role: {id, name, code, isDefault, permissions, accessCodes}}` — роли «Генератор документов → Права доступа» (право «Изменение настроек») |
| `documentgenerator.role.add`, `update` | `fields[name]`, `fields[permissions]` — `{TEMPLATES: {MODIFY}, DOCUMENTS: {VIEW, MODIFY}, SETTINGS: {MODIFY}}`, `X` (а также `A`, `D`) — разрешено, `""` — нет; в `update` неуказанные права остаются → `{role}` |
| `documentgenerator.role.delete` | `id`; роль по умолчанию («Все сотрудники») не удаляется |
| `documentgenerator.role.fillaccesses` | `accesses: [{roleId, accessCode}, …]` — участники ролей заново (`U<номер>`, `D<номер>`/`DR<номер>`); роли без участников в списке остаются без них |

- `users` шаблона — коды доступа: `UA` — все, `U<номер>` — сотрудник, `D<номер>`/`DR<номер>` — отдел с подотделами.
- `download` (DOCX шаблона), `downloadUrl` (DOCX документа) и `pdfUrl` (его PDF, сделанный сервером документов) —
  подписанные ссылки go24 (`/rest/docfile?token=…`): работают без ключа сутки, пока сотрудник активен и может
  пользоваться шаблоном или видеть карточку.
- Права — как на странице карточки (кто видит карточку, видит её документы, кто может её менять — меняет их) и по
  ролям генератора (GO24DEV-987): «Просмотр документов» и «Изменение документов». Нет:
  `crm.documentgenerator.document.upload`, `region` и `code` шаблонов, шаблоны счетов и кадровых документов,
  `imageUrl`.

## Задачи

`tasks.task.*`, `task.commentitem.*`, `task.checklistitem.*` (область `task`; чтение — `tasks:read`, изменения —
`tasks:write`) — GO24DEV-820; `task.elapseditem.*`, `task.stages.*`, `task.dependence.*`, `tasks.task.result.*`,
`history.list`, `counters.get`, `task.planner.getlist`, `startwatch`, `stopwatch` — GO24DEV-995. Права задач go24 действуют: список — задачи, которые вызывающий видит, изменения идут тем
же путём, что в go24 (те же проверки, роботы, уведомления, мессенджер).

| Метод | Что |
|---|---|
| `tasks.task.getFields` | `{"fields": {ID: {title, type, primary, required, values}}}` |
| `tasks.task.list` | `filter`, `order`, `select`, `start` → `{"tasks": [...]}`, по 50 с `total` и `next` |
| `tasks.task.get` | `taskId`, `select` → `{"task": {...}}` |
| `tasks.task.add` | `fields` → `{"task": {...}}` |
| `tasks.task.update` | `taskId`, `fields` → `{"task": {...}}`; меняются только переданные поля |
| `tasks.task.delete` | `taskId` → `{"task": true}`; задача уходит в корзину |
| `tasks.task.start`, `pause`, `complete`, `defer`, `renew`, `approve`, `disapprove` | `taskId` → `{"task": {...}}` |
| `tasks.task.delegate` | `taskId`, `userId` → `{"task": {...}}` |
| `tasks.task.favorite.add`, `favorite.remove`, `mute`, `unmute` | `taskId` → `{"task": true}` |
| `task.commentitem.getlist` | `TASKID`, `ORDER` (`ID`, `POST_DATE`, `AUTHOR_ID`), `FILTER` (`AUTHOR_ID`, `>POST_DATE`, `<ID`…) |
| `task.commentitem.get`, `add`, `update`, `delete` | `TASKID`, `ITEMID`, `FIELDS {POST_MESSAGE}`; `add` → номер комментария; менять можно свой комментарий |
| `task.checklistitem.getlist`, `get` | `TASKID`, `ITEMID` → `[{ID, TASK_ID, PARENT_ID, CREATED_BY, TITLE, SORT_INDEX, IS_COMPLETE, IS_IMPORTANT, TOGGLED_BY, TOGGLED_DATE}]` |
| `task.checklistitem.add`, `update`, `complete`, `renew`, `delete` | `TASKID`, `ITEMID`, `FIELDS {TITLE, IS_COMPLETE}`; `add` → номер пункта |
| `task.elapseditem.getlist` | `TASKID` (не задан — записи всех задач, которые вызывающий видит), `ORDER`, `FILTER` (`ID`, `USER_ID`, `TASK_ID`, `CREATED_DATE`, `SECONDS` с `>`, `<`, `>=`, `<=`, `!`) → `[{ID, TASK_ID, USER_ID, COMMENT_TEXT, SECONDS, MINUTES, SOURCE, CREATED_DATE, DATE_START, DATE_STOP}]`, по 50 |
| `task.elapseditem.get`, `add`, `update`, `delete` | `TASKID`, `ITEMID`, `ARFIELDS {SECONDS или MINUTES, COMMENT_TEXT, CREATED_DATE}`; `add` → номер записи; время записывают только себе (`USER_ID` — только вызывающий), запись меняет её автор или руководитель проекта |
| `task.stages.get` | `entityId` — номер группы, `0` — «Мой план» вызывающего → `{ID: {ID, TITLE, SORT, COLOR, SYSTEM_TYPE, ENTITY_ID, ENTITY_TYPE}}`; `SYSTEM_TYPE` `NEW`, `FINISH` у первой и последней стадии группы, `ENTITY_TYPE` `G` или `U` |
| `task.stages.add`, `update`, `delete` | `fields {TITLE, COLOR, ENTITY_ID}`, `id`; `add` → номер стадии, стадия группы встаёт перед «Сделаны»; стадии группы — владелец и модераторы, плана — только его хозяин |
| `task.stages.canmovetask`, `movetask` | `entityId`, `entityType`; `id` (задача), `stageId` — как перетаскивание на канбане: в «Сделаны» — задача завершается, оттуда — возобновляется |
| `task.dependence.add`, `delete` | `taskIdFrom` (предшественник), `taskIdTo`, `linkType` (`0` старт-старт, `1` старт-финиш, `2` финиш-старт — по умолчанию, `3` финиш-финиш); цикл не допускается |
| `tasks.task.result.list` | `taskId` → `[{id, taskId, commentId, createdBy, createdAt, updatedAt, status, text, formattedText, files}]` |
| `tasks.task.result.addFromComment`, `deleteFromComment` | `commentId` → результат, `true` |
| `tasks.task.history.list` | `taskId`, `filter {FIELD}`, `order {ID}` → `{"list": [{id, createdDate, field, value {from, to}, user}]}`; поля корпоративного портала (`NEW`, `STATUS` числами, `RESPONSIBLE_ID`, `DEADLINE`…) |
| `tasks.task.counters.get` | `userId` (чужие — в режиме администратора), `groupId`, `type` (`view_all`, `view_role_responsible`, `view_role_accomplice`, `view_role_auditor`, `view_role_originator`) → `{expired: {counter}, new_comments: {counter}}` |
| `task.planner.getlist` | → номера задач плана на день вызывающего |
| `tasks.task.startwatch`, `stopwatch` | `taskId` → `{"task": true}`; наблюдателем становится любой, кто видит задачу |

Поля в `select`, `filter`, `order` и `fields` — заглавными (`RESPONSIBLE_ID`; `responsibleId` тоже понимается), в ответе
— camelCase (`responsibleId`): `ID`, `PARENT_ID`, `TITLE`, `DESCRIPTION`, `MARK` (`P`, `N`), `PRIORITY`
(`0`, `1`, `2`), `STATUS` и `REAL_STATUS` (`2` ждёт выполнения, `3` выполняется, `4` ждёт контроля, `5` завершена, `6`
отложена), `GROUP_ID`, `CREATED_BY`, `CREATED_DATE`, `RESPONSIBLE_ID`, `CHANGED_DATE`, `CLOSED_BY`, `CLOSED_DATE`,
`DEADLINE`, `START_DATE_PLAN`, `END_DATE_PLAN`, `ALLOW_CHANGE_DEADLINE`, `MATCH_WORK_TIME`, `TASK_CONTROL`,
`TIME_ESTIMATE` (секунды), `TIME_SPENT_IN_LOGS`, `COMMENTS_COUNT`, `ACCOMPLICES`, `AUDITORS`, `TAGS`. С `CREATED_BY`,
`RESPONSIBLE_ID` и `GROUP_ID` в ответе — `creator`, `responsible` (`{id, name, icon, workPosition}`) и `group`
(`{id, name}`).

- Номера: у задач, групп (проектов go24), комментариев и пунктов чек-листа есть номера (миграция 662), у записей времени
  и стадий — тоже (миграция 996; стадии групп и планов — в одном ряду); выдаются один раз
  и повторно не используются, корзина восстанавливает задачу с её номером.
- `filter`: префиксы, как в CRM (`=`, `!`, `>`, `<`, `>=`, `<=`, `%`, `@`, `!@`), и `MEMBER` (участник в любой роли),
  `ACCOMPLICE`, `AUDITOR` (номера сотрудников), `TAG`, `STATUS` `-1` — просроченные.
- `select`: не задан или `*` — все поля; `ID` — всегда.
- `add`: без `GROUP_ID` задача ставится в проект, куда вызывающий ставил задачу последней (в go24 каждая задача — в
  проекте), без `RESPONSIBLE_ID` — на вызывающего. `update` не переносит задачу в другой проект и под другую задачу.
- Старые методы (`task.commentitem.*`, `task.checklistitem.*`) принимают параметры по имени или по месту: JSON-списком
  `[12, {"POST_MESSAGE": "…"}]` или `0=12&1[POST_MESSAGE]=…`.

### Скрам

`tasks.api.scrum.*` (scope `task`) — GO24DEV-1014. Скрам — скрам go24 по номеру группы; участники планируют (спринты,
эпики, бэклог), владелец, модераторы и скрам-команда начинают и завершают спринты. Бэклог и спринты всех скрамов
нумеруются в одном ряду (`entityId` задачи — то или другое), эпики — в своём (миграция 1014). Ответы — в camelCase.

| Метод | Что |
|---|---|
| `tasks.api.scrum.backlog.get`, `backlog.add` | `id` (группа) / `fields {groupId}` → `{id, groupId, entityType: backlog, createdBy…}`; у скрама go24 бэклог есть всегда |
| `tasks.api.scrum.epic.add`, `get`, `list`, `update`, `delete`, `getFields` | `fields {groupId, name, description, color}`, `id` → `{id, groupId, name, description, createdBy, modifiedBy, color}`; `list` — `filter`, `order`, `select`, `start` по скрамам, которые видит вызывающий; удалённый эпик снимается с задач |
| `tasks.api.scrum.sprint.add`, `get`, `list`, `update`, `delete`, `getFields` | `fields {groupId, name, goal, dateStart, dateEnd, status}`, `id` → `{id, groupId, entityType: sprint, name, goal, sort, createdBy, modifiedBy, dateStart, dateEnd, status}`; без дат спринт идёт за последним на две недели; `status` `active` / `completed` начинает или завершает его; удаляется запланированный спринт, его задачи возвращаются в бэклог |
| `tasks.api.scrum.sprint.start`, `complete` | `id` → спринт; при завершении незавершённые задачи уходят в бэклог |
| `tasks.api.scrum.kanban.getStages`, `addStage`, `updateStage`, `deleteStage`, `getFields` | `sprintId`, `stageId`, `fields {sprintId, name, color}` → `[{id, name, sort, type (NEW, WORK, FINISH), sprintId, color}]`; стадии — группы, одни для всех спринтов |
| `tasks.api.scrum.kanban.addTask`, `deleteTask` | `sprintId`, `taskId`, `stageId` — задача в спринт (в активном — на стадию), из него — в бэклог |
| `tasks.api.scrum.task.get`, `update`, `getFields` | `id`, `fields {entityId, storyPoints ("" — нет), epicId (0 — нет), sort}` → `{entityId, storyPoints, epicId, sort, createdBy, modifiedBy}` |

## Рабочие группы

`sonet_group.*`, `socialnetwork.api.workgroup.*` (scope `sonet_group`; чтение — `projects:read`, изменения —
`projects:write`) — GO24DEV-940. Группа — группа или проект go24 по номеру (тот же, что `GROUP_ID` задач); права —
как на страницах группы.

| Метод | Что |
|---|---|
| `sonet_group.create` | `NAME`, `DESCRIPTION`, `VISIBLE`, `OPENED`, `CLOSED`, `KEYWORDS`, `INITIATE_PERMS`, `PROJECT`, `PROJECT_DATE_START`, `PROJECT_DATE_FINISH`, `SUBJECT_ID`, `IMAGE` (`[имя, base64]`), `OWNER_ID` (у администратора) → номер группы |
| `sonet_group.update` | `GROUP_ID` и поля `create`, которые меняются → номер группы |
| `sonet_group.delete` | `GROUP_ID` → `true`; владелец или администратор, группа с задачами не удаляется (как на её странице) |
| `sonet_group.get` | `FILTER` (`!`, `%`, `>`, `>=`, `<`, `<=`), `ORDER` (по умолчанию `ID` `DESC`), `IS_ADMIN` → `[{ID, NAME, DESCRIPTION, DATE_CREATE, DATE_UPDATE, DATE_ACTIVITY, ACTIVE, VISIBLE, OPENED, CLOSED, SUBJECT_ID, SUBJECT_NAME, OWNER_ID, KEYWORDS, NUMBER_OF_MEMBERS, NUMBER_OF_MODERATORS, INITIATE_PERMS, PROJECT, PROJECT_DATE_START, PROJECT_DATE_FINISH, IS_EXTRANET, IMAGE}]` |
| `sonet_group.setowner` | `GROUP_ID`, `USER_ID` → `true`; не участник сначала вступает в группу |
| `sonet_group.user.get` | `ID` → `[{USER_ID, ROLE}]` |
| `sonet_group.user.add`, `user.delete` | `GROUP_ID`, `USER_ID` (один или список) → номера добавленных (сразу, без приглашения) или исключённых (не владельца) |
| `sonet_group.user.invite` | `GROUP_ID`, `USER_ID`, `MESSAGE` → номера приглашённых; их спрашивает колокольчик |
| `sonet_group.user.request` | `GROUP_ID`, `MESSAGE` → `true`: в открытую группу вызывающий вступает сразу, в закрытую — просит |
| `sonet_group.user.groups` | группы вызывающего → `[{GROUP_ID, GROUP_NAME, ROLE, GROUP_IMAGE, IS_EXTRANET}]` |
| `sonet_group.feature.access` | `GROUP_ID`, `FEATURE` (`tasks`, `files`, `calendar`, `blog`, `chat`), `OPERATION` → `true` / `false` |
| `socialnetwork.api.workgroup.get` | `params {groupId}` → группа с `MEMBERS`, `ORDINARY_MEMBERS`, `MODERATOR_MEMBERS`, `INVITED_MEMBERS`, `CHAT_ID`, `DIALOG_ID`, `TYPE` |
| `socialnetwork.api.workgroup.list` | `filter`, `order`, `select` → `{"workgroups": [{id, name, type, avatar, additionalData {role}}…]}` |

- Тип группы: открытая — `VISIBLE` и `OPENED` `Y`, закрытая — только `VISIBLE`, секретная — ни того, ни другого; новая
  группа — закрытая, если они не сказали иначе. `CLOSED` `Y` — архив, `PROJECT` `Y` — проект (или скрам).
- Роли: `A` — владелец, `E` — модератор, `K` — участник. `INITIATE_PERMS`: `A` — владелец, `E` — владелец и модераторы,
  `K` — все участники.
- `SUBJECT_ID` — номер тематики (миграция 880). Новая группа получает ключ `SG<номер>` (у групп корпоративного портала ключей нет).
- `feature.access`: `view…` — кто видит группу (в открытой — все), `create_tasks` — по правам группы на задачи, `full…`,
  `moderate…`, `premoderate…`, `delete…`, `edit_tasks`, `modify_folders` — владелец и модераторы, остальное — участники;
  инструмент, выключенный в «Возможностях группы», или которого в go24 нет (`photo`), — `false`.

## Лента

GO24DEV-948. `log.blogpost.*`, `log.blogcomment.*` (область `log`; чтение — `feed:read`, изменения — `feed:write`), с
правами страницы Ленты. Посты и комментарии — по номерам (миграция 892).

| Метод | Что |
|---|---|
| `log.blogpost.get` | посты, которые видит вызывающий, новые сверху, по 50 с `start`; `POST_ID` — один пост; `LOG_RIGHTS` — только с этими адресатами → `[{ID, TITLE, DETAIL_TEXT, AUTHOR_ID, DATE_PUBLISH, MICRO, NUM_COMMENTS, VIEWS, CATEGORY_ID, HAS_SOCNET_ALL, HAS_TAGS, HAS_IMAGES, FILES, UF_BLOG_POST_IMPRTNT, UF_IMPRTANT_DATE_END, URL}]` |
| `log.blogpost.add` | `POST_TITLE`, `POST_MESSAGE`, `DEST` (без него — всем сотрудникам), `IMPORTANT` `Y` и `IMPORTANT_DATE_END`, `FILES`, `UF_BLOG_POST_FILE` → номер поста |
| `log.blogpost.update` | `POST_ID`, `POST_TITLE`, `POST_MESSAGE` (автор), `DEST` меняет адресатов, `FILES` и `UF_BLOG_POST_FILE` добавляют файлы → номер поста |
| `log.blogpost.delete` | `POST_ID` → `true`; автор или администратор |
| `log.blogpost.share` | `POST_ID`, `DEST` (сотрудники и отделы) → `true` |
| `log.blogpost.getusers.important` | `POST_ID` → номера тех, кто отметил важный пост прочитанным |
| `log.blogcomment.add` | `POST_ID`, `TEXT`, `FILES`, `UF_BLOG_COMMENT_FILE` → номер комментария |
| `log.blogcomment.delete` | `COMMENT_ID` → `true`; автор или администратор |

- `DEST` (или `SPERM` старых интеграций): `UA` — все сотрудники, `U<n>` — сотрудник, `SG<n>` — группа, `D<n>` и `DR<n>` —
  отдел с подотделами. Пост go24 — не больше чем в одной группе: сотрудникам и отделам, указанным вместе с ней, пост
  открывается («Поделиться»).
- `FILES` — пары `[имя, base64]` (одна пара или список); `UF_BLOG_POST_FILE` — файлы Диска, `n<номер>`. В посте — до 10
  файлов.

## Рабочее время

GO24DEV-948, GO24DEV-978. `timeman.*` (чтение — `company:read`, изменения — `company:write`), с правами «Рабочего времени».

| Метод | Что |
|---|---|
| `timeman.status` | `USER_ID` (без него — вызывающий; чужой — руководителю и администратору) → `{STATUS, TIME_START, TIME_FINISH, DURATION, TIME_LEAKS, ACTIVE, IP_OPEN, IP_CLOSE, LAT_OPEN, LON_OPEN, LAT_CLOSE, LON_CLOSE, TZ_OFFSET}`; координаты — при включённом «Местоположении» (0 — неизвестно) |
| `timeman.open` | `LAT`, `LON` — где (запоминаются при включённом «Местоположении»); начинает день; на паузе — продолжает, завершённый — «Продолжить рабочий день», начатый — без изменений → состояние |
| `timeman.pause` | перерыв → состояние |
| `timeman.close` | `REPORT`, `LAT`, `LON` — завершает день с отчётом → состояние |
| `timeman.settings` | `USER_ID` → `{UF_TIMEMAN, UF_TM_FREE, UF_TM_MAX_START, UF_TM_MIN_FINISH, UF_TM_MIN_DURATION, UF_TM_REPORT_REQ, UF_TM_ALLOWED_DELTA, ADMIN}` по графику работы сотрудника или рабочему времени компании |
| `timeman.schedule.get` | `id` → `{ID, NAME, SCHEDULE_TYPE (FIXED, SHIFT, FLEXTIME), SHIFTS [{WORK_TIME_START, WORK_TIME_END, BREAK_DURATION, WORK_DAYS}], VIOLATION_RULES}`, время — секунды от полуночи |
| `timeman.networkrange.get` | офисные сети `[{ip_range, name}]`, `ip_range` — `первый-последний` |
| `timeman.networkrange.check` | `IP` → `{ip, range_name}`; вне офисов `range_name` — `false` |
| `timeman.networkrange.set` | `RANGES` `[{ip_range, name}]` — `первый-последний`, один адрес или подсеть — заменяет офисные сети (кто управляет рабочим временем) → `true` |
| `timeman.timecontrol.settings.get` | «Контроль времени»: `{active, minimum_idle_for_report, register_idle, report_request_type (all, none), …}` |
| `timeman.timecontrol.settings.set` | `ACTIVE`, `MINIMUM_IDLE_FOR_REPORT`, `REPORT_REQUEST_TYPE` (`all`, `user` — спрашивать, `none`); не переданное не меняется (кто управляет рабочим временем) → `true` |
| `timeman.timecontrol.report.add` | `REPORT_ID` — своё отсутствие, `TEXT`, `TYPE` (`WORK` — по умолчанию, `PRIVATE`) → `true` |
| `timeman.timecontrol.reports.get` | `USER_ID`, `YEAR`, `MONTH` (текущий месяц) → `{report: {days: [{index, date, workday_date_start, workday_date_finish, workday_time, workday_complete, reports: [{id, type, text, time_start, time_finish, duration}]}]}, user, info}` — дни с рабочим днём или отсутствиями; чужие — руководителю |
| `timeman.timecontrol.reports.settings.get` | `{user_id, user_admin, department_manager, department_id, departments [{id, name}], report_view_type (full, simple), minimum_idle_for_report, active}` |
| `timeman.timecontrol.reports.users.get` | `DEPARTMENT_ID` (с подотделами; без него — все) → сотрудники, чьи отчёты видит вызывающий, `[{id, name, position, avatar}]` |

- `STATUS`: `OPENED`, `PAUSED`, `CLOSED`, `EXPIRED` (не закрыт прошлый день); `DURATION` — отработано без перерывов,
  `TIME_LEAKS` — перерывы, оба `ЧЧ:ММ:СС`.
- `timeman.open`, `pause`, `close` с чужим `USER_ID` — только администратор. `TIME` (другое время) не принимается: день
  меняется сейчас, как на странице; «Только из офиса» и нарушения — как там.

## Приложения: события и места встраивания

GO24DEV-835. Вызывает только приложение своим токеном (`/rest/<метод>?auth=…`); входящий вебхук получает
`WRONG_AUTH_TYPE`. Подписывает и отписывает администратор.

| Метод | Что |
|---|---|
| `events` | события, на которые можно подписаться: `ONCRM<LEAD|DEAL|CONTACT|COMPANY|DYNAMICITEM><ADD|UPDATE|DELETE>`, `ONTASKADD`, `ONTASKUPDATE`, `ONTASKDELETE`, `ONUSERADD`, `ONAPPUNINSTALL`, `ONVOXIMPLANTCALLEND`, события чат-ботов |
| `event.get` | обработчики приложения `[{event, handler, auth_type, offline}]` |
| `event.bind` | `event`, `handler` (на домене приложения), `auth_type` (сотрудник, чьи токены получит обработчик; 0 — вызывающий) → `true` |
| `event.unbind` | `event`, `handler` (без него — все обработчики события) → `{count}` |
| `placement.list`, `get` | места; обработчики приложения `[{placement, handler, title, description, options}]` |
| `placement.bind`, `unbind` | `PLACEMENT`, `HANDLER`, `TITLE`, `DESCRIPTION` (или `LANG_ALL[<язык>][TITLE]`), `OPTIONS` → `true`; `{count}` |

Виджеты мессенджера (GO24DEV-977), как виджеты чата корпоративного портала:

| Место | Где | `PLACEMENT_OPTIONS` |
|---|---|---|
| `IM_TEXTAREA` | кнопка у поля ввода; фрейм открывается над ним размером `width`×`height` (100–800 × 50–600 пикс.) | `dialogId`, `roomId` |
| `IM_SIDEBAR` | кнопка в шапке чата; фрейм — в боковой панели | `dialogId`, `roomId` |
| `IM_CONTEXT_MENU` | пункт меню «…» сообщения после «Создать задачу / встречу / пост»; боковая панель | `dialogId`, `messageId`, `roomId`, `eventId` |
| `IM_NAVIGATION` | кнопка в шапке мессенджера; приложение вместо чата (`/im?app=<id>`) | — |

`OPTIONS`: `iconName` (Font Awesome: `fa-star` — значок go24 `star`, похожий или значок приложения), `context` — `USER`
(личные чаты с людьми, «Заметки»), `CHAT` (групповые чаты, каналы, чаты задач и коллабов), `CRM` (чат карточки CRM),
`LINES`, `ALL` (по умолчанию), несколько через `;`; `role` — `USER` (по умолчанию) или `ADMIN` (только
администраторам); `color` — названия корпоративного портала (`RED`, `AQUA`, `GRAPHITE`…); `width`, `height`; `extranet` — `Y`: видят и гости
коллабов (по умолчанию `N`). `dialogId` — `chatN` или номер собеседника (бота) личного чата, как `DIALOG_ID` в `im.*`;
фрейм открывается только в чате, где сотрудник состоит. Фрейм получает токены сотрудника, как вкладка карточки, в нём
работает go24.js (`closeApplication` закрывает панель).

Обработчик, подписанный через `event.bind` по `/rest/`, получает событие так, как его шлёт корпоративного портала: форма PHP-массивов
(`event=ONCRMDEALADD&data[FIELDS][ID]=5&ts=…&auth[access_token]=…`), запись — по номеру (сделки, задачи, сотрудника),
`auth[client_endpoint]` — `https://<go24>/rest/`, `auth[user_id]` — номер сотрудника. Обработчик, подписанный через REST
go24 (`POST /api/v1/events`), по-прежнему получает JSON с id go24.

## Приложения: настройки и хранилище данных

GO24DEV-979. Данные приложения: того, чей токен, а для входящего вебхука — собственного приложения вебхука, поэтому
данные вебхука хранятся отдельно.

| Метод | Что делает |
|---|---|
| `app.option.get` | `option` → значение (`null` — нет); без него — все `{имя: значение}` |
| `app.option.set` | `options` — `{имя: значение}`, `null` удаляет; администраторы → `true` |
| `user.option.get`, `set` | то же для собственных настроек вызывающего |
| `user.access` | `ACCESS` — коды доступа → `true`, если у вызывающего есть один из них |
| `access.name` | `ACCESS` → `{код: {provider, provider_id, name}}`; неизвестные коды пропускаются |
| `entity.add` | `ENTITY` (1–13 латинских букв, цифр, `_`), `NAME`, `ACCESS` — администраторы → `true` |
| `entity.get` | `ENTITY` → `{ID, ENTITY, NAME}`; без него — хранилища, которые вызывающему можно читать |
| `entity.update` | `ENTITY`, `NAME`, `ENTITY_NEW`, `ACCESS` |
| `entity.delete` | `ENTITY` — вместе с разделами, элементами и файлами |
| `entity.rights` | `ENTITY` → `{код: R|W|X}`; с `ACCESS` — сначала задаёт их |
| `entity.item.property.add`, `update`, `delete` | `ENTITY`, `PROPERTY`, `NAME`, `TYPE` (`S` — строка, `N` — число, `F` — файл), `PROPERTY_NEW`; новый код переносит значения, новый тип отбрасывает то, что не может хранить |
| `entity.item.property.get` | `ENTITY`, `PROPERTY` → `{PROPERTY, NAME, TYPE}`; без него — все |
| `entity.section.add`, `update`, `delete` | `ENTITY`, `ID`, `NAME`, `CODE`, `DESCRIPTION`, `SORT`, `ACTIVE`, `SECTION` (родительский) → номер; раздел удаляется вместе с вложенными разделами и элементами |
| `entity.section.get` | `ENTITY`, `FILTER`, `SORT`, `start` → `[{ID, CODE, NAME, DESCRIPTION, SECTION, SORT, ACTIVE, DEPTH_LEVEL, DATE_CREATE, CREATED_BY, TIMESTAMP_X, MODIFIED_BY}]` |
| `entity.item.add`, `update`, `delete` | `ENTITY`, `ID`, `NAME`, `CODE`, `ACTIVE`, `SORT`, `SECTION`, `DATE_ACTIVE_FROM`, `DATE_ACTIVE_TO`, `PREVIEW_TEXT`, `DETAIL_TEXT`, `PREVIEW_PICTURE`, `DETAIL_PICTURE`, `PROPERTY_VALUES` → номер; `update` меняет переданные свойства, остальные остаются |
| `entity.item.get` | `ENTITY`, `FILTER`, `SORT`, `start` → элементы с `PROPERTY_VALUES`, по 50 |

Коды доступа: `U<номер>` — сотрудник, `D<номер>` — сотрудники и руководитель отдела, `DR<номер>` — то же с
подотделами, `G1` — администраторы, `G2`, `AU`, `UA` — все. Права: `R` — чтение, `W` — ещё разделы и элементы, `X` —
ещё свойства, права и само хранилище; администраторам можно всё; по умолчанию `{"AU": "R"}`.

`FILTER` принимает поля и `PROPERTY_<код>` с операторами `=` (список — любое из значений), `!`, `!=`, `>`, `<`, `>=`,
`<=`, `%` (содержит), `!%`; пустое значение или `false` — не заполнено. Свойства `N` сравниваются как числа. `SORT` —
`{поле: ASC|DESC}`. Файл — `[имя, base64]` или `{fileData: [имя, base64]}` (до 10 МБ); `""` или `{remove: "Y"}`
удаляет его; `entity.item.get` отдаёт файл подписанной ссылкой на сутки — для того, кто может читать хранилище.

## Сотрудники и отделы

| Метод | Что |
|---|---|
| `user.fields` | поля сотрудника и их названия |
| `user.search` | `user.get` с условиями в `FILTER` или на верхнем уровне; ещё `FIND` (`NAME_SEARCH`) — слова имени, e-mail или должности, `WORK_POSITION`, `PERSONAL_CITY`, `UF_PHONE_INNER`, `UF_DEPARTMENT_NAME` |
| `user.add` | (`company:write`, кто может приглашать) `EMAIL`, `NAME`, `LAST_NAME`, поля карточки, `UF_DEPARTMENT` → номер сотрудника; приглашение уходит письмом, как из «Пригласить сотрудника»; `EXTRANET=Y` — отказ (гостей приглашают из коллабы) |
| `user.update` | `ID` и меняемые поля: карточку и её `UF_USR_*` — сам сотрудник или администратор; `UF_DEPARTMENT`, `ACTIVE` (`N` — уволить, `Y` — вернуть, в «Журнале событий») и внутренний номер при телефонии — администраторы |
| `user.userfield.list`, `get`, `add`, `update`, `delete` | свои поля профиля сотрудника, как `crm.<сущность>.userfield.*` (`ENTITY_ID` `USER`, коды `UF_USR_<код>` или `UF_USR_<номер>`, миграция 985); чтение — `company:read`, изменения — `company:write`, администратор (GO24DEV-985) |
| `department.get` | (область `department`, `company:read`) `[{ID, NAME, SORT, PARENT, UF_HEAD}]` — `ID`, `NAME`, `SORT`, `PARENT`, `UF_HEAD` параметрами или в `filter`; `sort` и `order`; по 50 |
| `department.fields` | поля отдела и их названия |
| `department.add` | `NAME`, `PARENT` (по умолчанию — под компанией), `UF_HEAD`, `SORT` → номер отдела |
| `department.update` | `ID` и меняемые поля; новый `PARENT` переносит отдел |
| `department.delete` | `ID` |

Отделы меняются с правами «Структура компании → Права доступа». Сотрудник из `user.add` есть в go24 ещё до регистрации —
в списках, выборе людей и задачах; первый вход с этим e-mail забирает запись себе (как у сотрудников, импортированных из
корпоративный портал), в ленте — приветствие нового сотрудника.

## Мессенджер

GO24DEV-839, 909, 915, 922, 937. Область `im` (`im:write` — уведомления и сообщения, `im:read` — чаты и сообщения).

| Метод | Что |
|---|---|
| `im.notify.system.add` | `USER_ID` (номер сотрудника), `MESSAGE`, `TAG` → номер уведомления: в колокольчике без автора, как системное |
| `im.notify.personal.add`, `im.notify` | то же от вызывающего |
| `im.notify.delete` | `ID` или `TAG` (`SUB_TAG`) — свои уведомления → `true` |
| `im.message.add` | `DIALOG_ID`, `MESSAGE` → номер сообщения; пишет вызывающий |
| `im.message.update` | `MESSAGE_ID`, `MESSAGE` (пусто — удалить) → `true`; своё сообщение |
| `im.message.delete` | `MESSAGE_ID` → `true` |
| `im.dialog.get`, `im.chat.get` | `DIALOG_ID` (или `CHAT_ID`) → `{id, dialog_id, name, owner, type, entity_type, entity_id, unread_count, …}` |
| `im.chat.get` | `ENTITY_TYPE=CRM`, `ENTITY_ID=DEAL\|12` (`LEAD`, `CONTACT`, `COMPANY`) → `{ID}` «Чата» карточки, `null` — его ещё нет |
| `im.chat.get` | другие `ENTITY_TYPE` и `ENTITY_ID` — к которым чат привязал `im.chat.add` → `{ID}` самого нового такого чата, где есть вызывающий, `null` — нет |
| `im.chat.user.list` | `CHAT_ID` → номера сотрудников в чате |
| `im.dialog.messages.get` | `DIALOG_ID`, `LIMIT` (до 50), `LAST_ID` (до него) или `FIRST_ID` (после него) → `{chat_id, messages: [{id, chat_id, author_id, date, text, params}], users: [{id, name, first_name, last_name, avatar, …}]}` |
| `im.chat.add` | `TYPE` (`CHAT`, `OPEN` — открытый чат, `CHANNEL`, `OPEN_CHANNEL`), `TITLE`, `DESCRIPTION`, `USERS` (номера сотрудников), `AVATAR` (картинка в base64), `MESSAGE` (первое сообщение), `OWNER_ID` (передать чат), `COLOR`, `ENTITY_TYPE` и `ENTITY_ID` (объект приложения, к которому привязан чат) → номер чата |
| `im.chat.user.add` | `CHAT_ID`, `USERS` → `true`, как позволяют права чата |
| `im.chat.user.delete` | `CHAT_ID`, `USER_ID` → `true`: владелец или администратор; себя — выход из чата |
| `im.chat.leave` | `CHAT_ID` → `true` |
| `im.chat.updateTitle`, `im.chat.updateAvatar` | `CHAT_ID`, `TITLE` / `AVATAR` (base64) → `true` |
| `im.chat.setOwner` | `CHAT_ID`, `USER_ID` (участник чата) → `true`; только владелец |
| `im.recent.get` | `SKIP_CHAT`, `SKIP_DIALOG`, `LAST_UPDATE` → чаты вызывающего, новые сверху: `[{id, chat_id, type (chat, user), title, counter, pinned, unread, message: {id, text, author_id, date}, date_update}]`; скрытые чаты не попадают |
| `im.recent.list` | то же постранично: `OFFSET`, `LIMIT` (до 50) → `{items, hasMore}` |
| `im.dialog.read` | `DIALOG_ID`, `MESSAGE_ID` (без него — весь чат) → `{dialogId, chatId, lastId, counter}` |
| `im.notify.read` | `ID`, `ONLY_CURRENT` (`Y` — только оно, иначе вместе с более старыми) → `true` |
| `im.notify.read.all` | → `true` |
| `im.notify.confirm` | `ID`, `NOTIFY_VALUE` — `Y` / `N`, для встречи ещё `tentative` → `true`: ответ на приглашение на встречу или в проект, на заявку в группу — как кнопки колокольчика (GO24DEV-896); ключу нужно ещё `calendar:write` или `projects:write` |
| `im.message.like` | `MESSAGE_ID`, `ACTION` — `plus`, `minus` или `auto` (наоборот, чем сейчас) → `true`; «Нравится» — реакция 👍 (GO24DEV-915) |
| `im.dialog.writing` | `DIALOG_ID` → `true`: остальные видят «печатает…» |
| `im.chat.mute` | `CHAT_ID`, `ACTION` — `Y` (по умолчанию) / `N` → `true`: чат без уведомлений для вызывающего |
| `im.recent.pin`, `im.recent.unread` | `DIALOG_ID`, `ACTION` `Y` / `N` → `true`: закрепить вверху списка чатов, отметить непрочитанным |
| `im.recent.hide` | `DIALOG_ID` → `true`: скрыть из списка чатов до нового сообщения |
| `im.counters.get` | → `{CHAT: {номер чата: n}, DIALOG: {номер сотрудника: n}, LINES: {}, TYPE: {ALL, NOTIFY, CHAT, DIALOG, LINES}}` — непрочитанное вызывающего; чаты без звука и скрытые не считаются |
| `im.user.get` | `ID` (без него — вызывающий) → сотрудник в полях мессенджера: `{id, active, name, first_name, last_name, work_position, avatar, gender, birthday (дд-мм), extranet, bot, status (online, away, dnd), idle, last_activity_date, departments, absent (последний день идущего отсутствия), phones}` |
| `im.user.list.get` | `ID` — номера → `{номер: сотрудник как в im.user.get или null}` |
| `im.user.status.get`, `im.user.status.set` | статус вызывающего: `online`, `away`, `dnd` («Не беспокоить»), как в его карточке; set — `STATUS` → `true` |
| `im.search.user.list` | `FIND` (не короче 3 символов), `OFFSET` → активные сотрудники по имени, e-mail или должности: `{номер: сотрудник как в im.user.get}`, до 50 |
| `im.search.chat.list` | `FIND` → групповые чаты и каналы вызывающего по названию: `{номер: {id, name, type (chat, channel), entity_type}}` |
| `im.notify.get` | `LIMIT` (до 50), `LAST_ID` (старше него) → `{notifications: [{id, author_id, date, notify_type (1 — с подтверждением, кнопки в `notify_buttons`; 2 — от человека; 4 — системное), notify_event, notify_read (Y/N), text, params.URL}], users (авторы как в im.user.get), total_count, total_unread_count}` (GO24DEV-922) |
| `im.notify.history.search` | `SEARCH_TEXT`, `SEARCH_TYPE` (модуль — `tasks`, `calendar`, `crm`, `im`, `socialnetwork`… или раздел настроек уведомлений go24), `SEARCH_DATE` (день), `LIMIT`, `LAST_ID` → как `im.notify.get` |
| `im.dialog.users.list` | `DIALOG_ID`, `SKIP_EXTERNAL` (`Y` — без гостей) → участники чата как в im.user.get |
| `im.dialog.unread` | `DIALOG_ID` → `true`: чат отмечен непрочитанным для вызывающего (целиком; `MESSAGE_ID` не нужен) |
| `im.department.get` | `ID` — номера, `USER_DATA` (`Y` — руководитель как в im.user.get в `manager_user_data`) → `[{id, name, full_name (отдел, вышестоящие, компания последней), manager_user_id}]` |
| `im.department.employees.get`, `im.department.managers.get` | `ID`, `USER_DATA` → `{номер отдела: [его активные сотрудники, руководитель первым] / [его руководитель]}` — номера или, с `USER_DATA=Y`, как в im.user.get |
| `im.department.colleagues.list` | `USER_DATA`, `LIMIT` (до 50), `OFFSET` → коллеги вызывающего: сотрудники его отделов и их руководители, у руководителя — ещё руководители подчинённых отделов; с `total` и `next` |
| `im.search.department.list` | `FIND` (не короче 3 символов) → отделы по названию, как в `im.department.get` |
| `im.revision.get` | → `{rest, web, mobile}` — ревизия методов `im.*` go24 |
| `im.disk.folder.get` | `CHAT_ID` или `DIALOG_ID` → `{ID}`: папка чата на Диске — «Сохранённые файлы» диска проекта для чата проекта (коллаба, задачи), если вызывающему можно туда писать, иначе — его «Моего диска»; там же «Прикрепить → Документ» хранит документы чата; загружать — `disk.folder.uploadfile` (GO24DEV-937) |
| `im.disk.file.commit` | `CHAT_ID` или `DIALOG_ID`, `FILE_ID` (`UPLOAD_ID`, `DISK_ID`) — файлы Диска, которые вызывающий видит, один или списком, — `MESSAGE` (уходит перед ними) → `{FILES: {uploadN: {id, chatId, date, type (image, file), name, extension, size, authorId, authorName, urlShow, urlDownload, …}}, DISK_ID, FILE_MODELS (файлы как `disk.file.get`), MESSAGE_ID — сообщение первого файла}`; каждый файл — отдельным сообщением |
| `im.disk.file.save` | `FILE_ID` → копия в «Сохранённых файлах» «Моего диска» вызывающего: `{folder: {id, name}, file: {id, name}}` |
| `im.disk.file.delete` | `CHAT_ID`, `FILE_ID` → `true`: сообщения, которыми `im.disk.file.commit` отправил файл, уходят из чата (свои; владелец и администраторы чата — любые); файл остаётся на своём диске |
| `im.chat.updateColor` | `CHAT_ID` (`DIALOG_ID`), `COLOR` — `RED`, `GREEN`, `MINT`, `LIGHT_BLUE`, `DARK_BLUE`, `PURPLE`, `AQUA`, `PINK`, `LIME`, `BROWN`, `AZURE`, `KHAKI`, `SAND`, `MARENGO`, `GRAY`, `GRAPHITE` → `true`, кому можно менять чат; `im.dialog.get` отдаёт его в `color` (`#f76187`); страницы go24 его не показывают |
| `im.message.share` | `MESSAGE_ID`, `TYPE` — `TASK`, `POST` (лента), `CALEND` (встреча на ближайший целый час с людьми чата), `CHAT` (новый чат с людьми чата) → `true`: из сообщения, как это делает его меню, — первая строка в заголовке, текст со ссылкой на сообщение, проект чата; на сообщение в чате приходит ответ со ссылкой |
| `im.notify.answer` | `NOTIFY_ID`, `ANSWER_TEXT` → `{result_message: [...]}`: ответ становится комментарием задачи, поста ленты или встречи уведомления; `NOTIFY_ANSWER_ERROR` — отвечать некуда |
| `im.notify.read.list` | `IDS` — уведомления вызывающего, `ACTION` — `Y` прочитаны (по умолчанию), `N` не прочитаны → `true` |
| `im.notify.schema.get` | виды уведомлений по модулям — `{модуль: {NAME, MODULE_ID, NOTIFY: {вид: {NAME, SITE, MAIL, PUSH, DISABLED}}}}`; `SITE` и `PUSH` — получает ли вызывающий этот вид (его настройки), `MAIL` всегда выключен |
| `im.smile.get` | смайлы мессенджера — `SETS` и `SMILE`; они Unicode: `TYPING` — сам символ, `IMAGE` нет |

- `DIALOG_ID` — `chatN` (чат по номеру) или номер сотрудника (личный чат с ним; если его нет — создаётся). Чат и
  сообщение Matrix получают номер, когда интеграция впервые их встречает (миграция 665).
- `MESSAGE` понимает BB-коды корпоративного портала (`[b]`, `[i]`, `[u]`, `[s]`, `[url]`, `[code]`, `[quote]`) и `[BR]`; уведомление —
  простой текст, коды убираются. Уведомление с тем же `TAG` заменяет прежнее от того же вызывающего; вид, отключённый
  сотрудником («Интеграции» в настройках уведомлений), не записывается — ответ `false`.
- `im.dialog.messages.get` отдаёт сообщения от новых к старым, с `FIRST_ID` — от старых к новым; строки о самом чате
  (добавлен, переименован) не входят.

### Чат-боты

GO24DEV-925, 937. Область `imbot`; зовёт только приложение (входящий вебхук получает `WRONG_AUTH_TYPE`), как и свой REST
ботов go24 (`/api/v1/imbot/…`). `BOT_ID` — номер бота из того же ряда, что номера сотрудников (бот корпоративного портала — пользователь:
это `DIALOG_ID` личного чата с ним); без `BOT_ID` — единственный бот приложения.

| Метод | Что |
|---|---|
| `imbot.register` | `CODE`, `TYPE` (`B`, `H`, `S` — получает все сообщения своих групповых чатов; `O` — как `B`), `EVENT_HANDLER` (все три сразу) или `EVENT_MESSAGE_ADD`, `EVENT_WELCOME_MESSAGE`, `EVENT_BOT_DELETE`, `PROPERTIES` (`NAME`, `LAST_NAME`, `WORK_POSITION`, `COLOR`) → `BOT_ID`; тот же `CODE` повторно — ошибка |
| `imbot.update` | `BOT_ID`, `FIELDS` — те же поля, меняется то, что передано → `true` |
| `imbot.unregister` | `BOT_ID` → `true`: бот уходит из мессенджера, обработчик получает `ONIMBOTDELETE`, сообщения остаются |
| `imbot.bot.list` | → `{номер: {ID, NAME, CODE, OPENLINE, TYPE}}` — боты приложения |
| `imbot.message.add` | `BOT_ID`, `DIALOG_ID` (`chatN` — чат, где есть бот, номер сотрудника — личный чат бота с ним), `MESSAGE` (BB-коды, `[BR]`), `KEYBOARD` (кнопки или `{BUTTONS: […]}`), `MENU`, `ATTACH` → номер сообщения |
| `imbot.message.update` | `BOT_ID`, `MESSAGE_ID`, `MESSAGE`, `KEYBOARD` (`N` — убрать кнопки), `MENU`, `ATTACH` → `true`; только своё сообщение бота |
| `imbot.message.delete` | `BOT_ID`, `MESSAGE_ID` → `true` |
| `imbot.command.register` | `BOT_ID`, `COMMAND`, `COMMON`, `HIDDEN`, `EXTRANET_SUPPORT` (`Y` / `N`), `EVENT_COMMAND_ADD`, `LANG` (`[{LANGUAGE_ID, TITLE, PARAMS}]`) → `COMMAND_ID` |
| `imbot.command.update` | `COMMAND_ID`, `FIELDS` — те же поля → `true` |
| `imbot.command.unregister` | `COMMAND_ID` → `true` |
| `imbot.command.answer` | `COMMAND_ID` (или `COMMAND` и `BOT_ID`), `MESSAGE_ID` — сообщение с командой из её события —, `MESSAGE`, `KEYBOARD`, `MENU`, `ATTACH` → номер ответа, в чате и треде команды |
| `imbot.chat.sendTyping` | `BOT_ID`, `DIALOG_ID` → `true`: остальные видят «печатает…» бота |
| `imbot.chat.leave` | `BOT_ID`, `CHAT_ID` → `true` |
| `imbot.chat.add` | `BOT_ID` и поля `im.chat.add` → номер чата: чат создаёт и ведёт бот (GO24DEV-937) |
| `imbot.chat.get` | `BOT_ID`, `ENTITY_TYPE`, `ENTITY_ID` → `{ID}` привязанного к ним чата бота, `null` — нет |
| `imbot.chat.updateTitle`, `imbot.chat.updateAvatar`, `imbot.chat.updateColor`, `imbot.chat.setOwner` | `BOT_ID` и поля метода `im.chat.*` → `true`, как позволяют боту права чата |
| `imbot.chat.setManager` | `BOT_ID`, `CHAT_ID`, `USER_ID`, `IS_MANAGER` (`Y` — администратор чата, `N` — больше нет) → `true`; владелец или администратор чата |
| `imbot.chat.user.add`, `imbot.chat.user.delete`, `imbot.chat.user.list` | `BOT_ID`, `CHAT_ID`, `USERS` / `USER_ID` → как `im.chat.user.*`; в списке есть и боты |
| `imbot.dialog.get` | `BOT_ID`, `DIALOG_ID` (`chatN` или номер сотрудника — личный чат бота с ним) → как `im.dialog.get` |
| `imbot.message.like` | `BOT_ID`, `MESSAGE_ID`, `ACTION` (`plus`, `minus`, `auto`) → `true`: 👍 бота |

- Обработчики, привязанные здесь, получают `ONIMBOTMESSAGEADD`, `ONIMBOTJOINCHAT`, `ONIMBOTDELETE` и `ONIMCOMMANDADD`
  так, как их шлёт корпоративного портала, — формой PHP-массивов, `data[PARAMS][DIALOG_ID]`, `data[BOT][<BOT_ID>]`,
  `data[COMMAND][<COMMAND_ID>]`, `data[USER]` — с номерами: `DIALOG_ID` — `chatN` или номер сотрудника, `CHAT_ID`,
  `MESSAGE_ID`, `FROM_USER_ID`, `TO_USER_ID` — номера. Обработчики своего REST go24 получают идентификаторы go24.

### Чат-боты 2.0

GO24DEV-954, 961. `imbot.v2.*`, область `imbot`; вызывает приложение (OAuth) — или входящий вебхук с `botToken` (ниже), —
параметры и ответы — camelCase с номерами:
`botId`, `dialogId` (`chatN` или номер человека — личный чат бота с ним), `messageId`, `commandId`, поля — в `fields`.
Бот тот же, что у `imbot.*` (аккаунт Matrix), но события у него imbot.v2: `eventMode` `fetch` — приложение забирает их
`imbot.v2.Event.get` (хранятся 24 часа), `webhook` — отправляются на `webhookUrl` (на хосте самого приложения),
`data.bot` — `{id, code, auth}`.

| Метод | Что |
|---|---|
| `imbot.v2.Bot.register` | `fields`: `code`, `type` (`bot`, `supervisor` — все сообщения групповых чатов, `personal`, `openline`), `eventMode`, `webhookUrl`, `botToken`, `isHidden` (нет в выборе людей), `isReactionsEnabled`, `properties` (`name`, `lastName`, `workPosition`, `color`) → `{bot, users}`; тот же `code` повторно — тот же бот |
| `imbot.v2.Bot.update`, `get` (`botId` или `code`), `list` (`filter.type`, `limit`, `offset` → `hasNextPage`), `unregister` | |
| `imbot.v2.Event.get` | `botId`, `offset`, `limit` (100, до 1000) → `{events: [{eventId, type, date, data}], nextOffset, hasMore}`; `offset` подтверждает события до него |
| `imbot.v2.Chat.Message.send`, `update`, `delete`, `get` | `fields`: `message`, `keyboard`, `menu`, `attach` → `{id, uuidMap, failureMap}`, `{result}`, `{message, user}` |
| `imbot.v2.Chat.InputAction.notify` | `dialogId` → остальные видят «печатает…» бота |
| `imbot.v2.Chat.add` | `fields`: `type` (`chat`, `open`), `title`, `description`, `color`, `avatar` (base64), `userIds`, `ownerId`, `message`, `entityType`, `entityId` → `{chat, users}`; бот в чате |
| `imbot.v2.Chat.get`, `update` | `dialogId` — чат, где есть бот → `{chat, users}`; `update` — `chatN`, `fields` `title`, `description`, `color`, `avatar`, как позволяют права бота в чате |
| `imbot.v2.Chat.User.list` | `dialogId`, `order.userId`, `limit` (50, до 200) → участники чата (`User`) |
| `imbot.v2.Chat.Message.read` | `dialogId`, `messageId` (или всё) → `{chatId, lastId, counter, viewedMessages}` |
| `imbot.v2.Chat.Message.getContext` | `messageId`, `range` (1–50) → `{messages, users, hasPrevPage, hasNextPage}`, от старых к новым |
| `imbot.v2.Chat.Message.Reaction.add`, `delete` | `messageId`, `reaction` — код корпоративного портала (`like`, `fire`, `redHeart`…) → `{result}` |
| `imbot.v2.Command.register`, `update`, `unregister`, `list`, `answer` | `fields`: `command`, `title` и `params` по языкам, `common`, `hidden`, `extranetSupport` |
| `imbot.v2.Chat.TextField.enabled` | `dialogId`, `enabled` (по умолчанию `true`) → `{result}`; выключено — на странице чата вместо поля ввода подсказка, кнопки бота отвечают по-прежнему; у чата `textFieldEnabled` `N` |
| `imbot.v2.File.upload` | `dialogId`, `fields`: `name`, `content` (base64), `message` (текст перед файлом) → `{file: {id, chatId, name, extension, size}, messageId, chatId, dialogId}` |
| `imbot.v2.File.download` | `fileId` — файл чата, где есть бот → `{downloadUrl}`: подписанный адрес go24, на час |
| `imbot.v2.Revision.get` | как `im.revision.get` |

- События: `ONIMBOTV2MESSAGEADD`, `ONIMBOTV2MESSAGEUPDATE`, `ONIMBOTV2MESSAGEDELETE` (личные чаты; у supervisor — все
  чаты; правка — и когда в ней упомянут бот), `ONIMBOTV2JOINCHAT`, `ONIMBOTV2COMMANDADD`, `ONIMBOTV2REACTIONCHANGE`
  (реакцию на сообщение бота поставили или сняли, если `isReactionsEnabled`), `ONIMBOTV2DELETE`.
- `ONIMBOTV2CONTEXTGET`: ссылка go24 `/online/?IM_DIALOG={номер бота}&BOT_CONTEXT={JSON}` (или `/im?…` с тем же)
  открывает чат сотрудника с ботом и передаёт боту `{dialogId, context, chat, user}`; контекст в адресе не остаётся.
  `im.v2.Chat.Bot.sendContext` (`dialogId` — номер бота, `context`) — то же через REST. Один `IM_DIALOG` — `chatN` или
  номер коллеги — просто открывает этот чат.
- Файл в чате — отдельное сообщение: `fileId` — номер этого сообщения, его даёт `message.params.FILE_ID` событий.
  Вызов REST — не больше 8 МБ, столько же и файл в base64.
- Входящий вебхук (личный ключ API с областью `imbot`) вызывает `imbot.v2.*` с `botToken`: `Bot.register` берёт его в
  `fields.botToken` (или отдельно), остальные методы — отдельно; без него — `BOT_TOKEN_NOT_SPECIFIED`, с чужим —
  `BOT_OWNERSHIP_ERROR`. Боты вебхука принадлежат его собственному приложению, оно создаётся при первом `Bot.register`
  (с именем ключа, в «Приложениях»); `imbot.*` остаётся только для приложений.

### im.v2

GO24DEV-961, область `im`; вызывающий действует от своего имени.

| Метод | Что |
|---|---|
| `im.v2.Event.subscribe`, `unsubscribe` | → `true`: go24 начинает (перестаёт, накопленное удаляется) записывать события чатов вызывающего |
| `im.v2.Event.get` | `offset`, `limit` (100, до 1000) → `{events: [{eventId, type, date, data}], nextOffset, hasMore}`; хранятся 24 часа |
| `im.v2.File.upload` | `dialogId`, `fields` (`name`, `content`, `message`) → как `imbot.v2.File.upload`, файл от вызывающего |
| `im.v2.File.download` | `dialogId`, `fileId` → `{downloadUrl}` |
| `im.v2.Chat.Bot.sendContext` | `dialogId` — номер бота, `context` → `{result}`; бот получает `ONIMBOTV2CONTEXTGET` |

- События: `ONIMV2MESSAGEADD`, `ONIMV2MESSAGEUPDATE` (`message`, `chat`, `user` — автор), `ONIMV2MESSAGEDELETE`
  (`messageId`), `ONIMV2REACTIONCHANGE` (`reaction`, `action` `add` или `delete`, `message`, `user` — чья реакция),
  `ONIMV2JOINCHAT` (`dialogId`, `user` — кто вошёл); у всех — `chat` и `language`. Воркер go24 читает чаты подписанного
  сотрудника от его имени с момента подписки.

## Бизнес-процессы

`bizproc.workflow.template.*`, `bizproc.workflow.*`, `bizproc.task.*` (область `bp` → `bizproc`; списки — `bp:read`,
остальное — `bp:write`) — GO24DEV-855. Права — как в REST бизнес-процессов go24: процессы и задания вызывающего, ключ
администратора администрирует.

| Метод | Что |
|---|---|
| `bizproc.workflow.template.list` | шаблоны, которые вызывающий может запускать (администратор — все, и неактивные): `ID`, `XML_ID` (uuid go24), `MODULE_ID`, `ENTITY`, `DOCUMENT_TYPE`, `AUTO_EXECUTE` (0, 1 — при добавлении, 2 — при изменении, 3), `NAME`, `DESCRIPTION`, `PARAMETERS`, `VARIABLES`, `CONSTANTS` (`{ключ: {Name, Type, Required, Multiple, Default, Options}}`), `MODIFIED`, `ACTIVE` |
| `bizproc.workflow.template.delete` | `ID` → `true`; шаблон удаляется вместе с процессами (администраторы) |
| `bizproc.workflow.start` | `TEMPLATE_ID`, `DOCUMENT_ID` (у процесса без документа — не нужен), `PARAMETERS` (параметр «Файл» — `[имя, base64]` или список таких) → id процесса |
| `bizproc.workflow.terminate` | `ID`, `STATUS` (причина) → `true`, как «Остановить» |
| `bizproc.workflow.kill` | `ID` → `true`; процесс удаляется с историей (администраторы) |
| `bizproc.workflow.instances`, `bizproc.workflow.instance.list` | выполняющиеся процессы (администратор — все, иначе свои): `ID`, `MODIFIED`, `OWNED_UNTIL`, `MODULE_ID`, `ENTITY`, `DOCUMENT_ID`, `STARTED`, `STARTED_BY`, `TEMPLATE_ID`, `WORKFLOW_STATE` (статус шага «Установить статус») |
| `bizproc.task.list` | задания, ждущие вызывающего (администратор — и по `filter[USER_ID]`): `ID`, `XML_ID`, `WORKFLOW_ID`, `DOCUMENT_NAME`, `NAME`, `DESCRIPTION`, `MODIFIED`, `WORKFLOW_STARTED`, `WORKFLOW_STARTED_BY`, `WORKFLOW_TEMPLATE_ID`, `WORKFLOW_TEMPLATE_NAME`, `WORKFLOW_STATE`, `STATUS`, `USER_ID`, `USER_STATUS`, `MODULE_ID`, `ENTITY`, `DOCUMENT_ID`, `ACTIVITY`, `ACTIVITY_NAME`, `PARAMETERS` (`Fields` запроса), `DOCUMENT_URL` |
| `bizproc.task.complete` | `TASK_ID`, `STATUS`, `COMMENT`, `FIELDS` → `true` |
| `bizproc.task.delegate` | `TASK_IDS`, `FROM_USER_ID`, `TO_USER_ID` → `true`, если «Делегирование» шага позволяет |
| `bizproc.activity.add` | `CODE`, `HANDLER` (на домене приложения), `AUTH_USER_ID`, `USE_SUBSCRIPTION`, `NAME`, `DESCRIPTION`, `PROPERTIES`, `RETURN_PROPERTIES` → `true` (токен приложения администратора) |
| `bizproc.activity.update` | `CODE`, `FIELDS` (как у `add`; остальное не меняется) → `true` |
| `bizproc.activity.delete` | `CODE` → `true`; шаги этого действия дальше завершаются ошибкой |
| `bizproc.activity.list` | коды действий приложения |
| `bizproc.event.send` | `EVENT_TOKEN`, `RETURN_VALUES`, `LOG_MESSAGE` → `true`; ждущий процесс идёт дальше |
| `bizproc.activity.log` | `EVENT_TOKEN`, `LOG_MESSAGE` → `true`, строка в журнале процесса |

- Номера: у шаблонов и заданий — номера (миграция 720), даются один раз и не повторяются; uuid go24 тоже принимается.
  Процесс — его uuid go24: `WORKFLOW_ID` в корпоративном портале тоже строка.
- `DOCUMENT_ID`: `["crm", "CCrmDocumentDeal", "DEAL_12"]` (`LEAD_`, `CONTACT_`, `COMPANY_` — у `CCrmDocumentLead`,
  `CCrmDocumentContact`, `CCrmDocumentCompany`; смарт-процесс —
  `["crm", "Legacy\\Crm\\Integration\\BizProc\\Document\\Dynamic", "DYNAMIC_<тип>_<номер>"]`), элемент списка —
  `["lists", "BizprocDocument", "<id go24>"]`, файл Общего диска — `["disk", "Legacy\\Disk\\BizProcDocument", "<id go24>"]`;
  отдельный процесс — без документа. Документ не того вида, что у шаблона, — `ERROR_WRONG_DOCUMENT_ID`.
- `PARAMETERS`: по ключам полей шаблона; сотрудник — `user_<номер>`, номер или uuid go24; «Да/Нет» — `Y`/`N`; список
  значений склеивается через запятую.
- `ACTIVITY`: `ApproveActivity`, `ReviewActivity`, `RequestInformationActivity`, `RequestInformationOptionalActivity`
  (запрос, который можно отклонить). `STATUS` у `complete`: утверждение — `1`/`yes` или `2`/`no`, ознакомление —
  `3`/`ok`, запрос — `3`/`ok` с `FIELDS` или `4`/`cancel` (`2`/`no`); иначе — `ERROR_WRONG_STATUS`.
- `filter` списков — равенство (список — любое из, `!` — не равно), `order` — одно поле, `select`, `start`.
- Действия приложений (GO24DEV-903) — только для токена приложения. `PROPERTIES` и `RETURN_PROPERTIES`:
  `{ключ: {Name, Description, Type, Required, Multiple, Default, Options}}`, `Type` — `string`, `text`, `int`, `double`,
  `bool`, `date`, `datetime`, `select`, `user`; `NAME` и `Name` — строка или `{"ru": …, "en": …}`. Конструктор
  предлагает действие в группе «Действия приложений». Шаг отправляет на `HANDLER` форму массивов PHP: `workflow_id`,
  `code`, `document_id`, `document_type`, `event_token`, `properties[…]` (сотрудник — `user_<номер>`),
  `use_subscription`, `timeout_duration` (секунды, `0` — без срока), `ts` и `auth` (токены `AUTH_USER_ID`, иначе
  запустившего процесс; `client_endpoint` — `/rest/`). С `USE_SUBSCRIPTION` = `Y` процесс ждёт `bizproc.event.send` (не
  дольше времени шага); `RETURN_VALUES` — поля `app_<код>_<ключ>`, их проверяют условия. Токен с ответом, просроченный
  или чужого приложения — `ERROR_WRONG_EVENT_TOKEN`.
- Нет: `bizproc.workflow.template.add`/`update` (корпоративный портал принимает файлы `.bpt` своего формата — шаблоны переносятся
  «Импортом» конструктора или `POST /api/v1/bp/templates`) и `bizproc.robot.*`.

## Календарь

| Метод | Что |
|---|---|
| `calendar.event.get` | (область `calendar:read`) встречи календаря: `type` — `user` (`ownerId` — номер сотрудника, по умолчанию вызывающий), `group` (номер проекта), `company_calendar`; `from`, `to` (по умолчанию от месяца назад до трёх месяцев вперёд), `section[]` — только этих календарей; повторяющаяся встреча — каждым повторением |
| `calendar.event.getbyid` | `id` → встреча |
| `calendar.event.get.nearest` | встречи вызывающего на ближайшие `days` (по умолчанию 60), не больше `maxEventsCount` |
| `calendar.event.add` | (`calendar:write`) `type`, `ownerId`, `name`, `from`, `to`, `skip_time`, `timezone_from`, `section`, `description`, `location`, `color`, `accessibility`, `importance`, `private_event`, `remind`, `rrule`, `attendees` → номер встречи |
| `calendar.event.update` | `id` и меняемые поля; `recursion_mode` — `this` (одно повторение, `current_date_from`), `next` (это и следующие), по умолчанию `all` |
| `calendar.event.delete` | `id`: организатор удаляет встречу, приглашённый — отказывается от неё |
| `calendar.meeting.status.get` | `eventId` → `Y`, `N`, `Q` или `H` (организатор) |
| `calendar.meeting.status.set` | `eventId`, `status` — `Y`, `N`, `Q` |
| `calendar.accessibility.get` | `users[]`, `from`, `to` → `{номер: [{ID, NAME, DATE_FROM, DATE_TO, ACCESSIBILITY, IMPORTANCE}]}` — занятость, как в планировщике встреч; у чужой личной встречи нет названия |
| `calendar.section.get` | `type`, `ownerId` → календари: `ID`, `NAME`, `COLOR`, `CAL_TYPE`, `OWNER_ID`, `PERM` (`view_time`, `view_title`, `view_full`, `add`, `edit`, `edit_section`, `access`) |
| `calendar.section.add` | `name`, `color` → свой календарь |
| `calendar.section.update` | `id`, `name`, `color`; у основного календаря — только цвет |
| `calendar.section.delete` | `id` — свой календарь вместе со встречами; основной — `ACCESS_DENIED` |

- Поля встречи: `ID`, `PARENT_ID`, `CAL_TYPE`, `OWNER_ID`, `NAME`, `DESCRIPTION`, `LOCATION`, `DATE_FROM`, `DATE_TO`
  (`дд.мм.гггг чч:мм:сс` в поясе встречи, у встречи на весь день — только дата), `TZ_FROM`, `TZ_TO`, `DT_SKIP_TIME`,
  `DT_LENGTH`, `DATE_FROM_TS_UTC`, `DATE_TO_TS_UTC`, `SECT_ID`, `ACCESSIBILITY` (`busy`, `quest`, `free`, `absent`),
  `IMPORTANCE`, `PRIVATE_EVENT`, `COLOR`, `IS_MEETING`, `MEETING_STATUS`, `MEETING_HOST`, `CREATED_BY`, `ATTENDEE_LIST`,
  `ATTENDEES_CODES` (`U<номер>`), `REMIND` (`[{type: "min", count}]`), `RRULE` (`FREQ`, `INTERVAL`, `BYDAY`, `COUNT`,
  `UNTIL`), у повторения — `DATE_FROM_ORIGINAL` и `EXDATE`.
- `from`/`to` — ISO 8601, `дд.мм.гггг чч:мм[:сс]` или дата; `remind` — `[{type: "min"|"hour"|"day", count}]`;
  `rrule` — `{FREQ: DAILY|WEEKLY|MONTHLY|YEARLY, INTERVAL, BYDAY: ["MO", …], COUNT, UNTIL}`.
- Права — как у страниц календаря: свой календарь и те, чьи владельцы дали «Управление»; проекта — его участники;
  компании — читают и добавляют все сотрудники (не гости); встречу меняет её организатор. Приглашённый видит встречу в своём основном календаре, со своим `OWNER_ID`.
- Номера: у встреч и календарей есть номера (миграция 735), выдаются один раз и не переиспользуются; календарь получает
  номер, когда интеграция впервые его видит. Задачи, отсутствия, дни рождения и дела CRM в календаре — не встречи, их
  нет в ответах.
- Нет: `calendar.resource.*` (переговорные бронируются в «Переговорных»), `calendar.settings.*` и
  `calendar.user.settings.*`.

## Диск

GO24DEV-886; область `disk` (`disk:read`, изменения — `disk:write`).

| Метод | Что делает |
|---|---|
| `disk.storage.getlist` | хранилища вызывающего: «Мой диск» (`ENTITY_TYPE` `user`, `ENTITY_ID` — номер сотрудника), «Общий диск» (`common`), диски его проектов (`group`, номер проекта); `filter` по `ID`, `NAME`, `ENTITY_TYPE`, `ENTITY_ID` |
| `disk.storage.get`, `getfields`, `gettypes` | `id` → `{ID, NAME, CODE, MODULE_ID, ENTITY_TYPE, ENTITY_ID, ROOT_OBJECT_ID}` |
| `disk.storage.getchildren` | `id`, `filter`, `start` → папки и файлы верхнего уровня диска, сначала папки, по 50 |
| `disk.storage.addfolder`, `disk.folder.addsubfolder` | `id`, `data[NAME]` → новая папка |
| `disk.storage.uploadfile`, `disk.folder.uploadfile` | `id`, `data[NAME]`, `fileContent` (`[имя, base64]`), `generateUniqueName` → новый файл; без `fileContent` → `{field: "file", uploadUrl}`: POST поля формы `file` на `uploadUrl` возвращает `{result: файл}` |
| `disk.folder.get`, `getchildren`, `getfields` | `id` (`ROOT_OBJECT_ID` хранилища — тоже папка) |
| `disk.folder.rename`, `disk.file.rename` | `id`, `newName` |
| `disk.folder.moveto`, `copyto`, `disk.file.moveto`, `copyto` | `id`, `targetFolderId` — этого диска или другого; `copyto` возвращает копию («имя (2)», если имя занято) |
| `disk.folder.markdeleted`, `restore`, `disk.file.markdeleted`, `restore` | в корзину диска и обратно; у удалённого `DELETED_TYPE` `3`, `DELETED_BY`, `DELETE_TIME` |
| `disk.folder.deletetree`, `disk.file.delete` | навсегда, с диска или из корзины → `true` |
| `disk.file.get`, `getfields` | `id` → файл |
| `disk.file.getversions` | `id` → версии, новая первой: `ID` (место в истории с 1), `OBJECT_ID`, `SIZE`, `NAME`, `CREATE_TIME`, `CREATED_BY`, `DOWNLOAD_URL` |
| `disk.file.uploadversion` | `id`, `fileContent` (или выданный `uploadUrl`) → файл с новой версией |
| `disk.file.restorefromversion` | `id`, `versionId` — версия из `getversions` снова становится текущей |

- Поля папки: `ID`, `NAME`, `CODE`, `STORAGE_ID`, `TYPE` (`folder`), `REAL_OBJECT_ID`, `PARENT_ID`, `DELETED_TYPE`,
  `CREATE_TIME`, `UPDATE_TIME`, `DELETE_TIME`, `CREATED_BY`, `UPDATED_BY`, `DELETED_BY`, `DETAIL_URL`; у файла `TYPE`
  `file` и ещё `GLOBAL_CONTENT_VERSION` (сколько версий), `FILE_ID`, `SIZE`, `DOWNLOAD_URL`.
- `DOWNLOAD_URL` и `uploadUrl` — подписанные ссылки go24 (`/rest/download?token=…`, `/rest/upload?token=…`): работают
  без ключа сутки, пока сотрудник работает и может читать (писать) там, с адресов, которые пускает «Ограничение
  доступа по IP».
- Имя, которое в папке уже есть, — ошибка `DISK_OBJ_22000`; `generateUniqueName=Y` даёт «имя (2)».
- Права — как у страниц Диска: свой «Мой диск», «Общий диск» (папки, закрытые правами, не видны), диски своих
  проектов; чужой «Мой диск» — администратор в режиме администратора.
- Номера: папки и файлы нумеруются из одной последовательности (миграция 772), номер выдаётся один раз и не
  переиспользуется; хранилище и его `ROOT_OBJECT_ID` получают номера, когда интеграция впервые видит диск.
- `fileContent` передаётся в теле вызова (до 8 МБ); больший файл — через `uploadUrl` (до предела загрузки Диска). Нет:
  `disk.storage.getforapp`, `disk.attachedObject.*`, внешние ссылки и права `disk.folder.sharetouser`.

## Телефония

GO24DEV-921; область `telephony` (`telephony:read`, изменения — `telephony:write`). Методы внешней АТС (облачной АТС,
АТС SIP-оператора), которая сообщает go24 о своих звонках, как «Интеграция с внешней АТС», и журнал звонков.

| Метод | Что делает |
|---|---|
| `telephony.externalcall.register` | `USER_ID` (номер) или `USER_PHONE_INNER`, `PHONE_NUMBER`, `TYPE` (1 исходящий, 2 входящий, 3 входящий с перенаправлением, 4 обратный, 5 информационный), `CALL_START_DATE`, `LINE_NUMBER`, `CRM_CREATE` (по умолчанию 1), `CRM_SOURCE` (источник лида, по умолчанию `CALL`), `CRM_ENTITY_TYPE` + `CRM_ENTITY_ID`, `SHOW` (по умолчанию 1) → `{CALL_ID, CRM_ENTITY_TYPE, CRM_ENTITY_ID, CRM_CREATED_LEAD, CRM_CREATED_ENTITIES}`: клиент ищется по телефону (контакт, компания, лид), незнакомый номер создаёт лид (если линия разрешает), сотруднику — звонок в колокольчике со ссылкой на карточку |
| `telephony.externalcall.finish` | `CALL_ID`, `USER_ID` (кто говорил), `DURATION`, `STATUS_CODE` (`200` отвечен, `304` пропущен, `486` занято, `603` отклонён, прочие — ошибка), `FAILED_REASON`, `COST`, `COST_CURRENCY`, `RECORD_URL`, `VOTE` (1–5) → звонок, как его отдаёт `voximplant.statistic.get`: он попадает в журнал, историю клиента, триггеры «Входящий/Пропущенный звонок», уведомление о пропущенном, `ONVOXIMPLANTCALLEND` и вебхук `call.ended`; запись расшифровывается, если это включено |
| `telephony.externalcall.attachRecord` | `CALL_ID`, `FILENAME` + `FILE_CONTENT` (base64) или `RECORD_URL` → `{CALL_ID, FILE_NAME, CALL_RECORD_URL}`; до или после `finish`; mp3, wav, ogg, m4a до 100 МБ |
| `telephony.externalcall.show`, `hide` | `CALL_ID`, `USER_ID` (один или список; без него — сотрудник звонка) → `true`: звонок в колокольчике показывается снова или убирается |
| `telephony.externalCall.searchCrmEntities` | `PHONE_NUMBER` → `[{CRM_ENTITY_TYPE, CRM_ENTITY_ID, NAME, ASSIGNED_BY_ID, ASSIGNED_BY: {ID, USER_PHONE_INNER}}]` — карточки, которые видит владелец ключа |
| `telephony.externalLine.get` | → `[{NUMBER, NAME, CRM_AUTO_CREATE}]` |
| `telephony.externalLine.add`, `update` | `NUMBER`, `NAME`, `CRM_AUTO_CREATE` (`Y`/`N`: незнакомый номер линии создаёт лид) → `{ID}` (номер) |
| `telephony.externalLine.delete` | `NUMBER` |
| `voximplant.statistic.get` | журнал звонков: `filter` по полям, `>=CALL_START_DATE` и `<=CALL_START_DATE` (период), `order`, `select`, `start` → `ID`, `CALL_ID`, `PORTAL_USER_ID`, `PORTAL_NUMBER`, `PHONE_NUMBER`, `CALL_TYPE` (1 исходящий, 2 входящий), `CALL_DURATION`, `CALL_START_DATE`, `CALL_STATUS`, `CALL_FAILED_CODE`, `CALL_FAILED_REASON`, `CALL_VOTE`, `COST`, `COST_CURRENCY`, `CALL_RECORD_URL`, `CRM_ENTITY_TYPE`, `CRM_ENTITY_ID`, `COMMENT` |

- Права: сотрудник регистрирует, показывает и завершает свои звонки; для другого сотрудника и для линий владелец ключа
  должен иметь право менять настройки телефонии (администратор или роль с «Настройками телефонии»).
  `voximplant.statistic.get` показывает звонки по «Правам доступа» телефонии (свои, отдела, все); ссылка на запись — тому,
  кто может её слушать.
- Звонки журнала пронумерованы (миграция 840); `CALL_ID` своих звонков go24 — id Asterisk, звонков внешней АТС —
  `externalCall.<hex>.<время>`. `voximplant.statistic.get` просматривает последние 5000 звонков периода.
- Линия, не добавленная через `externalLine.add`, создаёт лиды (как с включённым «Создавать в CRM»). Записи звонков внешней
  АТС хранятся там же, где записи Asterisk; `RECORD_URL` загружается только из интернета.
- Нет: `telephony.externalcall.attachRecord` через `uploadUrl`, `CRM_ACTIVITY_ID` (go24 хранит звонок в истории, а не
  делом), `voximplant.*` собственных линий и SIP-аккаунтов Voximplant (АТС go24 — Asterisk, см. `/api/v1/telephony`).

## Открытые линии

GO24DEV-930; область `imopenlines` (`openlines:read`, действия — `openlines:write`). Линии и диалоги «Открытых линий»,
как их отдаёт корпоративного портала; права — как на странице: оператор видит свои диалоги, неразобранные своих линий, те, куда его
пригласили, и диалоги подчинённых; администраторы и руководители линий — все. Диалог — один номер и для `CHAT_ID`, и
для `SESSION_ID` (миграция 866); понимаются и `DIALOG_ID` `chatN`, и `USER_CODE` `коннектор|линия|N|…`. Следующее
обращение клиента — новый номер.

| Метод | Что делает |
|---|---|
| `imopenlines.config.list.get` | `PARAMS[filter][ACTIVE]`, `PARAMS[filter][ID]`, `OPTIONS[QUEUE]` = `Y` (с очередями) → линии |
| `imopenlines.config.get` | `CONFIG_ID`, `WITH_QUEUE` (по умолчанию `Y`) → `ID`, `ACTIVE`, `LINE_NAME`, `QUEUE` (номера операторов), `QUEUE_TYPE` (`all`, `evenly`, `strictly`), `QUEUE_TIME`, `NO_ANSWER_TIME`, `CRM_CREATE` (`lead`, `none`), `CRM_FORWARD`, `WORKTIME_ENABLE`, `WORKTIME_FROM`/`TO` (`9.30`), `WORKTIME_DAYOFF` (`SA`, `SU`…), `WORKTIME_DAYOFF_TEXT`, `WELCOME_MESSAGE_TEXT`, `NO_ANSWER_TEXT`, `CLOSE_TEXT`, `AUTO_CLOSE_TIME`, `VOTE_MESSAGE`, `VOTE_MESSAGE_1_TEXT`, `AGREEMENT_MESSAGE`, `MAX_CHAT`, `TYPE_MAX_CHAT` (`ANSWERED`, `CLOSED`), `KPI_FIRST_ANSWER_TIME`, `KPI_FURTHER_ANSWER_TIME`, `HISTORY_SEND_ON`; время — в секундах |
| `imopenlines.config.add` | `PARAMS` — поля выше → номер новой линии (администраторы) |
| `imopenlines.config.update` | `CONFIG_ID`, `PARAMS` — меняются только переданные → `true` (администраторы) |
| `imopenlines.config.delete` | `CONFIG_ID` → `true` (администраторы) |
| `imopenlines.config.path.get` | → `{SERVER_ADDRESS, PUBLIC_PATH}` — где настраиваются линии |
| `imopenlines.dialog.get` | `CHAT_ID` / `SESSION_ID` / `DIALOG_ID` / `USER_CODE` → `{id, dialog_id, name, owner (номер оператора, 0 — нет), type: lines, entity_id (коннектор\|линия\|чат\|0), entity_data_1 (Y\|LEAD\|номер\|…), status, spam, pinned, waiting, connector_id, connector_title, client: {name, phone, email, page}}` |
| `imopenlines.session.history.get` | `CHAT_ID` или `SESSION_ID` → `{chatId, sessionId, canJoin, canVoteAsHead, sessionVoteHead, sessionCommentHead, message: {id: {id, chat_id, author_id (0 — клиент), date, text, system, params}}, usersMessage, users}`; диалог остаётся непрочитанным |
| `imopenlines.operator.answer` | `CHAT_ID` — владелец ключа берёт диалог |
| `imopenlines.operator.skip` | `CHAT_ID` — оператор отказывается от своего диалога: он уходит следующему в очереди |
| `imopenlines.operator.transfer` | `CHAT_ID`, `TRANSFER_ID` — номер сотрудника или `queueN` (линия N) |
| `imopenlines.operator.spam` | `CHAT_ID` — закрыть как спам |
| `imopenlines.operator.finish` | `CHAT_ID` — оператор закрывает свой диалог |
| `imopenlines.operator.another.finish` | `CHAT_ID` — чужой (администраторы, руководители линий) |
| `imopenlines.session.intercept` | `CHAT_ID` — «Перехватить диалог» |
| `imopenlines.session.start` | `CHAT_ID` — закрытый диалог открывается снова |
| `imopenlines.session.join` | `CHAT_ID` — владелец ключа входит в видимый ему диалог приглашённым коллегой (GO24DEV-1008) |
| `imopenlines.session.mode.pin` | `CHAT_ID`, `ACTIVATE` (`Y`/`N`) — «Закрепить за собой» |
| `imopenlines.session.mode.pinAll`, `unpinAll` | → номера открытых диалогов владельца ключа, которые закреплены или откреплены |
| `imopenlines.session.mode.silent` | → `true`; скрытое сообщение — `crm.message.add` с `HIDDEN` = `Y` |
| `imopenlines.session.head.vote` | `SESSION_ID`, `RATING` (4–5 — 👍, 1–2 — 👎, 0 — снять), `COMMENT` — оценка руководителя (администраторы, руководители линий, руководители оператора) |
| `imopenlines.crm.chat.get` | `CRM_ENTITY_TYPE` (`LEAD`, `CONTACT`), `CRM_ENTITY`, `ACTIVE_ONLY` → `[{CHAT_ID, CONNECTOR_ID, CONNECTOR_TITLE}]`, сначала последние |
| `imopenlines.crm.chat.getLastId` | `CRM_ENTITY_TYPE`, `CRM_ENTITY` → номер последнего диалога карточки, который видит владелец ключа (GO24DEV-1008) |
| `imopenlines.crm.chat.user.add` | `CRM_ENTITY_TYPE`, `CRM_ENTITY`, `USER_ID`, `CHAT_ID` (по умолчанию — последний) → номер диалога; сотрудника приглашают в него, как «Пригласить в диалог» (GO24DEV-1008) |
| `imopenlines.crm.chat.user.delete` | то же → `true`; сотрудник выходит из диалога (сам, оператор, руководитель линии, администратор) (GO24DEV-1008) |
| `imopenlines.crm.lead.create` | `CHAT_ID` — лид по диалогу или привязка к клиенту, которого CRM знает по телефону или e-mail (нужен и `crm:write`) |
| `imopenlines.crm.message.add` | `CHAT_ID`, `MESSAGE`, `HIDDEN` (`Y` — скрытое сообщение), `USER_ID` (владелец ключа) → id сообщения; ответивший первым берёт диалог |
| `imopenlines.message.quick.save` | `CHAT_ID`, `MESSAGE_ID` → `true`; сообщение становится своим быстрым ответом владельца ключа, первая строка — заголовок (GO24DEV-1008) |
| `imopenlines.revision.get` | → `{rest, web, mobile}` |

- Коннекторы: `livechat` (виджет), `telegrambot`, `vkgroup`, `viber`, `ok`, `avito`, `whatsappbyedna`, `max` и свои
  каналы приложений (ниже).
- Нет: `imopenlines.bot.*` (чат-боты `imbot`), `imopenlines.network.join` и
  `imopenlines.message.session.start`. Собственный REST линий — `/api/v1/openlines`.

### Каналы приложений

GO24DEV-989; только токеном приложения (`openlines:write`). Установленное приложение подключает к линии свой мессенджер,
как `imconnector.*` в корпоративном портале: сообщения клиентов, которые оно передаёт, становятся диалогами линии (канал «app»: очередь,
автосообщения, лид CRM, статистика); ответы операторов и сообщения линии приходят ему событиями. Клиент — один `chat.id`
и `user.id` мессенджера; следующее его сообщение попадает в тот же диалог (закрытый открывается снова).

| Метод | Что |
|---|---|
| `imconnector.register` | `ID` (код: `a-z0-9_.-`, один на портал), `NAME`, `ICON[DATA_IMAGE]`, `PLACEMENT_HANDLER` → `true`; повторно — меняет название и значок; код другого приложения не даётся (администраторы) |
| `imconnector.unregister` | `ID` → `true`; канал уходит из всех линий, его диалоги остаются (администраторы) |
| `imconnector.list` | → `{код: название}` — каналы go24 и приложений |
| `imconnector.activate` | `CONNECTOR`, `LINE`, `ACTIVE` (`1`/`0`) → `true` (администраторы) |
| `imconnector.status` | `CONNECTOR`, `LINE` → `{LINE, CONNECTOR, ERROR, CONFIGURED, STATUS}` |
| `imconnector.connector.data.set` | `CONNECTOR`, `LINE`, `DATA[id, url, url_im, name]` — аккаунт в линии, виден в её настройках (администраторы) |
| `imconnector.send.messages` | `CONNECTOR`, `LINE`, `MESSAGES[]` = `{user: {id, name, last_name, email, phone}, message: {id, text, files: [{url, name}]}, chat: {id, name, url}}` → `{SUCCESS, DATA: {RESULT: [{SUCCESS, user, chat: {id}, message: [id], session: {ID, CHAT_ID}}]}}`; первый файл сообщения go24 забирает к себе (до 50 МБ), остальные — ссылками; сообщение невключённого канала — `SUCCESS: false` с `ERROR` |
| `imconnector.update.messages` | `MESSAGES[]` = `{message: {id, text}, chat: {id}}` — сообщение клиента меняется |
| `imconnector.delete.messages` | `MESSAGES[]` = `{message: {id}, chat: {id}}` — помечается удалённым |
| `imconnector.send.status.delivery` | принимается (go24 показывает только прочитанное) |
| `imconnector.send.status.reading` | `MESSAGES[]` = `{im: {message_id}, chat: {id}}` — клиент прочитал ответы до этого |
| `imconnector.chat.name.set` | `CONNECTOR`, `LINE`, `CHAT_ID`, `NAME` — название диалога |

- События (`event.bind`): `ONIMCONNECTORMESSAGEADD` — `{CONNECTOR, LINE, MESSAGES: [{im: {chat_id, message_id}, message:
  {user_id, user_name, text, date, files: [{name, type, size, link}], keyboard}, chat: {id}}]}` на каждый ответ оператора
  и сообщение линии (скрытые и служебные не уходят); `ONIMCONNECTORDIALOGFINISH` — `{CONNECTOR, LINE, CHAT_ID,
  SESSION_ID}` один раз после закрытия диалога; `ONIMCONNECTORSTATUSDELETE` — `{connector, line}`, когда администратор
  отключил канал в настройках линии («Каналы приложений»). Повторы — как у остальных событий приложений.
- Нет: `ONIMCONNECTORMESSAGEUPDATE`/`DELETE` (операторы go24 не правят отправленные ответы), страницы настройки канала в
  линии (`PLACEMENT_HANDLER` сохраняется), значка канала в списках.

## SMS-провайдеры приложений

`messageservice.*` (GO24DEV-1027): приложение добавляет своего SMS-провайдера; он — один из «SMS» CRM (`/crm/sms`, вид
«Приложение»): через него отправляют карточки, робот «Отправить SMS клиенту», бизнес-процессы и провайдер по умолчанию.
Добавить и удалить — токеном приложения администратора (входящий вебхук получает `WRONG_AUTH_TYPE`); права go24 —
`crm:read`/`crm:write`.

| Метод | Что |
|---|---|
| `messageservice.sender.add` | `CODE` (латинские буквы, цифры, `_ . -`), `TYPE` (`SMS`), `HANDLER` (на домене приложения), `NAME`, `DESCRIPTION` (текст или `{ru, en}`) → `true`; первый провайдер go24 становится провайдером по умолчанию |
| `messageservice.sender.delete` | `CODE` → `true` |
| `messageservice.sender.list` | коды провайдеров приложения |
| `messageservice.message.status.update` | `CODE`, `MESSAGE_ID`, `STATUS` (`queued`, `sent`, `delivered`, `undelivered`, `failed`) → `true` |

- Каждое сообщение уходит на `HANDLER`, как его шлёт корпоративного портала, — формой массивов PHP: `type` (`SMS`), `code`, `message_id`,
  `message_to` (`+` и цифры), `message_body`, `message_from` (имя отправителя провайдера, если задано), `ts`, `auth[…]` —
  токены сотрудника, который отправляет (`auth[user_id]` — его номер); когда отправляет робот — только
  `application_token` и адрес портала. Ответ не 2xx — сообщение не отправлено, в ленте карточки — ошибка.
- `queued`/`sent` go24 хранит как «отправлено», `failed` — как ошибку. Ошибки: `ERROR_SENDER_ALREADY_INSTALLED`,
  `ERROR_SENDER_NOT_FOUND`, `ERROR_SENDER_VALIDATION_FAILURE`, `ERROR_HANDLER_URL_MATCH`, `ERROR_MESSAGE_NOT_FOUND`,
  `ERROR_MESSAGE_STATUS_INCORRECT`.
- Провайдеры удалённого приложения исчезают. Нет: остальных `messageservice.message.*`, типов кроме SMS.
