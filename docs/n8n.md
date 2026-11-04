Русская версия: [ru/n8n.md](ru/n8n.md)

# go24 and n8n

## n8n on the dev server

n8n runs in our docker compose (`n8n` service, image `n8nio/n8n:1.123.4`, data volume `n8n-data`):

- editor: **http://192.168.1.15:5678** — on first login n8n asks to create an owner account;
- go24 accesses n8n within the compose network: `http://n8n:5678/webhook/<path>` (and n8n to go24 — `http://go24:8080/api/v1/…`);
- n8n credential encryption key — `N8N_ENCRYPTION_KEY` in `deploy/.env` (do not change);
- currently without https: `N8N_SECURE_COOKIE=false`; when go24 is exposed to the internet — put n8n behind caddy with https.

Examples for import (n8n → Workflows → Import from File) — `docs/n8n-examples/`:

- `new-lead-to-task.json` — go24 «New lead» webhook → task to the assignee via the go24 API
  (substitute the project key and API key; in go24 create a webhook to `http://n8n:5678/webhook/go24-lead` with the «New lead» event);
- `process-invoice.json` — for the «Call n8n» process step with URL `http://n8n:5678/webhook/go24-invoice`:
  n8n responds with `{"invoice": "INV-…"}`, and the next process step can write «Счёт {invoice} готов».

Workflows must be activated (the Active toggle) — otherwise only test URLs `/webhook-test/…` work.

Three ways to connect go24 with n8n (or Make, Zapier — any service with HTTP).

## 1. go24 Events → n8n (webhooks)

Settings → «Webhooks (n8n)» (administrator): URL of the **Webhook** node from n8n and events:
`deal.created`, `deal.stage`, `lead.created`, `lead.stage`, `task.created`, `task.completed`, `task.deleted` (task moved to the trash),
`openline.started` (new open channel dialogue), `openline.message` (visitor or operator message, `data.from` —
`visitor` | `operator`), `openline.closed` (dialogue closed, `data.spam` — as spam), `call.ended` (telephony call
ended: `data` — call as in `GET /api/v1/telephony/calls/{id}`, without recording). Open lines REST — `/api/v1/openlines/…`
(`openlines:read` / `openlines:write` key): lines, dialogues and messages, reply, transfer, close, lead, quick replies, statistics;
CRM form to dialogue — `POST …/dialogs/{id}/form` `{"form_id"}`, booking link — `POST …/dialogs/{id}/booking`
`{"booking_link_id"}` (`kind` `booking` / `booking_done` messages, `booking.done_at` — when the client booked).

Request — `POST` JSON:

```json
{"event": "deal.stage", "at": "2026-10-14T18:30:24Z", "data": {"id": "…", "title": "Поставка", "stage_id": "…", "stage_code": "won", …}}
```

Headers: `X-Go24-Event` (event), `X-Go24-Signature: sha256=<HMAC-SHA256 of the body with webhook secret>` — the secret
is shown on the webhook page. Signature verification in n8n (Code node):

```js
const crypto = require('crypto');
const sig = 'sha256=' + crypto.createHmac('sha256', 'СЕКРЕТ').update(JSON.stringify($json.body)).digest('hex');
// for exact verification enable «Raw Body» in the Webhook node and calculate HMAC from the raw body
```

Events are queued by database triggers (any change path: interface, API, robot, process), dispatched
by the worker within ~15 seconds; on error — retry after 1, 5, 30 min, 2 and 6 h. «Check» sends the `ping` event.

## 2. Business process → n8n → business process

In the process designer, the **«Call n8n (webhook)»** step with the Webhook node address. go24 sends:

```json
{"event": "bp.step", "process": "Счёт", "process_id": "…", "link": "https://go24/bp/i/…",
 "values": {"sum": "1500"}, "starter": {"id": "…", "name": "Анна"}, "entity": "deal", "entity_id": "…", "card": "https://go24/crm/deals/…"}
```

and waits for a response up to 30 seconds. If n8n responds with a JSON object (the **Respond to Webhook** node), its fields become process
fields: for example, the response `{"invoice": "INV-1500"}` — and the next step can write «Счёт {invoice} готов».
A non-2xx response — the process stops with an error (in the process history).

## 3. n8n → go24 (REST API)

Key: Settings → «API keys» (scoped permissions). In n8n — the **HTTP Request** node:
`https://go24/api/v1/…`, header `Authorization: Bearer <key>`. The key operates on behalf of the employee who created it
(their permissions in CRM and tasks).

Incoming webhook, as in Portal: upon key creation go24 shows the URL `https://go24/rest/<employee id>/<key>/` —
followed by the method path (`…/crm/deals`, optionally with `.json`), which is identical to `/api/v1/crm/deals` with this key, without
the header. A different employee's key, an invalid, or a revoked key — 401. The URL contains the key: keep it like a password.
Portal methods by name (`…/user.current.json`, `…/batch`, response `{result, next, total, time}`) — [docs/rest-portal.md](rest-portal.md); the address can take the employee number, as in Portal.

CRM (scopes `crm:read`, `crm:write`), `{kind}` = `deals`, `leads`, `contacts`, `companies`:

| Method | What |
|---|---|
| `GET /api/v1/crm/pipelines` | deal pipelines with stages and lead stages (id, code) |
| `GET /api/v1/crm/currencies` | CRM currencies: code, name, rate to base (`base` — base, `auto` — Bank of Russia exchange rate) |
| `GET /api/v1/crm/plan?pipeline=&period=month\|quarter\|half\|year&start=` | «Sales plan» of a pipeline for a period (default — first pipeline, current month): `metric` (`sum` — sum of won deals in base currency, `count` — their count), rows `company`, `departments`, `users` — `target`, `fact`, `percent`, `won`, `won_sum`, `in_work`, `in_work_sum` (open deals with close date in the period) |
| `PUT /api/v1/crm/plan` | set the plan (administrators): `{pipeline, period, start, metric, company, departments: {ID: plan}, users: {ID: plan}}` — targets are replaced; without targets the plan is deleted |
| `GET /api/v1/crm/products?section_id=&q=&all=1` | product catalog: products of a section and its subsections (`section` — section path, `photo_url` — image); `all=1` — includes inactive |
| `GET /api/v1/crm/product-sections` | catalog sections as a tree: `parent_id`, `path` («Оборудование / Принтеры»), `sort` |
| `GET /api/v1/crm/products/{id}` | product, service, or variation (`purchase_price` — to those who view purchase prices) |
| `POST /api/v1/crm/products` | new product (administrator): `{"name", "type": "product\|service", "sku", "price", "currency", "purchase_price", "unit", "vat_rate" (null — without VAT), "vat_included", "description", "active", "section_id", "props": {property_id: value}, "barcodes": [...]}`; with `"parent_id"` — product variation (section, unit, and VAT from the parent product; without name — from «For variations» properties) |
| `PUT /api/v1/crm/products/{id}` | update a product: only the passed fields (`"section_id": ""` — to catalog root, `"props"` — only named properties, `""` clears) |
| `DELETE /api/v1/crm/products/{id}` | delete a product with variations (rows in deals remain) |
| `PUT /api/v1/crm/products/{id}/photo` | product image: request body is the image itself (JPEG, PNG, WebP, GIF, up to 5 MB); `DELETE` — remove |
| `POST /api/v1/crm/product-sections` | new section `{"name", "parent_id", "sort"}`; `PUT …/{id}` — update passed fields, `DELETE …/{id}` — delete (products and subsections move to parent) |
| `GET /api/v1/store/warehouses` | warehouses: `name`, `address`, `responsible_id`, `default`, `active`, `products` |
| `POST /api/v1/store/warehouses` | new warehouse (administrator) `{"name", "address", "responsible_id", "default", "active", "sort"}`; `PUT …/{id}` — update passed fields, `DELETE …/{id}` — delete empty warehouse not referenced in documents (default warehouse — only after assigning another) |
| `GET /api/v1/store/reports/sales?from=2026-10-01&to=2026-10-31` | warehouse «Sales» (default — current month; `warehouse_id`, `section_id`, `q`): by posted shipments — `[{"product_id", "name", "sku", "shipments", "quantity", "revenue"}]`, revenue in base currency; for those who view purchase prices, also `cost` (by average purchase price at end of period), `profit`, `margin` (profit over cost, %) |
| `GET /api/v1/crm/{kind}?q=…` | list (for deals `&pipeline=<id>` is available, for leads `&repeat=1` — repeat only, `&repeat=0` — primary only); for leads `is_repeat: true` — «Repeat lead»: client with the same phone or email already existed, `contact_id`/`company_id` is them |
| `GET /api/v1/crm/{kind}/{id}` | card |
| `POST /api/v1/crm/{kind}` | new card: `{"title", "name", "phone", "email", "source", "amount", "currency", "company_id", "contact_id", "pipeline_id", "stage_id", "responsible_id", "observer_ids", "comment"}` (`observer_ids` — observers of the deal or lead; for leads, contacts, and companies instead of `phone`/`email`, `"phones": [{"value", "type"}]`, `"emails": [...]` can be used — multiple, first is primary; phone types `work`, `mobile`, `home`, `other`, email — `work`, `home`, `other`; the card returns them in `phones`, `emails`) |
| `PATCH /api/v1/crm/{kind}/{id}` | update a card, like crm.*.update: only passed fields change (same as in `POST`); `stage_id` moves deal or lead to stage, `phone`/`email` replace primary phone and email; unknown field — error 422 |
| `DELETE /api/v1/crm/{kind}/{id}` | delete a card, like crm.*.delete: moved to CRM trash, can be restored from there (204) |
| `POST /api/v1/crm/{kind}/{id}/stage` | `{"stage_id": …}` — deals and leads (stage robots trigger) |
| `PUT /api/v1/crm/{deals\|leads}/{id}/observers` | `{"observer_ids": [...]}` — card observers (entire list); they see it regardless of permissions and receive notifications about comments and stages |
| `PUT /api/v1/crm/deals/{id}/parents` | «Dependencies» of an SPA item: `{"parent_ids": [...]}` — all parents at once, one of each kind to which the SPA is bound (deal or another SPA item); `[]` to unbind; the item card has `parent_ids`, and creation (`POST /api/v1/crm/deals`) accepts `parent_ids` too |
| `GET /api/v1/crm/deals/{id}/children` | child items of a deal or item (cards as in list, with `type_id`) |
| `PUT /api/v1/crm/{deals\|leads}/{id}/utm` | `{"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"}` — UTM parameters of the card (all at once; `{}` to remove). They can also be passed in `POST /api/v1/crm/{kind}` in the `"utm": {…}` field; deal or lead card returns them in `utm` (a deal without its own uses UTM parameters of the lead it was created from) |
| `GET /api/v1/marketing/analytics?from=2026-10-01&to=2026-10-31` | «End-to-end analytics» (default — current month): `{"currency", "channels": [{"id", "name", "expenses", "leads", "deals", "won", "revenue", "cost_per_lead", "cost_per_sale", "roi", "campaigns": […]}], "total"}`; channel without `id` — cards without source; `roi` — percentage, absent without expenses |
| `GET /api/v1/marketing/sources` | ad sources: `[{"id", "name", "utm_source": [...], "crm_source_id"}]` |
| `GET /api/v1/marketing/expenses?source=&from=&to=` | ad expenses, newest first (up to 1000) |
| `POST /api/v1/marketing/expenses` | `{"source_id", "day": "2026-10-25", "amount", "currency", "campaign", "comment"}` — expense (for example, export from an ad account) |
| `DELETE /api/v1/marketing/expenses/{id}` | delete own expense (administrator — any) |
| `GET /api/v1/marketing/campaigns` | email campaigns, newest first: `[{"id", "title", "status" (draft, planned, sending, paused, done, stopped), "mailbox_id", "subject", "send_at", "started_at", "finished_at", "author_id", "segments", "excluded", "recipients", "sent", "errors", "skipped", "opened", "clicked", "unsubscribed"}]` |
| `GET /api/v1/marketing/campaigns/{id}` | campaign with email message (`html`) |
| `POST /api/v1/marketing/campaigns` | `{"title", "mailbox_id", "subject", "html", "template_id", "segments": [id], "excluded": [id], "send_at", "start": true}` — new campaign from own mailbox; `template_id` (`base-news` etc. or company template) fills subject and email body if omitted; `start` — send immediately (or schedule at `send_at`); `"ab": {"subject", "html", "share": 10–100, "hours": 1–72, "metric": "opened"\|"clicked"}` — A/B test (variant B; response also includes `winner`) |
| `POST /api/v1/marketing/campaigns/{id}/{start\|pause\|resume\|stop\|winner-a\|winner-b}` | start, pause, resume, stop own campaign (administrator — any); `winner-a`/`winner-b` — conclude A/B test and send this variant to remaining recipients |
| `GET /api/v1/marketing/calls` | «Call lists»: `[{"id", "title", "status" (draft, started), "segment_id", "script", "deadline", "assign" (owner, staff), "staff", "started_at", "author_id", "cards", "done"}]` |
| `GET /api/v1/marketing/calls/{id}?done=&offset=` | call list with cards (up to 1000): `items: [{"entity", "entity_id", "name", "phone", "responsible_id", "activity_id", "done_at"}]`; `done=1` — called, `0` — not yet |
| `POST /api/v1/marketing/calls` | `{"title", "segment_id", "script", "deadline", "assign": "owner"\|"staff", "staff": [id], "start": true}` — call list for CRM segment: in each card with phone number, a «Call» activity with script, assigned to card responsibles or selected staff in rotation |
| `GET /api/v1/marketing/segments?count=1` | segments `[{"id", "name", "entity", "filter"}]`; `count=1` — includes recipient count |
| `GET /api/v1/marketing/templates?html=1` | email templates: built-in (`base: true`) and company templates; `html=1` — includes email body |
| `GET /api/v1/marketing/stoplist?q=` | stop-list (up to 1000): `[{"email", "campaign_id" (unsubscribed from campaign), "created_at"}]` |
| `POST /api/v1/marketing/stoplist` | `{"emails": [...]}` — add addresses; response `{"added"}` |
| `DELETE /api/v1/marketing/stoplist/{email}` | remove address from stop-list |
| `POST /api/v1/crm/{kind}/{id}/comments` | `{"body": …}` — entry in card history |
| `GET /api/v1/crm/{kind}/{id}/wait` | the card's «Wait»: `{"id", "until", "description", "created_by", "created_by_name", "created_at"}` or `null` |
| `POST /api/v1/crm/{kind}/{id}/wait` | wait: `{"until": RFC 3339}` or `{"n": 3, "unit": "day\|week", "anchor": "now\|close\|f:<field>"}`, `"description"`; the previous wait ends (201) |
| `DELETE /api/v1/crm/{kind}/{id}/wait` | «Finish» the wait (204) |
| `GET /api/v1/crm/deals/{id}/score` | «Deal scoring»: `{"probability": 0–100, "factors": [{"feature", "value", "text", "up"}]}`; `null` — the deal is closed or its pipeline is not scored |
| `GET /api/v1/crm/scoring` | scoring of the deal pipelines: `[{"pipeline_id", "name", "enabled", "ready", "samples", "won", "accuracy", "trained_at", "closed"}]` |
| `POST /api/v1/crm/scoring/{pipeline}` | administrators: `{"enabled": true\|false}` turns scoring on (it learns at once) or off, `{"train": true}` learns again now; returns the list |
| `POST /api/v1/crm/{kind}/{id}/copy` | «Copy»: a new card with the original's fields, own fields, client, observers, phones and product rows (without reserves); the responsible is the original's if the key may give them the card, else the key's owner; returns the copy (201) |
| `POST /api/v1/crm/{kind}/{id}/chat` | «Chat»: the card's group chat in the messenger — made on the first call (the responsible, observers and the key's owner in it), later the same one, the key's owner joins it: `{"room", "url"}` |
| `GET /api/v1/crm/paysystems` | «Payment systems»: `[{"id", "kind": "yookassa\|tbank", "name", "login", "active", "has_key"}]` (without keys); `?invoice=ID` — the invoice's online payments `[{"id", "system", "external_id", "amount", "currency", "status": "pending\|succeeded\|canceled", "error", "created_at", "paid_at"}]`, the last first |
| `GET /api/v1/crm/contact-center` | «Contact center»: channels `[{"key": "widget\|telegram\|telephony\|callback\|forms\|booking\|mail\|sms\|sites", "url", "count", "errors", "recent", "inbound"}]` — connected (0 — not), with an error, inquiries in 30 days |
| `GET/PUT /api/v1/crm/tile` | «Card view» of the kanban: `?scope=deal\|lead\|type:ID` — the fields the key's owner sees on the tiles in order `{"scope", "fields": ["amount", "client", "f:<field id>"…], "own", "common"}`; PUT `{"scope", "fields", "common"}` saves one's own view (`common` — for everybody, administrators), `"reset": true` — back to the common one (with `common` — the common one back to the standard) |
| `GET /api/v1/crm/{deals\|leads}/{id}/products` | card products: `[{"product_id", "name", "price", "quantity", "unit", "discount", "vat_rate", "vat_included", "total"}]` |
| `PUT /api/v1/crm/{deals\|leads}/{id}/products` | replace card products (same list; catalog product row without `name` takes its name and price) — response `{"amount"}`, card amount is calculated from products |
| `GET /api/v1/crm/{companies\|contacts}/{id}/requisites` | card requisites, primary first: `[{"id", "preset", "name", "inn", "kpp", "ogrn", "director", "address", "bank", "bik", "account", "corr_account", "is_default"}]` (`preset`: `org` — organization, `ip` — sole trader, `person` — individual) |
| `POST /api/v1/crm/{companies\|contacts}/{id}/requisites` | new requisites (same object; without `id`) or update requisites with `id`; `"is_default": true` — set as primary — response `{"id"}` |
| `DELETE /api/v1/crm/{companies\|contacts}/{id}/requisites/{req}` | delete requisites |

Telephony (scopes `telephony:read`, `telephony:write`; like `voximplant.*` and `telephony.*` of Portal). Call log,
statistics, and recordings — according to the key owner's telephony «Access rights» (own calls / own department / all; administrator
with `X-Go24-Admin: 1` — all), blocklist — to those who can change telephony settings. Application event
`ONVOXIMPLANTCALLEND` (`CALL_ID`, `CALL_TYPE` 1 — outbound / 2 — inbound, `PHONE_NUMBER`, `PORTAL_NUMBER`,
`PORTAL_USER_ID`, `CALL_DURATION`, `CALL_START_DATE`, `CALL_FAILED_CODE` 200 / 304 / 486 / 603, `CALL_STATUS`,
`CRM_ENTITY_TYPE`, `CRM_ENTITY_ID`) and webhook `call.ended` — on completion of each call:

| Method | What |
|---|---|
| `GET /api/v1/telephony/calls` | call log (`voximplant.statistic.get`), newest first: `{"calls": [{"id", "type", "direction", "status", "from", "to", "from_user", "to_user", "phone_number", "line", "started_at", "answered_at", "seconds", "rating", "comment", "transfer_from", "transfer_to", "crm": {"entity", "id", "name"}, "has_record", "record", "transcript"}], "next_before"}`; `type` — for the key owner: `out`, `in`, `missed`, `inner` (another user's); `direction` — `in` (inbound from outside), `out` (outbound to outside), `inner` (between employees); `status` — `answered`, `busy`, `noanswer`, `cancel`, `failed`, `voicemail`; `record` and `transcript` — to those allowed to listen to recordings. Filters: `type` (`in`, `out`, `missed`), `user`, `q` (part of number), `from`, `to` (dates), `limit` (up to 200), `before` — next page (`next_before`) |
| `GET /api/v1/telephony/calls/{id}` | single call |
| `PATCH /api/v1/telephony/calls/{id}` | `{"comment": "…"}` — «Call comment» (and in the CRM card's «Call»; `""` to remove) |
| `GET /api/v1/telephony/calls/{id}/record` | call recording (WAV) |
| `POST /api/v1/telephony/calls/{id}/transcript` | transcribe recording (Whisper, once — then stored): call with `transcript` |
| `GET /api/v1/telephony/stats?from=&to=&line=&user=` | «Call statistics» of external lines: `{"total", "users": [{"user", "in", "answered", "missed", "missed_pct", "out", "out_answered", "seconds", "avg_talk", "avg_wait", "rated", "avg_rating"}], "days": [{"day", "in", "answered", "missed", "out"}], "hours": [{"weekday", "hour", "in", "missed"}], "previous": {"from", "to", "total"}, "missed": {"missed", "recalled", "not_recalled", "recalled_pct", "avg_recall", "waiting": [{"number", "times", "last", "user", "crm"}]}}` — `recalled`: a conversation with the number followed the missed call |
| `GET /api/v1/telephony/lines` | external lines (`voximplant.line.get`, without logins and passwords): `{"lines": [{"id", "name", "number", "can_call"}], "outgoing"}`; `outgoing` — the key owner's permission for outbound calls: `none`, `crm`, `all` |
| `GET /api/v1/telephony/users` | internal employee extensions (`voximplant.user.get`): `{"users": [{"id", "name", "number", "online", "skip"}]}`; `online` — SIP phone or softphone connected (field omitted if PBX did not respond); `skip` — why clients' calls pass the employee by now: `dnd`, `absent`, `workday` («Whom not to ring»; omitted — they ring) |
| `POST /api/v1/telephony/call` | `{"number": "+7…"}` — place a call: the key owner's SIP phone and softphone ring showing the counterparty's number; once answered — the PBX dials the number as if dialed by the employee (external — via their line and according to their outbound permissions; recording — if enabled); in the call log — their outbound call. Response `{"calling": true, "from", "to"}` |
| `GET /api/v1/telephony/tts` | «Speak a text»: `{"voices": ["ru_RU-irina-medium", …], "max_text": 1000}`; `POST` `{"text", "voice", "rate"}` (rate 0.5–2, 1 — normal) — the WAV as telephony plays it (8 kHz mono) |
| `GET /api/v1/telephony/blacklist` | blocklist: `{"blacklist": [{"id", "number", "comment", "added_by", "created_at"}]}` |
| `POST /api/v1/telephony/blacklist` | `{"number", "comment"}` — add (response — list); `DELETE …/blacklist/{id}` — remove |
| `GET /api/v1/telephony/active` | «Current calls» — active conversations of employees shown according to the key owner's «Listening to current calls» permission (no permission — 403): `{"items": [{"channel", "user", "number", "peer", "peer_name", "direction", "state", "started_at", "seconds"}]}`; `direction` — `in`, `out`; `state` — `talking`, `ringing` |
| `POST /api/v1/telephony/active/spy` | `{"channel", "mode"}` — connect to a call: go24 calls the key owner's phone; once answered — `listen` listens, `whisper` coaches (heard only by employee), `barge` talks with both; own conversation — not allowed |
| `POST /api/v1/telephony/active/pick` | `{"channel"}` — pick up a call ringing for a colleague (a channel from `GET /api/v1/telephony/active`): go24 calls the key owner's phone; once answered, the call is theirs |

Online booking (scopes `booking:read`, `booking:write`; like `booking.v1.*` of Portal). Types and resources are modified by
administrators and managers selected in «Online booking → Access rights», bookings and waitlist — by those who work with
online booking (all employees by default); for everyone else, this section's REST returns 403. Time — RFC 3339 (`2026-10-27T11:00:00+01:00`), periods —
dates `from`/`to` inclusive (current month by default, no more than one year):

| Method | What |
|---|---|
| `GET /api/v1/booking/types` | resource types (`resourceType.list`): `[{"id", "code", "name", "resources"}]`; `code` — for standard types (`employee`, `room`, `equipment`) |
| `POST /api/v1/booking/types`, `PUT …/types/{id}`, `DELETE …/types/{id}` | `{"name"}` — custom type; can only delete type without resources |
| `GET /api/v1/booking/resources?type_id=` | resources (`resource.list`): `[{"id", "type_id", "type_code", "type_name", "name", "description", "user_id", "user_name", "slot_minutes", "days": [1..7], "from": "09:00", "to": "18:00", "notify_mail", "remind_hours", "late_minutes", "overbooking", "feedback", "services": [{"product_id", "name", "price", "currency", "minutes"}], "day_hours": {"6": ["10:00", "15:00"]}, "break_from": "13:00", "break_to": "14:00", "off_dates": ["2026-12-31"], "confirm_hours", "confirm_alert_hours", "calendar_busy"}]`; `calendar_busy` — employee resource takes own calendar into account: meetings (except «Free») and absences block slots for booking via link and in `/slots` (`true` by default; an employee's bookings are visible in their calendar and mark them busy in `/api/v1/calendar/busy`, `kind: "booking"`); `confirm_hours` — how many hours before booking to ask client for confirmation (email with link to booking page; 0 — do not ask), `confirm_alert_hours` — how many hours before booking to alert employee if client has not confirmed (less than `confirm_hours`; 0 — do not alert); `day_hours` — custom hours for days of the week, `break_*` — break ("" — none), `off_dates` — days off; `services` — catalog services provided by resource; `feedback` — ask client to review the visit; `GET …/resources/{id}` — single resource |
| `POST /api/v1/booking/resources`, `PUT …/resources/{id}` | resource (same fields; `services` — `[{"product_id", "minutes"}]`, only products with type «Service», `minutes` 0 — slot; unpassed fields in `PUT` remain; new — Mon–Fri 9:00–18:00 with 30-minute slots); `DELETE …/resources/{id}` — along with bookings |
| `GET /api/v1/booking/resources/{id}/slots?date=2026-10-27&service_id=` | free slots for a day (`resource.slots.list`): `{"slot_minutes", "minutes", "free": [slot starts]}`; with `service_id` — starts where `minutes` of the service are free |
| `GET /api/v1/booking/bookings?from=&to=&resource_id=&status=&q=&mine=1&by_link=1` | bookings overlapping the period (`booking.list`): `[{"id", "resource_id", "resource_name", "extra_resource_ids", "start", "end", "client_name", "client_phone", "client_email", "note", "status", "contact_id", "company_id", "deal_id", "created_by", "link_id", "client_url", "services": [{"product_id", "name", "price", "currency", "minutes"}], "amount", "currency", "rating", "rating_note"}]`; services — with price at time of booking, `amount` — their total; `rating` (1–5) and `rating_note` — client visit review if provided; `resource_id` — includes bookings where resource is extra; `status` — also `late` (client is late) and `attention` (unconfirmed and upcoming, or late — counted by menu badge); `q` — part of client name, phone (by digits), or email; `mine=1` — key owner's bookings (booked by them, their resource, their link); `by_link=1` — bookings by clients via links; up to 1000; `GET …/bookings/{id}` — single booking |
| `POST /api/v1/booking/bookings`, `PUT …/bookings/{id}` | booking (`booking.add`/`update`, `client.set`): `{"resource_id", "extra_resource_ids", "service_ids", "start", "end" or "minutes" (default — total duration of services, without them — resource slot), "client_name", "client_phone", "client_email", "note", "status", "contact_id", "company_id", "deal_id"}`; unpassed fields in `PUT` remain, rescheduling without `end` preserves duration. Time conflict — `409`; deal pulls in contact and company, contact pulls in client name and phone; in CRM card history — «Booking» |
| `POST /api/v1/booking/bookings/{id}/status` | `{"status"}`: `unconfirmed`, `confirmed`, `came`, `no_show`, `cancelled` |
| `POST /api/v1/booking/bookings/{id}/wait` | move client to waitlist, booking is deleted (`waitlist.createfrombooking`); `DELETE …/bookings/{id}` — delete booking |
| `GET /api/v1/booking/waitlist`, `GET …/waitlist/{id}` | waitlist (`waitlist.list`): `[{"id", "resource_id", "resource_name", "client_name", "client_phone", "client_email", "note", "contact_id", "company_id", "deal_id", "created_by", "created_at"}]` |
| `POST /api/v1/booking/waitlist`, `PUT …/waitlist/{id}`, `DELETE …/waitlist/{id}` | client in waitlist (same fields; `resource_id` — requested resource, omitted — any) |
| `POST /api/v1/booking/waitlist/{id}/book` | `{"resource_id", "start"}` — book client from waitlist to resource slot; response — booking |
| `GET /api/v1/booking/stats?from=&to=&type_id=` | resource load (like «Online booking → Statistics»): `{"resources": [{"resource_id", "name", "work_minutes", "busy_minutes", "load", "bookings", "statuses": {…}, "no_show_share"}], "total": {…}}`; `load` and `no_show_share` — percentages, `no_show_share` — `null` while nobody has arrived or been marked «не пришёл» |

Company (scopes `company:read`, `company:write`; like `user.*`, `department.*`, `timeman.*` of Portal). Departments are changed only by
an administrator; work day — only own (key owner's); another employee's days are visible to their managers and
administrators; absences — according to the permissions of the «Absences» page (own, manager — subordinates, HR specialist,
administrator). Dates `from`/`to` — `YYYY-MM-DD` inclusive (current month by default, no more than one year); `me` instead of
ID — key owner:

| Method | What |
|---|---|
| `GET /api/v1/users?q=&department_id=&team_id=&fired=1&preset=here\|absent\|new` | employees (`user.search`): `[{"id", "name", "last_name", "first_name", "second_name", "email", "position", "work_phone", "mobile_phone", "inner_phone", "birthday", "gender", "city", "time_zone", "employment_date", "telegram", "whatsapp", "website", "username", "about", "active", "last_login", "photo_url", "departments": [{"id", "name"}]}]`; `department_id` — includes sub-departments; `birthday` — `MM-DD` if employee hid birth year; `guests=1` — collab guests (to administrator) |
| `GET /api/v1/users/{id}` | employee card (`user.get`, `me` — `user.current`) |
| `GET /api/v1/users/{id}/schedule?from=&to=` | employee schedule (`timeman.schedule.get`): `{"schedule": {…} \| null, "days": {"2026-10-26": {"work", "free", "start", "end", "break", "shift"}}}`; `null` — company working hours |
| `GET`/`PUT /api/v1/users/{id}/vacation?year=` | «Salary and vacation» — the vacation balance of the year (the employee, their managers, HR officers, administrator; `me` — own): `{"user_id", "year", "days", "own", "carried", "taken", "planned", "left", "left_now"}`; change (administrator, HR officer with «Absences») `{"year", "days": 28 \| null, "carried"}` — `null` is the company default |
| `GET`/`POST /api/v1/payslips?user_id=&month=2026-09`, `GET …/{id}/file`, `DELETE …/{id}` | «Payslips»: an employee sees only their own, an HR officer with «Salary» — everyone's: `[{"id", "user_id", "user_name", "month", "file_name", "size", "uploaded_by", "created_at", "seen_at"}]`; upload — multipart `user_id`, `month`, `file` (PDF up to 10 MB, replaces the month's one) |
| `GET`/`POST /api/v1/gallery/albums?parent=`, `GET`/`PUT`/`DELETE …/albums/{id}`, `GET`/`POST …/albums/{id}/photos`, `GET /api/v1/gallery/photos/{id}/file`, `DELETE …/photos/{id}` | «Photo gallery»: the albums the key owner sees (`parent` — inside an album): `[{"id", "parent_id", "title", "description", "cover_id", "anyone_adds", "created_by", "author_name", "created_at", "updated_at", "photos", "albums"}]`; create or change `{"parent_id", "title", "description", "anyone_adds", "access": ["USER_ID", "dept:ID"]}` (at the top — administrator, HR officer with «Photo gallery»; inside — who changes that album); photos `[{"id", "album_id", "file_name", "mime", "size", "width", "height", "caption", "created_by", "created_at", "likes", "liked", "comments"}]`; upload — multipart `files` (JPEG, PNG, GIF, WEBP up to 20 MB, up to 50), `caption` |
| `POST /api/v1/users/invite` | invite employees (`user.add`; administrator or HR employee with «Приглашать» permission): `{"people": [{"email", "name"}], "department_id"}` → `201 {"invites": [{"id", "email", "name", "department_id", "department", "state", "link", "mailed", "expires", "created", "invited_by": {"id", "name"}}]}`; Authentik registration link valid for 7 days is sent by email from the key owner's mailbox; on first login, employee is assigned to department, with «Hired» in «Staff changes» |
| `GET /api/v1/users/invites` | recent invitations; `state` — `waiting`, `joined`, `expired`, `revoked` (`link` — only for waiting) |
| `POST /api/v1/users/invites/{id}/resend`, `DELETE /api/v1/users/invites/{id}` | «Invite again» (new link for another week, old link stops working) and «Undo» |
| `GET /api/v1/users/join-requests` | «Quick registration» requests: pending (`pending`), then decided within 30 days (`accepted` with `invite_id`, `rejected`) |
| `POST /api/v1/users/join-requests/{id}/accept` `{"department_id"}`, `POST …/{id}/reject` | accept request (invitation, like `POST /users/invite`) or reject |
| `GET`/`PUT /api/v1/settings/employees`, `POST …/employees/link` | «Settings → Employees» (administrator): `invite_all`, `register`, `approve`, `domains`, `link`; `link` — new registration link |
| `GET`/`PUT /api/v1/settings/two-factor` | «Two-factor authentication» (administrator): `{"mode": "" \| "admins" \| "all", "days": N}`; response contains `policy` and `users` (`required`, `mfa_at`, `deadline`) |
| `GET /api/v1/departments` | structure (`department.get`) as an ordered tree: `[{"id", "parent_id", "name", "head_id", "head_name", "deputies": [{"id", "name", "position"}], "employees": […], "depth"}]`; without `parent_id` — company itself |
| `POST /api/v1/departments` | `{"name", "parent_id", "head_id"}` — new department (`department.add`; without `parent_id` — under company) — response `{"id"}` |
| `PATCH /api/v1/departments/{id}` | `{"name", "parent_id", "head_id", "deputies": [ID]}` — update passed fields (`department.update`), new `parent_id` moves department; `DELETE` — delete empty department. Structure can be changed by administrators and those allowed by structure «Access permissions» (`/departments/rights`), otherwise `403` |
| `GET`/`PUT /api/v1/departments/rights` | structure «Access permissions» (changed by administrator): `{"head": {"create": "own", …}, "deputy": {…}, "employee": {…}}` — actions `create`, `edit`, `delete`, `staff` (add, transfer, remove employees), `chats`; `own` — in own departments with sub-departments, `all` — in all, omitted key — not allowed |
| `GET /api/v1/teams?department_id=` | structure teams: `[{"id", "department_id", "department", "name", "description", "head_id", "head_name", "deputies": [{"id", "name", "position"}], "members": […]}]`; `GET /api/v1/teams/{id}` — single team; for employee in `/users` — `"teams": [{"id", "name"}]` |
| `POST /api/v1/teams` | `{"department_id", "name", "description", "head_id", "deputies": [ID], "members": [ID]}` — new team (administrator; without `department_id` — under company) — response `{"id"}` |
| `PATCH /api/v1/teams/{id}` | update passed fields: administrator — all, team lead — except `department_id` and `head_id`; `DELETE` — delete team (administrator), employees remain in departments |
| `GET /api/v1/worktime/status` | working day today (`timeman.status`): `{"state": "none\|running\|paused\|done", "day", "plan": {"work", "start", "end", …}, "workday": {"started", "ended", "paused_at", "pause_secs", "worked_secs", "report", "violations", "start": {"ip", "office", "geo"}, "end": {"ip", "office", "geo"}}}` — `office`: name of office network, `""` — out of office, omitted — networks not configured; `geo`: `{"lat", "lon", "acc"}` (accuracy in metres) with «Location» on; with «only from an office network», start outside office returns 422 |
| `POST /api/v1/worktime/{start\|pause\|resume\|finish\|reopen}` | start, break, resume, finish (`{"report"}`; at start and finish also `"lat", "lon", "acc"` — kept with «Location» on), «Continue the working day» (`timeman.open/pause/close`) — response like `status`; not in appropriate state — `422` |
| `GET /api/v1/worktime/days?user_id=&from=&to=` | working days (`timeman.timecontrol.reports.get`): `[{"user_id", "day", "state", "started", "ended", "pause_secs", "worked_secs", "report", "violations": ["late\|open\|early\|short"]}]` — violations per employee schedule |
| `GET /api/v1/worktime/timecontrol?user_id=&from=&to=` | «Time control» absences (`timeman.timecontrol.reports.get`): your own; an employee's — to their manager; without `user_id` — everyone the key owner can see: `[{"id", "user_id", "name", "day", "start", "end", "secs", "kind": "\|work\|personal", "reason", "answered_at"}]`; `POST …/timecontrol/{id}` `{"kind", "reason"}` — answer about your own absence |
| `GET`/`PUT /api/v1/worktime/timecontrol/settings` | «Time control» settings (`timeman.timecontrol.settings.get/set`): `{"active", "idle_minutes", "ask", "since"}`; may be changed by the administrator and those whom the working time «Access rights» grant settings |
| `GET`/`PUT /api/v1/worktime/rights` | Working time «Access rights» (changed by the administrator): `{"head": {"view": "own", "edit": "own"}, "deputy": {…}, "employee": {"settings": "all"}}` — `view` (see days, the table, reports), `edit` (edit days, approve corrections, rate reports, shifts), `settings` (settings and schedules, `all` only); `own` — in their own departments with sub-departments, `all` — in all, no key — not allowed. The same rights decide whether `/worktime/days` and `/worktime/timecontrol` return other people's days |
| `GET`/`POST /api/v1/worktime/terminals`, `GET`/`PUT`/`DELETE …/{id}` | «Time clock terminals» (those who change the working time settings): `{"id", "name", "active", "link", "used_at"}`; add `{"name"}`, change `{"name", "active", "new_link": true}` — the old link stops working |
| `GET /api/v1/worktime/schedules` | work schedules: `[{"id", "name", "kind": "fixed\|shift\|flexible", "departments", "people"}]`; `GET …/schedules/{id}` — schedule: `times` (days of the week 0–6 with hours and a break), `shifts`, `late_after`, `leave_before`, `min_duration`, `tolerance`, `notify_head` and `notify_to` (whom to notify about violations: the manager, employee IDs), `members` (IDs and `dept:ID`), `excluded`, `people` |
| `GET /api/v1/absences?from=&to=&user_id=` | absences: `[{"id", "user_id", "user_name", "kind": "vacation\|sick\|trip\|dayoff\|absent\|maternity\|other\|cXXXXXXXX", "from", "to", "comment", "acting_id", "acting_name", "acting_rights"}]` |
| `POST /api/v1/absences` | `{"user_id", "kind", "from", "to", "comment", "acting_id", "acting_rights": ["view", "work", "control", "manage", "notify"]}` — response `{"id"}`; `DELETE /api/v1/absences/{id}` — delete |
| `GET /api/v1/absences/kinds?all=1` | «Absence types»: `[{"key", "name", "color": "green\|red\|violet\|orange\|blue\|gray", "off", "builtin", "title"}]` — standard and custom (`c…` key); `name` — custom name (empty — standard), `title` — as displayed; without `all=1` — enabled only |
| `PUT /api/v1/absences/kinds` | `[{"key", "name", "color", "off"}]` — save types (administrator or HR employee with «Absences» right): without `key` — new; custom types not in list are deleted (if they have absences — disabled); response — same as `GET` |
| `GET /api/v1/interests?q=&limit=` | employee «Interests», most frequent first: `[{"tag", "count"}]`; `q` — start of interest or word in it. For employee in `/api/v1/users` — `"interests": ["…"]`, filter `?interest=` |
| `PUT /api/v1/users/{id}/interests` | `{"interests": ["футбол", "фотография"]}` — replace interests (`me` — own; others — administrator); response — employee card |
| `GET /api/v1/honors?department_id=&user_id=&archive=1` | «Honor board»: who is on board now (and from future date), `archive=1` — whose term has expired: `[{"id", "user_id", "name", "position", "departments", "reason", "from", "to", "by": {"id", "name"}, "created"}]`; without `to` — indefinite |
| `POST /api/v1/honors` | `{"user_id", "reason", "from", "to"}` — place on board (administrator or HR employee with «Honor board» right; `from` — today, without `to` — until removed), employee receives notification; `DELETE /api/v1/honors/{id}` — remove from board |
| `GET /api/v1/enps` | eNPS surveys: to administrator and HR employee with «eNPS» right — all with `enps` index, to others — those where they are asked: `[{"id", "question", "from", "to", "status": "coming\|open\|closed", "departments", "created_by", "audience", "answers", "asked", "answered", "enps"}]` |
| `GET /api/v1/enps/{id}` | survey; to survey managers — `results`: `shown` (from `min_answers` responses), `total` and `departments` (`answers`, `promoters`, `passives`, `detractors`, `enps`), `scores` (responses by scores 0–10), `hidden`, `comments` without authors, `prev_id`, `prev_enps` |
| `POST /api/v1/enps` | `{"question", "from", "to", "department_ids"}` — launch survey (without departments — entire company; by default standard question and two weeks from today), participants receive notification; `POST …/{id}/close` — close, `DELETE …/{id}` — delete |
| `POST /api/v1/enps/{id}/answer` | `{"score": 0–10, "comment"}` — anonymous response of key owner, once |
| `GET /api/v1/meetings?role=owner\|member&status=prepare\|action\|done&q=` | «Meetings and briefings» (like `meeting.*` in Portal): meetings visible to the key employee (organizer, secretary, participant; administrator — all): `[{"id", "title", "description", "place", "start", "minutes", "status", "owner", "keeper", "members", "prev_id", "next_id", "event_id", "protocol", "started", "finished"}]`; `GET …/meetings/{id}` — also `items` (agenda: `id`, `parent_id`, `num`, `title`, `reporter`, `minutes`, `result`, `tasks`) and `carried` — unresolved tasks from the previous meeting in the series |
| `POST /api/v1/meetings` | `{"title", "start" (RFC 3339 or `2026-10-27T10:00`), "minutes" (60), "place", "description", "owner_id", "keeper_id", "members": [ID], "prev_id"}` — meeting in organizer's calendar, participants receive invitation and notification; `PATCH …/{id}` — same fields plus `"protocol"`, `"status": "prepare\|action\|done"` (finished — read-only until `action`); `DELETE …/{id}`; `POST …/{id}/next {"start"}` — next meeting in the series (in a week): same people, unresolved items. Modified by organizer, secretary, and administrator |
| `POST /api/v1/meetings/{id}/items` | `{"title", "reporter_id", "minutes", "result", "parent_id"}` — agenda item (sub-item — with `parent_id`); `PATCH`/`DELETE …/items/{item}`; `POST …/items/{item}/tasks {"project": "KEY", "title", "description", "responsible_id", "deadline"}` — task for the item, response `{"key"}` |
| `GET /api/v1/pulse?period=today\|week\|month\|quarter` (or `?from=&to=` — dates, up to one year) `&dept=` | «Company pulse», as in Portal: to administrator — entire company, to head and deputy — their departments with sub-departments (for others — 403): `{"from", "to", "total", "active", "today", "involved", "index", "involved_tools", "days": [{"day", "users"}], "tools": [{"tool", "users", "percent", "daily"}], "departments": [{"id", "name", "depth", "total", "active", "involved", "index"}], "users": [{"id", "name", "position", "departments", "tools", "days", "involved"}]}`; tools — `tasks`, `im`, `feed`, `calendar`, `drive`, `docs`, `mail`, `crm`, `telephony`, `openlines`, `kb`, `bp`, `company`, `collabs`, `store`, `sites`, `marketing`, `booking`, `sign`, `bi`, `mobile` (from phone; not counted in involvement); involved — 4 tools or more over the period |
| `GET /api/v1/learning/courses?mine=1&q=` | «Learning»: courses visible to the key owner (instructors — all, including drafts), with their progress: `[{"id", "title", "description", "published", "author", "lessons", "done", "finished", "assigned", "due"}]`; `mine=1` — assigned and started; `GET /api/v1/learning/courses/{id}` — course with `items`: `[{"id", "kind" (lesson \| test), "chapter", "title", "body", "pass_pct", "attempts", "minutes", "done"}]` |
| `GET /api/v1/learning/assignments?course_id=&department_id=&status=active\|finished\|overdue` | Who has courses assigned (instructors — all, heads and deputies — employees of their departments, others — 403): `[{"user_id", "name", "course_id", "course", "done", "total", "finished", "overdue", "best_test_pct", "due", "assigned", "assigned_by"}]` |
| `POST /api/v1/learning/assignments` | `{"course_id", "to": ["employee ID", "dept:department ID"], "due": "2026-11-30"}` — assign course (department — its current employees, with sub-departments; head — only to own employees), new ones receive notification; response `{"assigned": N}`; `DELETE /api/v1/learning/assignments?course_id=&user_id=` — remove assignment |

Collabs (like `socialnetwork.collab.*` in Portal; scopes `collabs:read`, `collabs:write`). The key only sees collabs of
its employee (another's — `404`), permissions — as on the collab page; an archived collab cannot be modified (`422`), except
for restoration:

| Method | What |
|---|---|
| `GET /api/v1/collabs?q=&archived=1` | own collabs (`q` — by name and description, `archived=1` — archive): `[{"key", "name", "description", "my_role", "members", "guests", "archived", "photo_url", "chat", "counters": {"overdue", "comments"}}]` |
| `POST /api/v1/collabs` | `{"name", "description", "members": [employee and guest ids]}` — new collab, owner is the key's employee |
| `GET /api/v1/collabs/{key}` | `{"collab": {… "settings"}, "members": [{"id", "name", "role": "owner"\|"manager"\|"member", "guest"}]}` |
| `PATCH /api/v1/collabs/{key}` | `{"name", "description"}` — unpassed fields remain (who may change settings) |
| `PUT /api/v1/collabs/{key}/photo` | avatar: request body is the image itself (JPEG, PNG, WebP, GIF, up to 5 MB); `DELETE` — remove |
| `PATCH /api/v1/collabs/{key}/settings` | `{"invite_perm", "guest_invite_perm": "owner"\|"managers"\|"members", "chat_history", "guests_tasks", "guests_files", "edit_perm": "owner"\|"managers", "chat_perm": "members"\|"employees"\|"managers", "msg_ttl": 0\|3600\|86400\|604800\|2592000 (auto-deletion of chat messages in seconds; 0 — disabled), "events_members", "events_guests", "events_invites", "events_collab" (notifications in collab chat: members, guest joins, invites, collab itself)}` |
| `POST /api/v1/collabs/{key}/members` | `{"user_id", "role"}` — add member or change role; `DELETE …/members/{id}` — remove (own id — leave collab) |
| `GET /api/v1/collabs/{key}/invites` | guest invitations for the month, active first: `[{"id", "kind": "link"\|"email"\|"phone", "url", "email", "phone", "name", "created_by", "created_at", "expires_at", "state": "active"\|"revoked"\|"used"\|"expired", "uses", "used_by"}]` — who may invite guests |
| `POST /api/v1/collabs/{key}/invites` | `{"kind", "contact" (email or phone), "name", "days" (1–90, default 7), "send": true — email from employee's first mailbox in go24}` — invitation; response — same with `url` and `sent`. Link (`link`) is valid for everyone until expiration date, email and phone — one-time |
| `DELETE /api/v1/collabs/{key}/invites/{id}` | revoke invitation (those who joined by it remain) |
| `POST /api/v1/collabs/{key}/invites/{id}/resend` | «Invite again»: personal invitation where nobody has joined yet and which is not revoked remains active for `days` (default 7) from this moment; email is resent (`{"send": false}` — do not send) |
| `POST /api/v1/collabs/{key}/archive` | `{"archived": true\|false}` — to archive and back |
| `POST /api/v1/collabs/{key}/owner` | `{"user_id"}` — transfer owner rights to a member employee |
| `DELETE /api/v1/collabs/{key}?confirm=<name>` | delete collab (owner) |
| `GET /api/v1/collabs/{key}/log` | activity log (owner and moderators), newest first |
| `GET /api/v1/collabs/{key}/chats` | collab chats: main (`"main": true`) and group chats where the key owner is a member: `[{"room", "name", "main", "copilot"}]` (`copilot` — a «Chat with CoPilot») |
| `POST /api/v1/collabs/{key}/chats` | `{"name", "user_ids"}` — create a group chat in collab (any member); anyone not in the collab will become members (by the «Who may invite» permission); `"copilot": true` — a «Chat with CoPilot» (not for guests; named «Chat with CoPilot» by default): CoPilot answers every message and knows the collab's people and tasks; response — `{"room", "name", "copilot"}` |
| `PUT /api/v1/collabs/{key}/chats/{room}` | add own group chat to collab (its owner, if allowed to invite to collab); chat participants become collab members, history is preserved; `DELETE` — remove chat from collab (chat owner, collab owner and moderators), collab guests leave the chat |
| `POST /api/v1/users/{id}/employee` | turn guest into employee (administrator key) |

Workflows (like `bizproc.workflow.*`, `bizproc.workflow.template.*`, `bizproc.task.*` in Portal; scopes `bp:read` —
read, `bp:write` — start, decisions, termination). Templates are created and modified by portal administrators (they also see
disabled templates and steps), an active template can be started by anyone; a process is visible to the starter, anyone with an assigned task in it, anyone
who can read its CRM card, and administrators. Steps are numbered from 1, matching the step's `goto`; the template id is also visible in the URL
of the designer (`/bp/templates/<id>`):

| Method | What |
|---|---|
| `GET /api/v1/bp/templates?entity=` | templates (`deal`, `lead`, `contact`, `company`, `file` — document of the Company drive, `entity_id` — file id, `list` — items of list `list_id`, `entity_id` — item id, `none` — without card): `[{"id", "name", "description", "entity", "type_id", "active", "auto_create", "auto_change", "fields", "steps", "url"}]`; `steps` — to administrators |
| `GET /api/v1/bp/templates/{id}` | template |
| `POST /api/v1/bp/templates` | create (administrators): `{"name", "description", "entity", "type_id", "active", "auto_create", "auto_change", "fields": [{"key", "label", "kind": "string"\|"text"\|"number"\|"date", "required"}], "steps": [{"kind", "text", "to", …}]}` — steps in the format returned by `GET` |
| `PATCH /api/v1/bp/templates/{id}` | update: unpassed fields remain |
| `DELETE /api/v1/bp/templates/{id}` | delete template along with its processes, as in the designer |
| `POST /api/v1/bp/templates/{id}/start` | start (`bizproc.workflow.start`): `{"entity_id": … (CRM card template), "values": {…}}`; response — process; the previous URL `POST /api/v1/bp/{id}/start` also works |
| `GET /api/v1/bp/instances` | own processes, newest first; `?status=`; `?entity=deal&entity_id=…` — card processes; `?all=1&status=&template_id=&limit=&offset=` — all (administrators) |
| `GET /api/v1/bp/instances/{id}` | process: `{"id", "template_id", "name", "entity", "entity_id", "status": "running"\|"done"\|"rejected"\|"failed"\|"stopped", "step", "label" (status of «Set status» step), "values", "started_by", "can_stop", "commands" (buttons of «Commands» step that the key owner can click), "fields", "steps": [{"n", "kind", "text", "state": "done"\|"now"\|""}], "tasks", "log": [{"step", "actor", "kind", "text", "at"}], "url"}` |
| `POST /api/v1/bp/instances/{id}/stop` | «Stop» (`bizproc.workflow.terminate`): `{"reason"}` — starter, anyone who can modify the card, administrators |
| `POST /api/v1/bp/instances/{id}/command` | click button of «Commands» step awaited by the process: `{"step": step number, "command": "name"}` — step employees, administrators; response — process |
| `DELETE /api/v1/bp/instances/{id}` | delete process with history (`bizproc.workflow.kill`, administrators) |
| `GET /api/v1/bp/tasks` | tasks awaiting decision by the key owner (`bizproc.task.list`): `[{"id", "process_id", "process", "kind": "approve"\|"review"\|"request", "text", "step", "fields", "can_reject", "url"}]` |
| `POST /api/v1/bp/tasks/{id}/complete` | decision (`bizproc.task.complete`): `{"decision": "approve"\|"reject"\|"seen"\|"answer", "comment", "values": {…} (response to request)}`; response — process |

Lists (like `lists.get`, `lists.field.get`, `lists.element.*` in Portal; scopes `lists:read` — read, `lists:write` —
elements). The key sees lists visible to its owner, and modifies elements where allowed; the lists themselves (fields,
permissions) are configured by administrators on the «Workflows → Lists» page. Workflows of a list are also triggered
by elements added and modified via REST:

| Method | What |
|---|---|
| `GET /api/v1/lists` | lists: `[{"id", "name", "description", "fields": [{"key", "label", "kind", "required", "options", "default"}], "elements", "can_write", "url"}]` |
| `GET /api/v1/lists/{id}` | list with fields |
| `GET /api/v1/lists/{id}/elements` | elements: `?q=` (in name and fields), `?sort=name\|created\|changed\|<field key>`, `?desc=1`, `?limit=` (up to 500, default 50), `?offset=`; `[{"id", "list_id", "name", "values": {key: value}, "created_by", "created_at", "changed_by", "changed_at", "url"}]` |
| `POST /api/v1/lists/{id}/elements` | add: `{"name", "values": {…}}` — «Yes/No» `Y`/`N`, employee — id, date `YYYY-MM-DD`, date and time `YYYY-MM-DDThh:mm`; omitted fields take default value; validation error — 422 |
| `GET /api/v1/lists/{id}/elements/{element}` | element |
| `PATCH /api/v1/lists/{id}/elements/{element}` | update: unpassed fields remain |
| `DELETE /api/v1/lists/{id}/elements/{element}` | delete |

Tasks, projects, comments, notifications, messenger — see `internal/web/api.go`.
