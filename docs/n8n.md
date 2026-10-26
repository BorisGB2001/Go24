# go24 и n8n

## n8n на dev-сервере

n8n работает в нашем docker compose (сервис `n8n`, образ `n8nio/n8n:1.123.4`, данные — том `n8n-data`):

- редактор: **http://192.168.1.15:5678** — при первом входе n8n просит создать учётку владельца;
- go24 обращается к n8n внутри сети compose: `http://n8n:5678/webhook/<путь>` (а n8n к go24 — `http://go24:8080/api/v1/…`);
- ключ шифрования учётных данных n8n — `N8N_ENCRYPTION_KEY` в `deploy/.env` (не менять);
- пока без https: `N8N_SECURE_COOKIE=false`; когда go24 выйдет в интернет — поставить n8n за caddy с https.

Примеры для импорта (n8n → Workflows → Import from File) — `docs/n8n-examples/`:

- `new-lead-to-task.json` — вебхук go24 «Новый лид» → задача ответственному через API go24
  (подставьте ключ проекта и ключ API; в go24 создайте вебхук на `http://n8n:5678/webhook/go24-lead` с событием «Новый лид»);
- `process-invoice.json` — для шага процесса «Вызвать n8n» с адресом `http://n8n:5678/webhook/go24-invoice`:
  n8n отвечает `{"invoice": "INV-…"}`, и следующий шаг процесса может написать «Счёт {invoice} готов».

Воркфлоу нужно активировать (переключатель Active) — иначе работают только тестовые адреса `/webhook-test/…`.

Три способа связать go24 с n8n (или Make, Zapier — любым сервисом с HTTP).

## 1. События go24 → n8n (вебхуки)

Настройки → «Вебхуки (n8n)» (администратор): адрес узла **Webhook** из n8n и события:
`deal.created`, `deal.stage`, `lead.created`, `lead.stage`, `task.created`, `task.completed`, `task.deleted` (задача перемещена в корзину),
`openline.started` (новый диалог открытой линии), `openline.message` (сообщение посетителя или оператора, `data.from` —
`visitor` | `operator`), `openline.closed` (диалог закрыт, `data.spam` — как спам), `call.ended` (звонок телефонии
завершён: `data` — звонок, как в `GET /api/v1/telephony/calls/{id}`, без записи). REST открытых линий — `/api/v1/openlines/…`
(ключ `openlines:read` / `openlines:write`): линии, диалоги и сообщения, ответ, передача, закрытие, лид, быстрые ответы, статистика.

Запрос — `POST` JSON:

```json
{"event": "deal.stage", "at": "2026-10-14T18:30:24Z", "data": {"id": "…", "title": "Поставка", "stage_id": "…", "stage_code": "won", …}}
```

Заголовки: `X-Go24-Event` (событие), `X-Go24-Signature: sha256=<HMAC-SHA256 тела с секретом вебхука>` — секрет
показан на странице вебхука. Проверка подписи в n8n (узел Code):

```js
const crypto = require('crypto');
const sig = 'sha256=' + crypto.createHmac('sha256', 'СЕКРЕТ').update(JSON.stringify($json.body)).digest('hex');
// для точной проверки включите в узле Webhook «Raw Body» и считайте HMAC от сырого тела
```

События ставятся в очередь триггерами базы (любой путь изменения: интерфейс, API, робот, процесс), отправляются
воркером в течение ~15 секунд; при ошибке — повтор через 1, 5, 30 мин, 2 и 6 ч. «Проверить» шлёт событие `ping`.

## 2. Бизнес-процесс → n8n → бизнес-процесс

В конструкторе процесса шаг **«Вызвать n8n (webhook)»** с адресом узла Webhook. go24 отправляет:

```json
{"event": "bp.step", "process": "Счёт", "process_id": "…", "link": "https://go24/bp/i/…",
 "values": {"sum": "1500"}, "starter": {"id": "…", "name": "Анна"}, "entity": "deal", "entity_id": "…", "card": "https://go24/crm/deals/…"}
```

и ждёт ответа до 30 секунд. Если n8n отвечает JSON-объектом (узел **Respond to Webhook**), его поля становятся полями
процесса: например ответ `{"invoice": "INV-1500"}` — и следующий шаг может написать «Счёт {invoice} готов».
Ответ не 2xx — процесс останавливается с ошибкой (в истории процесса).

## 3. n8n → go24 (REST API)

Ключ: Настройки → «Ключи API» (права по областям). В n8n — узел **HTTP Request**:
`https://go24/api/v1/…`, заголовок `Authorization: Bearer <ключ>`. Ключ действует от имени создавшего его сотрудника
(его права в CRM и задачах).

Входящий вебхук: при создании ключа go24 показывает ссылку `https://go24/rest/<id сотрудника>/<ключ>/` —
дальше пишется путь метода (`…/crm/deals`, можно с `.json`), и это то же, что `/api/v1/crm/deals` с этим ключом, без
заголовка. Ключ чужого сотрудника, неверный или отозванный — 401. Ссылка содержит ключ: хранить как пароль.

CRM (области `crm:read`, `crm:write`), `{kind}` = `deals`, `leads`, `contacts`, `companies`:

| Метод | Что |
|---|---|
| `GET /api/v1/crm/pipelines` | воронки сделок со стадиями и стадии лидов (id, code) |
| `GET /api/v1/crm/currencies` | валюты CRM: код, название, курс к базовой (`base` — базовая, `auto` — курс ЦБ РФ) |
| `GET /api/v1/crm/plan?pipeline=&period=month\|quarter\|half\|year&start=` | «План продаж» воронки на период (по умолчанию — первая воронка, текущий месяц): `metric` (`sum` — сумма успешных сделок в базовой валюте, `count` — их число), строки `company`, `departments`, `users` — `target`, `fact`, `percent`, `won`, `won_sum`, `in_work`, `in_work_sum` (открытые сделки с датой завершения в периоде) |
| `PUT /api/v1/crm/plan` | задать план (администраторы): `{pipeline, period, start, metric, company, departments: {ID: план}, users: {ID: план}}` — цели заменяются; без целей план удаляется |
| `GET /api/v1/crm/products?section_id=&q=&all=1` | каталог товаров: товары раздела и его подразделов (`section` — путь раздела, `photo_url` — картинка); `all=1` — и неактивные |
| `GET /api/v1/crm/product-sections` | разделы каталога деревом: `parent_id`, `path` («Оборудование / Принтеры»), `sort` |
| `GET /api/v1/crm/products/{id}` | товар, услуга или вариация (`purchase_price` — тем, кто видит закупочные цены) |
| `POST /api/v1/crm/products` | новый товар (администратор): `{"name", "type": "product\|service", "sku", "price", "currency", "purchase_price", "unit", "vat_rate" (null — без НДС), "vat_included", "description", "active", "section_id", "props": {id свойства: значение}, "barcodes": [...]}`; с `"parent_id"` — вариация товара (раздел, единица и НДС — от товара; без названия — по свойствам «для вариаций») |
| `PUT /api/v1/crm/products/{id}` | изменить товар: только переданные поля (`"section_id": ""` — в корень каталога, `"props"` — только названные свойства, `""` очищает) |
| `DELETE /api/v1/crm/products/{id}` | удалить товар с вариациями (в сделках строки остаются) |
| `PUT /api/v1/crm/products/{id}/photo` | картинка товара: тело запроса — само изображение (JPEG, PNG, WebP, GIF, до 5 МБ); `DELETE` — убрать |
| `POST /api/v1/crm/product-sections` | новый раздел `{"name", "parent_id", "sort"}`; `PUT …/{id}` — изменить переданные поля, `DELETE …/{id}` — удалить (товары и подразделы — в родительский) |
| `GET /api/v1/store/warehouses` | склады: `name`, `address`, `responsible_id`, `default`, `active`, `products` |
| `POST /api/v1/store/warehouses` | новый склад (администратор) `{"name", "address", "responsible_id", "default", "active", "sort"}`; `PUT …/{id}` — изменить переданные поля, `DELETE …/{id}` — удалить пустой склад, не названный в документах (склад по умолчанию — только назначив другой) |
| `GET /api/v1/store/reports/sales?from=2026-10-01&to=2026-10-31` | «Продажи» склада (по умолчанию — текущий месяц; `warehouse_id`, `section_id`, `q`): по проведённым реализациям — `[{"product_id", "name", "sku", "shipments", "quantity", "revenue"}]`, выручка в базовой валюте; тем, кто видит закупочные цены, — ещё `cost` (по средней закупочной цене на конец периода), `profit`, `margin` (прибыль к себестоимости, %) |
| `GET /api/v1/crm/{kind}?q=…` | список (для сделок можно `&pipeline=<id>`, для лидов `&repeat=1` — только повторные, `&repeat=0` — только первичные); у лида `is_repeat: true` — «Повторный лид»: клиент с тем же телефоном или e-mail уже был, `contact_id`/`company_id` — он |
| `GET /api/v1/crm/{kind}/{id}` | карточка |
| `POST /api/v1/crm/{kind}` | новая карточка: `{"title", "name", "phone", "email", "source", "amount", "currency", "company_id", "contact_id", "pipeline_id", "stage_id", "responsible_id", "observer_ids", "comment"}` (`observer_ids` — наблюдатели сделки или лида; у лида, контакта и компании вместо `phone`/`email` можно `"phones": [{"value", "type"}]`, `"emails": [...]` — несколько, первый основной; типы телефона `work`, `mobile`, `home`, `other`, e-mail — `work`, `home`, `other`; карточка отдаёт их в `phones`, `emails`) |
| `POST /api/v1/crm/{kind}/{id}/stage` | `{"stage_id": …}` — сделки и лиды (сработают роботы стадии) |
| `PUT /api/v1/crm/{deals\|leads}/{id}/observers` | `{"observer_ids": [...]}` — наблюдатели карточки (весь список); видят её при любых правах и получают уведомления о комментариях и стадиях |
| `PUT /api/v1/crm/deals/{id}/parents` | «Связи» элемента смарт-процесса: `{"parent_ids": [...]}` — все родители сразу, по одному каждого вида, к которому привязан смарт-процесс (сделка или элемент другого смарт-процесса); `[]` — отвязать; у карточки элемента — `parent_ids`, при создании (`POST /api/v1/crm/deals`) — тоже `parent_ids` |
| `GET /api/v1/crm/deals/{id}/children` | дочерние элементы сделки или элемента (карточки, как в списке, с `type_id`) |
| `PUT /api/v1/crm/{deals\|leads}/{id}/utm` | `{"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"}` — UTM-метки карточки (все сразу; `{}` — убрать). Их же можно передать в `POST /api/v1/crm/{kind}` полем `"utm": {…}`; карточка сделки или лида отдаёт их в `utm` (сделка без своих — метки лида, из которого создана) |
| `GET /api/v1/marketing/analytics?from=2026-10-01&to=2026-10-31` | «Сквозная аналитика» (по умолчанию — текущий месяц): `{"currency", "channels": [{"id", "name", "expenses", "leads", "deals", "won", "revenue", "cost_per_lead", "cost_per_sale", "roi", "campaigns": […]}], "total"}`; канал без `id` — карточки без источника; `roi` — процент, нет без расходов |
| `GET /api/v1/marketing/sources` | рекламные источники: `[{"id", "name", "utm_source": [...], "crm_source_id"}]` |
| `GET /api/v1/marketing/expenses?source=&from=&to=` | расходы на рекламу, новые сверху (до 1000) |
| `POST /api/v1/marketing/expenses` | `{"source_id", "day": "2026-10-25", "amount", "currency", "campaign", "comment"}` — расход (например, выгрузка из рекламного кабинета) |
| `DELETE /api/v1/marketing/expenses/{id}` | удалить свой расход (администратор — любой) |
| `GET /api/v1/marketing/campaigns` | email-рассылки, новые сверху: `[{"id", "title", "status" (draft, planned, sending, paused, done, stopped), "mailbox_id", "subject", "send_at", "started_at", "finished_at", "author_id", "segments", "excluded", "recipients", "sent", "errors", "skipped", "opened", "clicked", "unsubscribed"}]` |
| `GET /api/v1/marketing/campaigns/{id}` | рассылка с письмом (`html`) |
| `POST /api/v1/marketing/campaigns` | `{"title", "mailbox_id", "subject", "html", "template_id", "segments": [id], "excluded": [id], "send_at", "start": true}` — новая рассылка из своего ящика; `template_id` (`base-news` и т. п. или шаблон компании) подставляет тему и письмо, если их нет; `start` — сразу запустить (или запланировать на `send_at`); `"ab": {"subject", "html", "share": 10–100, "hours": 1–72, "metric": "opened"\|"clicked"}` — A/B-тест (вариант B; в ответе ещё `winner`) |
| `POST /api/v1/marketing/campaigns/{id}/{start\|pause\|resume\|stop\|winner-a\|winner-b}` | запустить, приостановить, продолжить, остановить свою рассылку (администратор — любую); `winner-a`/`winner-b` — завершить A/B-тест и отправить остальным этот вариант |
| `GET /api/v1/marketing/calls` | «Обзвон»: `[{"id", "title", "status" (draft, started), "segment_id", "script", "deadline", "assign" (owner, staff), "staff", "started_at", "author_id", "cards", "done"}]` |
| `GET /api/v1/marketing/calls/{id}?done=&offset=` | обзвон с карточками (до 1000): `items: [{"entity", "entity_id", "name", "phone", "responsible_id", "activity_id", "done_at"}]`; `done=1` — позвонили, `0` — ещё нет |
| `POST /api/v1/marketing/calls` | `{"title", "segment_id", "script", "deadline", "assign": "owner"\|"staff", "staff": [id], "start": true}` — обзвон сегмента CRM: в каждой карточке с телефоном дело «Звонок» со сценарием, ответственным карточек или выбранным сотрудникам по очереди |
| `GET /api/v1/marketing/segments?count=1` | сегменты `[{"id", "name", "entity", "filter"}]`; `count=1` — и число адресатов |
| `GET /api/v1/marketing/templates?html=1` | шаблоны писем: готовые (`base: true`) и компании; `html=1` — с письмом |
| `GET /api/v1/marketing/stoplist?q=` | стоп-лист (до 1000): `[{"email", "campaign_id" (отписался из рассылки), "created_at"}]` |
| `POST /api/v1/marketing/stoplist` | `{"emails": [...]}` — добавить адреса; ответ `{"added"}` |
| `DELETE /api/v1/marketing/stoplist/{email}` | убрать адрес из стоп-листа |
| `POST /api/v1/crm/{kind}/{id}/comments` | `{"body": …}` — строка в истории карточки |
| `GET /api/v1/crm/{deals\|leads}/{id}/products` | товары карточки: `[{"product_id", "name", "price", "quantity", "unit", "discount", "vat_rate", "vat_included", "total"}]` |
| `PUT /api/v1/crm/{deals\|leads}/{id}/products` | заменить товары карточки (тот же список; строка товара каталога без `name` берёт его название и цену) — ответ `{"amount"}`, сумма карточки считается по товарам |
| `GET /api/v1/crm/{companies\|contacts}/{id}/requisites` | реквизиты карточки, основные — первыми: `[{"id", "preset", "name", "inn", "kpp", "ogrn", "director", "address", "bank", "bik", "account", "corr_account", "is_default"}]` (`preset`: `org` — организация, `ip` — ИП, `person` — физ. лицо) |
| `POST /api/v1/crm/{companies\|contacts}/{id}/requisites` | новые реквизиты (тот же объект; без `id`) или изменение реквизитов с `id`; `"is_default": true` — сделать основными — ответ `{"id"}` |
| `DELETE /api/v1/crm/{companies\|contacts}/{id}/requisites/{req}` | удалить реквизиты |

Телефония (области `telephony:read`, `telephony:write`; как `voximplant.*` и `telephony.*` корпоративного портала). Детализация,
статистика и записи — по «Правам доступа» телефонии владельца ключа (свои звонки / своего отдела / все; администратор
с `X-Go24-Admin: 1` — все), чёрный список — тем, кто может менять настройки телефонии. Событие приложений
`ONVOXIMPLANTCALLEND` (`CALL_ID`, `CALL_TYPE` 1 — исходящий / 2 — входящий, `PHONE_NUMBER`, `PORTAL_NUMBER`,
`PORTAL_USER_ID`, `CALL_DURATION`, `CALL_START_DATE`, `CALL_FAILED_CODE` 200 / 304 / 486 / 603, `CALL_STATUS`,
`CRM_ENTITY_TYPE`, `CRM_ENTITY_ID`) и вебхук `call.ended` — по окончании каждого звонка:

| Метод | Что |
|---|---|
| `GET /api/v1/telephony/calls` | детализация (`voximplant.statistic.get`), новые первыми: `{"calls": [{"id", "type", "direction", "status", "from", "to", "from_user", "to_user", "phone_number", "line", "started_at", "answered_at", "seconds", "rating", "comment", "transfer_from", "transfer_to", "crm": {"entity", "id", "name"}, "has_record", "record", "transcript"}], "next_before"}`; `type` — для владельца ключа: `out`, `in`, `missed`, `inner` (чужой); `direction` — `in` (снаружи), `out` (наружу), `inner` (между сотрудниками); `status` — `answered`, `busy`, `noanswer`, `cancel`, `failed`, `voicemail`; `record` и `transcript` — тем, кому можно слушать записи. Фильтры: `type` (`in`, `out`, `missed`), `user`, `q` (часть номера), `from`, `to` (даты), `limit` (до 200), `before` — следующая страница (`next_before`) |
| `GET /api/v1/telephony/calls/{id}` | один звонок |
| `PATCH /api/v1/telephony/calls/{id}` | `{"comment": "…"}` — «Комментарий к звонку» (и в «Звонке» карточки CRM; `""` — убрать) |
| `GET /api/v1/telephony/calls/{id}/record` | запись разговора (WAV) |
| `POST /api/v1/telephony/calls/{id}/transcript` | расшифровать запись (Whisper, один раз — дальше хранится): звонок с `transcript` |
| `GET /api/v1/telephony/stats?from=&to=&line=&user=` | «Статистика звонков» внешних линий: `{"total", "users": [{"user", "in", "answered", "missed", "missed_pct", "out", "out_answered", "seconds", "avg_talk", "avg_wait", "rated", "avg_rating"}], "days": [{"day", "in", "answered", "missed", "out"}]}` |
| `GET /api/v1/telephony/lines` | внешние линии (`voximplant.line.get`, без логинов и паролей): `{"lines": [{"id", "name", "number", "can_call"}], "outgoing"}`; `outgoing` — право владельца ключа на исходящие наружу: `none`, `crm`, `all` |
| `GET /api/v1/telephony/users` | внутренние номера сотрудников (`voximplant.user.get`): `{"users": [{"id", "name", "number", "online"}]}`; `online` — включён SIP-телефон или софтфон (нет поля — АТС не ответила) |
| `POST /api/v1/telephony/call` | `{"number": "+7…"}` — позвонить: у владельца ключа звонят SIP-телефон и софтфон с номером собеседника, он отвечает — АТС набирает номер, как будто его набрал сам сотрудник (внешний — через его линию и по его праву на исходящие; запись — если включена); в детализации — его исходящий. Ответ `{"calling": true, "from", "to"}` |
| `GET /api/v1/telephony/blacklist` | чёрный список: `{"blacklist": [{"id", "number", "comment", "added_by", "created_at"}]}` |
| `POST /api/v1/telephony/blacklist` | `{"number", "comment"}` — добавить (ответ — список); `DELETE …/blacklist/{id}` — убрать |

Онлайн-запись (области `booking:read`, `booking:write`; как `booking.v1.*` корпоративного портала). Типы и ресурсы меняют
администраторы и выбранные в «Онлайн-запись → Права доступа» управляющие, брони и лист ожидания — те, кто работает с
онлайн-записью (по умолчанию все сотрудники); остальным REST раздела отвечает 403. Время — RFC 3339 (`2026-10-27T11:00:00+01:00`), периоды —
даты `from`/`to` включительно (по умолчанию текущий месяц, не больше года):

| Метод | Что |
|---|---|
| `GET /api/v1/booking/types` | типы ресурсов (`resourceType.list`): `[{"id", "code", "name", "resources"}]`; `code` — у стандартных (`employee`, `room`, `equipment`) |
| `POST /api/v1/booking/types`, `PUT …/types/{id}`, `DELETE …/types/{id}` | `{"name"}` — свой тип; удалить можно тип без ресурсов |
| `GET /api/v1/booking/resources?type_id=` | ресурсы (`resource.list`): `[{"id", "type_id", "type_code", "type_name", "name", "description", "user_id", "user_name", "slot_minutes", "days": [1..7], "from": "09:00", "to": "18:00", "notify_mail", "remind_hours", "late_minutes", "overbooking", "feedback", "services": [{"product_id", "name", "price", "currency", "minutes"}], "day_hours": {"6": ["10:00", "15:00"]}, "break_from": "13:00", "break_to": "14:00", "off_dates": ["2026-12-31"], "confirm_hours", "confirm_alert_hours", "calendar_busy"}]`; `calendar_busy` — ресурс-сотрудник учитывает свой календарь: встречи (кроме «свободен») и отсутствия закрывают слоты для записи по ссылке и в `/slots` (по умолчанию `true`; брони сотрудника сами видны в его календаре и делают его занятым в `/api/v1/calendar/busy`, `kind: "booking"`); `confirm_hours` — за сколько часов просить клиента подтвердить запись (письмо со ссылкой на её страницу; 0 — не просить), `confirm_alert_hours` — за сколько часов до записи сообщить сотруднику, что клиент не подтвердил (меньше `confirm_hours`; 0 — не сообщать); `day_hours` — свои часы дней недели, `break_*` — перерыв ("" — нет), `off_dates` — нерабочие даты; `services` — услуги каталога, которые оказывает ресурс; `feedback` — просить клиента оценить визит; `GET …/resources/{id}` — один |
| `POST /api/v1/booking/resources`, `PUT …/resources/{id}` | ресурс (те же поля; `services` — `[{"product_id", "minutes"}]`, только товары с типом «Услуга», `minutes` 0 — слот; в `PUT` не переданные остаются; новый — Пн–Пт 9:00–18:00 по 30 минут); `DELETE …/resources/{id}` — вместе с бронями |
| `GET /api/v1/booking/resources/{id}/slots?date=2026-10-27&service_id=` | свободные слоты на день (`resource.slots.list`): `{"slot_minutes", "minutes", "free": [начала слотов]}`; с `service_id` — начала, с которых свободны `minutes` услуги |
| `GET /api/v1/booking/bookings?from=&to=&resource_id=&status=&q=&mine=1&by_link=1` | брони, задевающие период (`booking.list`): `[{"id", "resource_id", "resource_name", "extra_resource_ids", "start", "end", "client_name", "client_phone", "client_email", "note", "status", "contact_id", "company_id", "deal_id", "created_by", "link_id", "client_url", "services": [{"product_id", "name", "price", "currency", "minutes"}], "amount", "currency", "rating", "rating_note"}]`; услуги — с ценой на момент записи, `amount` — их сумма; `rating` (1–5) и `rating_note` — оценка визита клиентом, если есть; `resource_id` — и где ресурс дополнительный; `status` — ещё `late` (клиент опаздывает) и `attention` (не подтверждена и впереди или опаздывает — что считает счётчик в меню); `q` — часть имени, телефона (по цифрам) или e-mail клиента; `mine=1` — брони владельца ключа (записал он, его ресурс, его ссылка); `by_link=1` — записи клиентов по ссылкам; не больше 1000; `GET …/bookings/{id}` — одна |
| `POST /api/v1/booking/bookings`, `PUT …/bookings/{id}` | бронь (`booking.add`/`update`, `client.set`): `{"resource_id", "extra_resource_ids", "service_ids", "start", "end" или "minutes" (по умолчанию — длительность услуг вместе, без них — слот ресурса), "client_name", "client_phone", "client_email", "note", "status", "contact_id", "company_id", "deal_id"}`; в `PUT` не переданные остаются, перенос без `end` сохраняет длительность. Время занято — `409`; сделка подтягивает контакт и компанию, контакт — имя и телефон клиента; в истории карточек CRM — «Запись клиента» |
| `POST /api/v1/booking/bookings/{id}/status` | `{"status"}`: `unconfirmed`, `confirmed`, `came`, `no_show`, `cancelled` |
| `POST /api/v1/booking/bookings/{id}/wait` | клиента — в лист ожидания, бронь удаляется (`waitlist.createfrombooking`); `DELETE …/bookings/{id}` — удалить бронь |
| `GET /api/v1/booking/waitlist`, `GET …/waitlist/{id}` | лист ожидания (`waitlist.list`): `[{"id", "resource_id", "resource_name", "client_name", "client_phone", "client_email", "note", "contact_id", "company_id", "deal_id", "created_by", "created_at"}]` |
| `POST /api/v1/booking/waitlist`, `PUT …/waitlist/{id}`, `DELETE …/waitlist/{id}` | клиент в листе (те же поля; `resource_id` — желаемый ресурс, без него — любой) |
| `POST /api/v1/booking/waitlist/{id}/book` | `{"resource_id", "start"}` — записать клиента из листа на слот ресурса; ответ — бронь |
| `GET /api/v1/booking/stats?from=&to=&type_id=` | загрузка ресурсов (как «Онлайн-запись → Статистика»): `{"resources": [{"resource_id", "name", "work_minutes", "busy_minutes", "load", "bookings", "statuses": {…}, "no_show_share"}], "total": {…}}`; `load` и `no_show_share` — проценты, `no_show_share` — `null`, пока никто не пришёл и не «не пришёл» |

Компания (области `company:read`, `company:write`; как `user.*`, `department.*`, `timeman.*` корпоративного портала). Отделы меняет только
администратор; рабочий день — только свой (владельца ключа); дни другого сотрудника видят его руководители и
администраторы; отсутствия — с правами страницы «График отсутствий» (своё, руководитель — подчинённых, кадровик,
администратор). Даты `from`/`to` — `YYYY-MM-DD` включительно (по умолчанию текущий месяц, не больше года); `me` вместо
ID — владелец ключа:

| Метод | Что |
|---|---|
| `GET /api/v1/users?q=&department_id=&team_id=&fired=1&preset=here\|absent\|new` | сотрудники (`user.search`): `[{"id", "name", "last_name", "first_name", "second_name", "email", "position", "work_phone", "mobile_phone", "inner_phone", "birthday", "gender", "city", "time_zone", "employment_date", "telegram", "whatsapp", "website", "username", "about", "active", "last_login", "photo_url", "departments": [{"id", "name"}]}]`; `department_id` — с подотделами; `birthday` — `MM-DD`, если сотрудник скрыл год; `guests=1` — гости коллабов (администратору) |
| `GET /api/v1/users/{id}` | карточка сотрудника (`user.get`, `me` — `user.current`) |
| `GET /api/v1/users/{id}/schedule?from=&to=` | график сотрудника (`timeman.schedule.get`): `{"schedule": {…} \| null, "days": {"2026-10-26": {"work", "free", "start", "end", "break", "shift"}}}`; `null` — рабочее время компании |
| `POST /api/v1/users/invite` | пригласить сотрудников (`user.add`; администратор или сотрудник кадров с правом «Приглашать»): `{"people": [{"email", "name"}], "department_id"}` → `201 {"invites": [{"id", "email", "name", "department_id", "department", "state", "link", "mailed", "expires", "created", "invited_by": {"id", "name"}}]}`; ссылка регистрации Authentik на 7 дней уходит письмом из почтового ящика владельца ключа; при первом входе сотрудник попадает в отдел, в «Кадровых изменениях» — «принят» |
| `GET /api/v1/users/invites` | последние приглашения; `state` — `waiting`, `joined`, `expired`, `revoked` (`link` — только у ждущих) |
| `POST /api/v1/users/invites/{id}/resend`, `DELETE /api/v1/users/invites/{id}` | «Пригласить повторно» (новая ссылка ещё на неделю, старая перестаёт работать) и «Отменить» |
| `GET /api/v1/departments` | структура (`department.get`) деревом по порядку: `[{"id", "parent_id", "name", "head_id", "head_name", "deputies": [{"id", "name", "position"}], "employees": […], "depth"}]`; без `parent_id` — сама компания |
| `POST /api/v1/departments` | `{"name", "parent_id", "head_id"}` — новый отдел (`department.add`; без `parent_id` — под компанией) — ответ `{"id"}` |
| `PATCH /api/v1/departments/{id}` | `{"name", "parent_id", "head_id", "deputies": [ID]}` — изменить переданное (`department.update`), новый `parent_id` переносит отдел; `DELETE` — удалить пустой отдел |
| `GET /api/v1/teams?department_id=` | команды структуры: `[{"id", "department_id", "department", "name", "description", "head_id", "head_name", "deputies": [{"id", "name", "position"}], "members": […]}]`; `GET /api/v1/teams/{id}` — одна; у сотрудника в `/users` — `"teams": [{"id", "name"}]` |
| `POST /api/v1/teams` | `{"department_id", "name", "description", "head_id", "deputies": [ID], "members": [ID]}` — новая команда (администратор; без `department_id` — под компанией) — ответ `{"id"}` |
| `PATCH /api/v1/teams/{id}` | изменить переданное: администратор — всё, руководитель команды — кроме `department_id` и `head_id`; `DELETE` — удалить команду (администратор), сотрудники остаются в отделах |
| `GET /api/v1/worktime/status` | рабочий день сегодня (`timeman.status`): `{"state": "none\|running\|paused\|done", "day", "plan": {"work", "start", "end", …}, "workday": {"started", "ended", "paused_at", "pause_secs", "worked_secs", "report", "violations", "start": {"ip", "office"}, "end": {"ip", "office"}}}` — `office`: название офисной сети, `""` — вне офиса, нет — сети не заданы; при «только из офисной сети» начало вне офиса — 422 |
| `POST /api/v1/worktime/{start\|pause\|resume\|finish\|reopen}` | начать, перерыв, продолжить, завершить (`{"report"}`), «Продолжить рабочий день» (`timeman.open/pause/close`) — ответ как `status`; не в том состоянии — `422` |
| `GET /api/v1/worktime/days?user_id=&from=&to=` | рабочие дни (`timeman.timecontrol.reports.get`): `[{"user_id", "day", "state", "started", "ended", "pause_secs", "worked_secs", "report", "violations": ["late\|open\|early\|short"]}]` — нарушения по графику сотрудника |
| `GET /api/v1/worktime/schedules` | графики работы: `[{"id", "name", "kind": "fixed\|shift\|flexible", "departments", "people"}]`; `GET …/schedules/{id}` — график: `times` (дни недели 0–6 с часами и перерывом), `shifts`, `late_after`, `leave_before`, `min_duration`, `tolerance`, `members` (ID и `dept:ID`), `excluded`, `people` |
| `GET /api/v1/absences?from=&to=&user_id=` | отсутствия: `[{"id", "user_id", "user_name", "kind": "vacation\|sick\|trip\|dayoff\|other", "from", "to", "comment", "acting_id", "acting_name", "acting_rights"}]` |
| `POST /api/v1/absences` | `{"user_id", "kind", "from", "to", "comment", "acting_id", "acting_rights": ["view", "work", "control", "manage", "notify"]}` — ответ `{"id"}`; `DELETE /api/v1/absences/{id}` — удалить |
| `GET /api/v1/honors?department_id=&user_id=&archive=1` | «Доска почёта»: кто на доске сейчас (и с будущей даты), `archive=1` — чей срок прошёл: `[{"id", "user_id", "name", "position", "departments", "reason", "from", "to", "by": {"id", "name"}, "created"}]`; нет `to` — бессрочно |
| `POST /api/v1/honors` | `{"user_id", "reason", "from", "to"}` — поместить на доску (администратор или сотрудник кадров с правом «Доска почёта»; `from` — сегодня, без `to` — пока не снимут), сотруднику — уведомление; `DELETE /api/v1/honors/{id}` — снять с доски |

Коллабы (как `socialnetwork.collab.*` в корпоративном портале; области `collabs:read`, `collabs:write`). Ключ видит только коллабы
своего сотрудника (чужой — `404`), права — как на странице коллаба; коллаб в архиве не меняется (`422`), кроме
восстановления:

| Метод | Что |
|---|---|
| `GET /api/v1/collabs?q=&archived=1` | свои коллабы (`q` — по названию и описанию, `archived=1` — архив): `[{"key", "name", "description", "my_role", "members", "guests", "archived", "photo_url", "chat", "counters": {"overdue", "comments"}}]` |
| `POST /api/v1/collabs` | `{"name", "description", "members": [id сотрудников и гостей]}` — новый коллаб, владелец — сотрудник ключа |
| `GET /api/v1/collabs/{key}` | `{"collab": {… "settings"}, "members": [{"id", "name", "role": "owner"\|"manager"\|"member", "guest"}]}` |
| `PATCH /api/v1/collabs/{key}` | `{"name", "description"}` — что не передано, остаётся (кто может изменять настройки) |
| `PUT /api/v1/collabs/{key}/photo` | аватар: тело запроса — само изображение (JPEG, PNG, WebP, GIF, до 5 МБ); `DELETE` — убрать |
| `PATCH /api/v1/collabs/{key}/settings` | `{"invite_perm", "guest_invite_perm": "owner"\|"managers"\|"members", "chat_history", "guests_tasks", "guests_files", "edit_perm": "owner"\|"managers", "chat_perm": "members"\|"employees"\|"managers", "msg_ttl": 0\|3600\|86400\|604800\|2592000 (автоудаление сообщений чата, секунды; 0 — выключено), "events_members", "events_guests", "events_invites", "events_collab" (уведомления в чате коллаба: участники, вступление гостей, приглашения, сам коллаб)}` |
| `POST /api/v1/collabs/{key}/members` | `{"user_id", "role"}` — добавить участника или сменить роль; `DELETE …/members/{id}` — исключить (свой id — покинуть коллаб) |
| `GET /api/v1/collabs/{key}/invites` | приглашения гостей за месяц, действующие — первыми: `[{"id", "kind": "link"\|"email"\|"phone", "url", "email", "phone", "name", "created_by", "created_at", "expires_at", "state": "active"\|"revoked"\|"used"\|"expired", "uses", "used_by"}]` — кто может приглашать гостей |
| `POST /api/v1/collabs/{key}/invites` | `{"kind", "contact" (e-mail или телефон), "name", "days" (1–90, по умолчанию 7), "send": true — письмо с первого ящика сотрудника в go24}` — приглашение; ответ — оно же с `url` и `sent`. Ссылка (`link`) действует для всех до срока, по e-mail и телефону — один раз |
| `DELETE /api/v1/collabs/{key}/invites/{id}` | отозвать приглашение (вступившие по нему остаются) |
| `POST /api/v1/collabs/{key}/invites/{id}/resend` | «Пригласить повторно»: личное приглашение, по которому ещё никто не пришёл и которое не отозвано, действует `days` (по умолчанию 7) с этой минуты; по e-mail письмо уходит ещё раз (`{"send": false}` — не отправлять) |
| `POST /api/v1/collabs/{key}/archive` | `{"archived": true\|false}` — в архив и обратно |
| `POST /api/v1/collabs/{key}/owner` | `{"user_id"}` — передать права владельца сотруднику из участников |
| `DELETE /api/v1/collabs/{key}?confirm=<название>` | удалить коллаб (владелец) |
| `GET /api/v1/collabs/{key}/log` | журнал действий (владелец и модераторы), новые сверху |
| `GET /api/v1/collabs/{key}/chats` | чаты коллаба: главный (`"main": true`) и групповые, в которых состоит владелец ключа: `[{"room", "name", "main"}]` |
| `POST /api/v1/collabs/{key}/chats` | `{"name", "user_ids"}` — создать групповой чат в коллабе (любой участник); кого нет в коллабе — станут участниками (по праву «кто может приглашать»); ответ — `{"room", "name"}` |
| `PUT /api/v1/collabs/{key}/chats/{room}` | добавить в коллаб свой групповой чат (его владелец, если может приглашать в коллаб); участники чата становятся участниками коллаба, история сохраняется; `DELETE` — убрать чат из коллаба (владелец чата, владелец и модераторы коллаба), гости коллаба выходят из чата |
| `POST /api/v1/users/{id}/employee` | сделать гостя сотрудником (ключ администратора) |

Бизнес-процессы (как `bizproc.workflow.*`, `bizproc.workflow.template.*`, `bizproc.task.*` в корпоративном портале; области `bp:read` —
чтение, `bp:write` — запуск, решения, остановка). Шаблоны создают и меняют администраторы портала (они же видят
выключенные шаблоны и шаги), запускает активный шаблон любой; процесс видят запустивший, у кого в нём задание, кто
может читать его карточку CRM, и администраторы. Шаги нумеруются с 1, как `goto` шага; id шаблона виден и в адресе
конструктора (`/bp/templates/<id>`):

| Метод | Что |
|---|---|
| `GET /api/v1/bp/templates?entity=` | шаблоны (`deal`, `lead`, `contact`, `company`, `file` — документ Общего диска, `entity_id` — id файла, `none` — без карточки): `[{"id", "name", "description", "entity", "type_id", "active", "auto_create", "auto_change", "fields", "steps", "url"}]`; `steps` — администраторам |
| `GET /api/v1/bp/templates/{id}` | шаблон |
| `POST /api/v1/bp/templates` | создать (администраторы): `{"name", "description", "entity", "type_id", "active", "auto_create", "auto_change", "fields": [{"key", "label", "kind": "string"\|"text"\|"number"\|"date", "required"}], "steps": [{"kind", "text", "to", …}]}` — шаги в том виде, в каком их отдаёт `GET` |
| `PATCH /api/v1/bp/templates/{id}` | изменить: что не передано — остаётся |
| `DELETE /api/v1/bp/templates/{id}` | удалить шаблон вместе с его процессами, как в конструкторе |
| `POST /api/v1/bp/templates/{id}/start` | запустить (`bizproc.workflow.start`): `{"entity_id": … (шаблон карточки CRM), "values": {…}}`; ответ — процесс; прежний адрес `POST /api/v1/bp/{id}/start` тоже работает |
| `GET /api/v1/bp/instances` | свои процессы, новые сверху; `?status=`; `?entity=deal&entity_id=…` — процессы карточки; `?all=1&status=&template_id=&limit=&offset=` — всех (администраторы) |
| `GET /api/v1/bp/instances/{id}` | процесс: `{"id", "template_id", "name", "entity", "entity_id", "status": "running"\|"done"\|"rejected"\|"failed"\|"stopped", "step", "values", "started_by", "can_stop", "fields", "steps": [{"n", "kind", "text", "state": "done"\|"now"\|""}], "tasks", "log": [{"step", "actor", "kind", "text", "at"}], "url"}` |
| `POST /api/v1/bp/instances/{id}/stop` | «Остановить» (`bizproc.workflow.terminate`): `{"reason"}` — запустивший, кто может менять карточку, администраторы |
| `DELETE /api/v1/bp/instances/{id}` | удалить процесс с историей (`bizproc.workflow.kill`, администраторы) |
| `GET /api/v1/bp/tasks` | задания, которые ждут решения владельца ключа (`bizproc.task.list`): `[{"id", "process_id", "process", "kind": "approve"\|"review"\|"request", "text", "step", "fields", "can_reject", "url"}]` |
| `POST /api/v1/bp/tasks/{id}/complete` | решение (`bizproc.task.complete`): `{"decision": "approve"\|"reject"\|"seen"\|"answer", "comment", "values": {…} (ответ на запрос)}`; ответ — процесс |

Задачи, проекты, комментарии, уведомления, мессенджер — см. `internal/web/api.go`.
