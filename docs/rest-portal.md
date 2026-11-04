# REST compatible with the portal

Integrations written for the portal — the n8n the portal node, 1C exchange, Zapier, CRest/portalphpsdk scripts, marketplace
connectors — call REST methods by name. go24 answers those calls in the portal format, so such an integration works
with go24 after changing the portal address and the key (GO24DEV-758).

## Addresses

| Call | Address |
|---|---|
| Incoming webhook | `https://<go24>/rest/<employee>/<key>/<method>` (`.json` at the end is optional) |
| Application (OAuth 2.0) | `https://<go24>/rest/<method>?auth=<access_token>`, or the token in `Authorization: Bearer` |

`<employee>` is the employee's number (`ID` in `user.current` or their go24 uuid; the key is a go24
API key of that employee («Settings → API keys»). The key's scopes hold: a method of a scope the key lacks answers
`insufficient_scope`. An application call also takes a go24 API key in `auth`.

A path with no dot (`…/crm/deals`, `…/me`) is still go24 REST `/api/v1/<path>`.

## Parameters

In the query, in a form (`application/x-www-form-urlencoded`, `multipart/form-data`) with PHP arrays —
`filter[>ID]=5&select[]=ID&select[]=TITLE&fields[PHONE][0][VALUE]=…` — or as JSON. Lists are paged by 50: `start` is
where the page begins.

## Answers

```json
{"result": [...], "total": 120, "next": 50, "time": {"start": …, "finish": …, "duration": …, "processing": …,
 "date_start": "2026-10-28T10:00:00+01:00", "date_finish": "…", "operating": 0}}
```

`total` and `next` come with lists only; `next` is absent on the last page. Errors:

| HTTP | `error` | When |
|---|---|---|
| 401 | `NO_AUTH_FOUND` | no token, a wrong, revoked or expired key, another employee's webhook address |
| 401 | `insufficient_scope` | the key lacks the method's scope |
| 403 | `ACCESS_DENIED` | no right to the object, access by IP |
| 404 | `ERROR_METHOD_NOT_FOUND` | no such method, or its section is switched off («Settings → Tools») |
| 400 | `ERROR_NOT_FOUND` | no such object |
| 400 | `ERROR_CORE` | other errors (wrong data) |
| 400 | `WRONG_AUTH_TYPE` | `app.info` called with a webhook |

`error_description` is in the language of `Accept-Language`.

## batch

`batch` runs up to 50 commands: `cmd[<name>]=<method>?<query>`, `halt=1` stops at the first error. `$result[<name>][FIELD]…`
in a command is a value of another command's result; a command runs after the ones it refers to, the rest in the order of
their names. The answer: `result.result`, `result.result_error`, `result.result_total`, `result.result_next`,
`result.result_time` (an empty part is `[]`. A batch inside a batch is not run.

## Methods

| Method | What |
|---|---|
| `server.time` | the server time (ISO 8601 with the zone) |
| `profile` | the caller: `ID`, `ADMIN`, `NAME`, `LAST_NAME`, `PERSONAL_GENDER`, `PERSONAL_PHOTO`, `TIME_ZONE`, `TIME_ZONE_OFFSET` |
| `user.current` | the caller in the fields of `user.get` |
| `user.get` | employees (scope `company:read`): `FILTER` by `ID`, `ACTIVE`, `EMAIL`, `NAME`, `LAST_NAME`, `UF_DEPARTMENT`, `USER_TYPE`; `SORT`/`ORDER`; fields `ID`, `XML_ID` (go24 uuid), `ACTIVE`, `NAME`, `LAST_NAME`, `SECOND_NAME`, `EMAIL`, `LAST_LOGIN`, `IS_ONLINE`, `TIME_ZONE`, `PERSONAL_*`, `WORK_PHONE`, `WORK_POSITION`, `UF_PHONE_INNER`, `UF_DEPARTMENT` (department numbers), the profile's own fields `UF_USR_*`, `UF_EMPLOYMENT_DATE`, `USER_TYPE` (`employee`, `extranet` — a collab guest, seen by administrators) |
| `scope` | the portal scopes of the key (`full=true` — all go24 has) |
| `methods` | the methods the key may call (`full=true` — all, `scope=crm` — of one scope) |
| `method.get` | `name=` → `{isExisting, isAvailable}` |
| `app.info` | the calling application (an application token only) |
| `batch` | see above |

go24 scopes map to the portal scopes: `tasks` → `task`, `crm` → `crm`, `im` → `im`, `company` → `department`, `bp` →
`bizproc`, `calendar` → `calendar`, `disk` → `disk`, `lists` → `lists`, `openlines` → `imopenlines`, `telephony` →
`telephony`, `projects` → `sonet_group`, `feed` → `log`; `user` and `user_basic` are always there.

Employees and departments have numbers for the portal compatibility (migration 601): given once, in the order of creation,
never reused.

## CRM

`crm.deal.*`, `crm.lead.*`, `crm.contact.*`, `crm.company.*` (scope `crm`; `fields`, `list`, `get` — `crm:read`, `add`,
`update`, `delete` — `crm:write`) — GO24DEV-761. The rights of CRM hold: a list gives the cards the caller may read, a
change goes the way of go24 REST (the same checks, robots, events, outgoing webhooks).

| Method | What |
|---|---|
| `crm.<entity>.fields` | the fields: `type`, `isRequired`, `isReadOnly`, `isImmutable`, `isMultiple`, `title`, `statusType` |
| `crm.<entity>.list` | `filter`, `order`, `select`, `start` — a page of 50 with `total` and `next` |
| `crm.<entity>.get` | `id` → the card with all its fields, `PHONE`, `EMAIL`, `WEB` among them |
| `crm.<entity>.add` | `fields` → the number of the new card |
| `crm.<entity>.update` | `id`, `fields` → `true`; only the fields given change |
| `crm.<entity>.delete` | `id` → `true`; the card goes to the CRM's recycle bin |

Fields:

| Entity | Fields |
|---|---|
| deal | `ID`, `TITLE`, `TYPE_ID`, `CATEGORY_ID`, `STAGE_ID`, `STAGE_SEMANTIC_ID`, `CLOSED`, `OPPORTUNITY`, `CURRENCY_ID`, `COMPANY_ID`, `CONTACT_ID`, `CONTACT_IDS`, `BEGINDATE`, `CLOSEDATE`, `SOURCE_ID`, `UTM_*` |
| lead | `ID`, `TITLE`, `NAME`, `SECOND_NAME`, `LAST_NAME`, `COMPANY_TITLE`, `SOURCE_ID`, `SOURCE_DESCRIPTION`, `STATUS_ID`, `STATUS_SEMANTIC_ID`, `OPPORTUNITY`, `CURRENCY_ID`, `IS_RETURN_CUSTOMER`, `CONTACT_ID`, `COMPANY_ID`, `HAS_PHONE`, `HAS_EMAIL`, `PHONE`, `EMAIL`, `UTM_*` |
| contact | `ID`, `NAME`, `SECOND_NAME`, `LAST_NAME`, `POST`, `TYPE_ID`, `SOURCE_ID`, `COMPANY_ID`, `HAS_PHONE`, `HAS_EMAIL`, `PHONE`, `EMAIL` |
| company | `ID`, `TITLE`, `COMPANY_TYPE`, `INDUSTRY`, `ADDRESS`, `WEB`, `HAS_PHONE`, `HAS_EMAIL`, `PHONE`, `EMAIL` |
| all | `ASSIGNED_BY_ID`, `CREATED_BY_ID`, `DATE_CREATE`, `DATE_MODIFY`, `COMMENTS` |

- Numbers: cards, pipelines and stages have numbers (migration 619), given once and never reused; the recycle bin
  restores a card with its number. `CATEGORY_ID` 0 is the main pipeline. `ASSIGNED_BY_ID`, `COMPANY_ID`, `CONTACT_ID` are
  numbers (a go24 uuid is taken too).
- Stages: the built-in ones by their the portal codes (`NEW`, `PREPARATION`, `PREPAYMENT_INVOICE`, `EXECUTING`,
  `FINAL_INVOICE`, `WON`, `LOSE`; leads — `NEW`, `IN_PROCESS`, `PROCESSED`, `CONVERTED`, `JUNK`), custom ones `UC_<number>`;
  a stage of another pipeline has the prefix `C<CATEGORY_ID>:`. `add` with `STAGE_ID` `C2:NEW` puts the deal into the
  pipeline 2; `update` does not move a deal to another pipeline.
- Lists of values (`SOURCE_ID`, `TYPE_ID`, `COMPANY_TYPE`, `INDUSTRY`) are the built-in codes (`CALL`, `CLIENT`, `IT`…),
  custom items — their go24 id.
- `PHONE`, `EMAIL`: `[{"ID", "VALUE", "VALUE_TYPE", "TYPE_ID"}]`, `ID` is the place on the card from 1; `update` with an
  `ID` changes that one (`"DELETE": "Y"` removes it), without — adds one. `WEB` — the company's site.
- go24 keeps a person's name whole: `NAME` gives it all; `NAME`, `SECOND_NAME`, `LAST_NAME` given are joined.
- Values gives them: numbers and sums as text (`"1500.00"`), dates in ISO 8601 with the zone, `Y`/`N`.
- `filter`: `FIELD` (equals; a list — any of; empty — not set), `=`, `!`, `>`, `<`, `>=`, `<=`, `%` (contains), `=%` (LIKE
  as given), `@` (in a list), `!@`; `PHONE` and `EMAIL` find the card by a phone (any format) or an e-mail. A field go24
  does not have is skipped.
- `select`: by default and with `*` — all but `PHONE`, `EMAIL`, `WEB`, `CONTACT_IDS` and the user fields; `UF_*` — the
  user fields; `ID` always. `get` gives all of them.

### User fields, links, duplicates

GO24DEV-957; scope `crm` (`crm:read`, changes — `crm:write`).

The own fields of deals, leads, contacts and companies («Fields» of the CRM settings) are `UF_CRM_<code>` — the code
given by `userfield.add` — or `UF_CRM_<number>` for a field added in go24 (migration 903: fields have numbers, given once
and never reused). They are in `crm.<entity>.fields` (`isDynamic: true`, `title`, `items` of a list), `get`, `list`
(`select: ["UF_*"]` or by name; `filter` as other fields), `add` and `update` (only the fields given change; a card the
caller may change).

| Method | What |
|---|---|
| `crm.<entity>.userfield.list` | `filter` (`ID`, `FIELD_NAME`, `USER_TYPE_ID`, `MANDATORY`) → `[{ID, ENTITY_ID, FIELD_NAME, USER_TYPE_ID, SORT, MULTIPLE, MANDATORY, EDIT_FORM_LABEL, LIST_COLUMN_LABEL, LIST_FILTER_LABEL, LIST, …}]` |
| `crm.<entity>.userfield.get` | `id` — the field |
| `crm.<entity>.userfield.add` | `fields {FIELD_NAME, USER_TYPE_ID, EDIT_FORM_LABEL, LIST, MANDATORY, SORT}` → the number; an administrator |
| `crm.<entity>.userfield.update` | `id`, `fields {EDIT_FORM_LABEL, SORT, MANDATORY, LIST}`; an administrator |
| `crm.<entity>.userfield.delete` | `id` — the field with its values on every card; an administrator |
| `crm.userfield.types`, `crm.userfield.fields`, `crm.userfield.enumeration.fields` | the types and the fields of a user field and of a list item |
| `crm.deal.contact.items.get` | `id` → `[{CONTACT_ID, SORT, ROLE_ID, IS_PRIMARY}]`, the main contact first |
| `crm.deal.contact.items.set`, `items.delete` | `id`, `items [{CONTACT_ID, SORT, IS_PRIMARY}]` — the contacts of the deal instead of its own; `delete` — none |
| `crm.deal.contact.add`, `delete`, `fields` | `id`, `fields {CONTACT_ID, IS_PRIMARY}` — one contact more (the main one with `IS_PRIMARY=Y`) or less |
| `crm.contact.company.items.*`, `crm.contact.company.add/delete/fields` | the same with `COMPANY_ID` |
| `crm.company.contact.items.*`, `crm.company.contact.add/delete/fields` | the contacts of a company, `CONTACT_ID` |
| `crm.duplicate.findbycomm` | `type` (`PHONE`, `EMAIL`), `values` (up to 20), `entity_type` (`LEAD`, `CONTACT`, `COMPANY`; none — all) → `{CONTACT: [numbers], …}` of the cards the caller may read; none — `[]` |
| `crm.multifield.fields` | the fields of `PHONE`, `EMAIL`, `WEB`: `ID`, `TYPE_ID`, `VALUE`, `VALUE_TYPE` |

- Types: `string`, `double` (`integer` is taken as a number), `date` (`datetime` is kept as a date), `boolean` (`1`/`0`;
  `Y`, `1`, `true` are taken), `enumeration`, `url`. go24 keeps one value of a field: `MULTIPLE=Y` is refused, the
  first value of an array is taken.
- A list's value is the `ID` of its item — its place from 1 (the text of the item is taken too). `userfield.update`
  with `LIST`: `{ID, VALUE}` renames the item with the values of the cards, `{ID, DEL: "Y"}` removes it with them,
  `{VALUE}` adds one; the places of the items after a removed one move up.
- `FIELD_NAME` is taken with `UF_CRM_` or without: up to 40 Latin letters, digits and `_`; a code of digits only is at
  least 7 long, so that it is not taken for the number of a field added in go24. `EDIT_FORM_LABEL` (or
  `LIST_COLUMN_LABEL`, `LABEL`) — a text or texts by language (`{"en": …, "ru": …}`): the name of the field.
- A contact has one company in go24: `crm.contact.company.items.set` with several keeps the main one (`IS_PRIMARY=Y`
  or the least `SORT`); `crm.contact.company.add` replaces the company. Changes go the way of `update` (rights, robots,
  history).

### Lists, pipelines, deeds, comments

GO24DEV-834; scope `crm` (`crm:read`, changes — `crm:write`).

| Method | What |
|---|---|
| `crm.status.list` | `filter` (`ENTITY_ID`, `STATUS_ID`, any field; equals), `order` (one field) → `[{ID, ENTITY_ID, STATUS_ID, NAME, NAME_INIT, SORT, SYSTEM, CATEGORY_ID, COLOR, SEMANTICS, EXTRA}]` |
| `crm.status.get`, `crm.status.fields` | `id` — the item |
| `crm.status.add` | administrators: `fields {ENTITY_ID, NAME, SORT, COLOR, SEMANTICS}` → the new `ID`; a stage (`STATUS`, `DEAL_STAGE`, `DEAL_STAGE_<n>`, `DYNAMIC_<t>_STAGE_<n>`) is a working one, `SEMANTICS` `F` — a failure one, at its `SORT` among the stages of its kind; `STATUS_ID` is go24's own (`UC_<ID>`) |
| `crm.status.update` | `id`, `fields {NAME, COLOR, SORT}` — a stage's name, color and place; a dictionary item's name |
| `crm.status.delete` | `id`; a built-in item only with `params {FORCED: "Y"}`; a stage with cards stays — move them first; the success stage and the last stage of its kind stay |
| `crm.status.entity.types` | the lists: `STATUS` (lead stages), `SOURCE`, `CONTACT_TYPE`, `COMPANY_TYPE`, `INDUSTRY`, `DEAL_TYPE`, `DEAL_STAGE` and `DEAL_STAGE_<n>` of every pipeline, `DYNAMIC_<t>_STAGE_<n>` of the smart processes |
| `crm.status.entity.items` | `entityId` → `[{NAME, SORT, STATUS_ID}]` |
| `crm.dealcategory.list`, `get`, `fields` | the pipelines but the main one: `[{ID, CREATED_DATE, NAME, IS_LOCKED, SORT}]` |
| `crm.dealcategory.add`, `update`, `delete` | administrators: `fields {NAME, SORT}` → the new `ID` (a new pipeline has the stages of one made in the CRM), `id`; a pipeline with deals and the main one are not deleted |
| `crm.dealcategory.default.get` | `{ID: 0, NAME}` |
| `crm.dealcategory.stage.list` | `id` → the stages of the pipeline `[{NAME, SORT, STATUS_ID}]` |
| `crm.category.list`, `get` | `entityTypeId` 2 or a smart process's (128…), `id` → `{categories: [{id, name, sort, entityTypeId, isDefault}]}`, `{category}` |
| `crm.category.add`, `update`, `delete`, `fields` | administrators: `entityTypeId`, (`id`), `fields {name, sort}` → `{category}`; `delete` → `[]` |
| `crm.stagehistory.list` | `entityTypeId` 1, 2 or a smart process's, `filter` (`OWNER_ID`, `TYPE_ID`, `STAGE_ID`/`STATUS_ID`, `CATEGORY_ID`, `>=CREATED_TIME`…), `order`, `select`, 50 a page → `{items: [{ID, TYPE_ID, OWNER_ID, CREATED_TIME, CATEGORY_ID, STAGE_SEMANTIC_ID, STAGE_ID}]}` (a lead's — `STATUS_SEMANTIC_ID`, `STATUS_ID`) |
| `crm.currency.list`, `get`, `base.get` | `[{CURRENCY, AMOUNT_CNT, AMOUNT, SORT, BASE, FULL_NAME, DATE_UPDATE}]`; `AMOUNT` — the rate to the base one |
| `crm.enum.ownertype`, `crm.enum.activitytype` | `[{ID, NAME, SYMBOL_CODE, SYMBOL_CODE_SHORT}]`: 1 lead, 2 deal, 3 contact, 4 company; deeds 1 meeting, 2 call, 6 to-do |
| `crm.activity.fields`, `list`, `get` | «Дела»: `filter` (`OWNER_TYPE_ID`, `OWNER_ID`, `COMPLETED`, `RESPONSIBLE_ID`, `>DEADLINE`…), `order`, `select`, 50 a page |
| `crm.activity.add` | `fields` with `OWNER_TYPE_ID`, `OWNER_ID`, `SUBJECT`, `TYPE_ID` (or `PROVIDER_ID` `CRM_TODO`, `CRM_MEETING`, `VOXIMPLANT_CALL`), `DESCRIPTION`, `RESPONSIBLE_ID`, `DEADLINE` (`END_TIME`, `START_TIME`), `COMPLETED` → the deed's number |
| `crm.activity.update`, `delete` | `id`, `fields` — only the fields given; `COMPLETED` `Y` completes the deed, `N` reopens it |
| `crm.timeline.comment.list` | `filter[ENTITY_TYPE]` (`deal`, `lead`, `contact`, `company`; `2`, `D` are taken too), `filter[ENTITY_ID]` → `[{ID, CREATED, ENTITY_ID, ENTITY_TYPE, AUTHOR_ID, COMMENT}]`, newest first |
| `crm.timeline.comment.get`, `add`, `update`, `delete`, `fields` | `id`, `fields {ENTITY_TYPE, ENTITY_ID, COMMENT}`; `add` and `update` → the comment's number; one's own comment (or an administrator) is changed |

- Stages and dictionary items are numbered from one sequence (migration 663), so `ID` of `crm.status.list` is unique. Deeds have numbers too; a comment's number is its timeline line.
- A deed of a card is read by who may read the card and by its responsible; added on a card the caller may change;
  changed and deleted by its responsible or who may change the card. A comment is added by who may read the card.
- With no `DEADLINE` a deed is due now; a moved deadline is reminded of again.
- The lists and the pipelines are changed by the administrators, as in the CRM's settings (GO24DEV-976). A dictionary
  item's `SORT` is not set through the REST yet.
- The stage history (GO24DEV-976, migration 940) is written by go24 whatever moved the card — the kanban, a robot, the
  REST, a deleted stage. `TYPE_ID`: 1 created, 2 moved to a working stage, 3 to a final one (success or failure), 5
  moved to another pipeline. Numbers are numbers in it. The cards made before the migration have one
  row: their stage then, at their creation. Only the history of the cards the caller may read is given.

### Universal methods and smart processes

GO24DEV-952; scope `crm` (`crm:read`, changes — `crm:write`). `crm.item.*` work with the cards of any kind by
`entityTypeId`: 1 leads, 2 deals, 3 contacts, 4 companies, 128 and above — smart processes.

| Method | What |
|---|---|
| `crm.item.fields` | `entityTypeId` → `{fields: {title: {type, isRequired, isReadOnly, isMultiple, title, upperName}…}}` |
| `crm.item.list` | `entityTypeId`, `filter`, `order`, `select`, `start` → `{items: [...]}`, 50 a page; the filter's operators as in `crm.deal.list` |
| `crm.item.get` | `entityTypeId`, `id` → `{item}` |
| `crm.item.add`, `update` | `entityTypeId`, (`id`), `fields` → `{item}` as it is now; `update` changes the fields given |
| `crm.item.delete` | `entityTypeId`, `id` — the card goes to the recycle bin → `[]` |
| `crm.type.list`, `get`, `getByEntityTypeId`, `fields` | the smart processes: `{types: [{id, entityTypeId, title, createdTime, isCategoriesEnabled…}]}`, `{type}` |
| `crm.type.add`, `update`, `delete` | administrators: `fields {title}` → `{type}`; a new one has its pipeline and stages; one with cards is not deleted |
| `crm.category.list`, `get` | `entityTypeId` 2 or a smart process's → its pipelines |

- The fields are those of `crm.deal.*` and the others in camelCase: `assignedById`, `categoryId`, `opportunity`,
  `contactIds`, `utmSource`; `stageId` is also a lead's `STATUS_ID`, `createdTime`/`updatedTime` — `DATE_CREATE`/
  `DATE_MODIFY`, `createdBy` — `CREATED_BY_ID`, a company's `typeId` — `COMPANY_TYPE`. Numbers and ids are numbers.
  Phones, e-mails and websites are `fm: [{id, typeId (PHONE, EMAIL, WEB), valueType, value}]`, given in `fields` the
  same way (`id` changes that one, `delete: "Y"` removes it).
- A smart process's number (`entityTypeId`, migration 925) is also its `id`. Its cards are numbered as deals, its
  pipelines as the deal pipelines; its stages are `DT<entityTypeId>_<categoryId>:NEW` (`PREPARATION`, `CLIENT`,
  `SUCCESS`, `FAIL`, `UC_<n>` of one's own); `crm.status.list` gives them with `ENTITY_ID`
  `DYNAMIC_<entityTypeId>_STAGE_<categoryId>`. A new card without `categoryId` goes to the first pipeline.
- The rights of the CRM are those of deals for a smart process's cards.
- Not there yet: quotes (7) and invoices (31) through `crm.item.*`, `crm.item.batchImport`, user fields `ufCrm…`.

### Products and product rows

GO24DEV-928; scope `crm` (`crm:read`, changes — `crm:write`). The catalog of «Товары» and the products of deals and
leads.

| Method | What |
|---|---|
| `crm.product.fields`, `list`, `get` | `filter`, `order`, `select`, `start` → `ID`, `CATALOG_ID`, `NAME`, `XML_ID` (the SKU), `ACTIVE`, `SECTION_ID`, `TYPE` (1 product, 4 variation, 7 service), `PRICE`, `CURRENCY_ID`, `VAT_ID`, `VAT_INCLUDED`, `MEASURE`, `DESCRIPTION`, `DATE_CREATE`, `CREATED_BY` |
| `crm.product.add`, `update`, `delete` | `fields` — only the fields given; `TYPE` 7 makes a service; `add` → the product's number; `delete` removes it with its variations, the cards keep their rows |
| `crm.productsection.fields`, `list`, `get`, `add`, `update`, `delete` | the catalog's sections: `ID`, `CATALOG_ID`, `SECTION_ID` (the parent), `NAME`, `SORT`; a deleted section's products and subsections go to its parent |
| `crm.catalog.list`, `get`, `fields` | the one catalog, `ID` 1 |
| `crm.measure.list`, `get` | the units: `ID`, `CODE` (ОКЕИ), `MEASURE_TITLE`, `SYMBOL_RUS`, `SYMBOL_INTL`, `IS_DEFAULT` |
| `crm.vat.list`, `get` | the VAT rates 0, 5, 7, 10, 20 %: `ID` 1–5, `RATE`, `NAME` |
| `crm.deal.productrows.get`, `crm.lead.productrows.get` | `id` → `[{ID, OWNER_ID, OWNER_TYPE, PRODUCT_ID, PRODUCT_NAME, PRICE, PRICE_EXCLUSIVE, PRICE_NETTO, PRICE_BRUTTO, QUANTITY, DISCOUNT_TYPE_ID, DISCOUNT_RATE, DISCOUNT_SUM, TAX_RATE, TAX_INCLUDED, MEASURE_CODE, MEASURE_NAME, SORT, TYPE, STORE_ID, RESERVE_QUANTITY, DATE_RESERVE_END}]` |
| `crm.deal.productrows.set`, `crm.lead.productrows.set` | `id`, `rows` — the card's rows replaced; the amount is counted from them |
| `crm.productrow.fields`, `list` | `filter[OWNER_TYPE]` (`D`, `L`), `filter[OWNER_ID]` → the card's rows |
| `crm.item.productrow.fields`, `list`, `get` | the same rows in camelCase (`productName`, `priceExclusive`…): `filter {=ownerType, =ownerId}` → `{productRows}`, `id` → `{productRow}` |
| `crm.item.productrow.add`, `update`, `delete` | `fields {ownerType, ownerId, …, sort}` → `{productRow}` at its place by `sort` (else the last); `update` — the fields given; `delete` → `null` |
| `crm.item.productrow.set` | `ownerType`, `ownerId`, `productRows` → `{productRows}` |

- Products, sections, units and warehouses are numbered (migration 851); a row's `ID` is its number and stays while
  the row does (an `ID` of the card's row given to `set` keeps it).
- Prices: `PRICE` is the price of one with the discount and the tax, `PRICE_EXCLUSIVE` — with the discount and without
  the tax, `PRICE_NETTO` — without both, `PRICE_BRUTTO` — with the tax and without the discount. A row takes the first
  given of `PRICE`, `PRICE_EXCLUSIVE`, `PRICE_NETTO`, `PRICE_BRUTTO`; a row of a catalog product without them — the
  product's price, and its name, unit and VAT. go24 keeps the discount in percent: an absolute one
  (`DISCOUNT_TYPE_ID` 1, `DISCOUNT_SUM` off the price without the tax) is kept as its percent, to the cent.
- `MEASURE_CODE` is the ОКЕИ code of the unit, `MEASURE_NAME` its symbol. `STORE_ID` and `RESERVE_QUANTITY` reserve a
  deal's catalog product on a warehouse (none given — as «Складской учёт» reserves).
- The catalog is read by everyone of the CRM and changed by administrators, as on its pages; a card's rows — by who may
  read or change the card.
- Not there: the rows of quotes and invoices (`ownerType` `Q`, `SI`), `crm.product.property.*`, pictures
  (`PREVIEW_PICTURE`, `DETAIL_PICTURE`) and `catalog.*` of the new catalog API — the products' REST of go24 is at
  `/api/v1/crm/products`.

### Document generator

GO24DEV-980; scope `documentgenerator` (`documents:read`, changes — `documents:write`). The CRM's templates, the
documents of a card and «Нумераторы»; a card is `entityTypeId` (1 leads, 2 deals, 3 contacts, 4 companies, from 128 the
smart processes) and `entityId`. Templates, documents and numerators have numbers (migration 955).

| Method | What |
|---|---|
| `crm.documentgenerator.template.list` | the templates the caller may make documents by («Кому доступен»; with «Changing templates» — all); `filter` by `id`, `name` (`%name`), `entityTypeId`, `numeratorId`; `start` → `{templates: […]}` |
| `crm.documentgenerator.template.get` | `id` → `{template: {id, name, entityTypeId: ["2"], numeratorId, users, fileName, download, createTime, …}}` |
| `crm.documentgenerator.template.getfields` | `id`, the card (`entityTypeId`, `entityId`), `values` → `{templateFields: {name: {title, value, default, required, hideRow}}}` — the placeholders with the card's values (or the field's value by default), titles and settings from the template's «Fields» |
| `crm.documentgenerator.template.add` | `fields[name]`, `fields[file]` (the DOCX: base64, `[name, base64]` or `{fileData: …}`), `fields[entityTypeId]` (one kind), `fields[numeratorId]`, `fields[users]`, `fields[active]`, `fields[sort]`, `fields[withStamps]` → `{template}` (the right «Changing templates») |
| `crm.documentgenerator.template.update` | `id`, `fields[name]`, `fields[file]`, `fields[numeratorId]`, `fields[users]`, `fields[active]`, `fields[sort]`, `fields[withStamps]` → `{template}` (the right «Changing templates») |
| `crm.documentgenerator.template.delete` | `id` (the right «Changing templates»); the documents made by it stay |
| `crm.documentgenerator.document.list` | `filter[entityTypeId]`, `filter[entityId]` (required) and by `templateId`, `number`, `title`; `start` → `{documents: […]}`, the newest first |
| `crm.documentgenerator.document.get` | `id` → `{document: {id, title, number, templateId, entityTypeId, entityId, values, stampsEnabled, downloadUrl, pdfUrl, publicUrl, createdBy, createTime, updateTime}}` |
| `crm.documentgenerator.document.getfields` | `id`, `values` → `{documentFields}` — the template's placeholders with the values the document was made with |
| `crm.documentgenerator.document.add` | `templateId`, `entityTypeId`, `entityId`, `values` (over the card's), `stampsEnabled` (`Y` by default) → `{document}` |
| `crm.documentgenerator.document.update` | `id`, `values`, `stampsEnabled` → `{document}` made again; the number stays |
| `crm.documentgenerator.document.delete` | `id` |
| `crm.documentgenerator.document.enablepublicurl` | `id`, `status` (`1` — on, `0` — off) → `{publicUrl}` (`null` — off) |
| `crm.documentgenerator.numerator.list`, `get` | `{numerators: […]}`, `{numerator: {id, name, template, settings}}` (the right «Changing templates» or «Changing settings») |
| `crm.documentgenerator.numerator.add`, `update` | `fields[name]`, `fields[template]` (`{NUMBER}`, `{YEAR}`, `{MONTH}`, `{DAY}` and text), `fields[settings][Legacy_Main_Numerator_Generator_SequentNumberGenerator]` — `start`, `step`, `periodicBy` (`day`, `month`, `year`; empty — never), `length` (the digits of `{NUMBER}`) → `{numerator}` (the right «Changing settings») |
| `crm.documentgenerator.numerator.delete` | `id` — one no template numbers by (the right «Changing settings») |
| `documentgenerator.role.list`, `get` | `{roles: […]}`, `{role: {id, name, code, isDefault, permissions, accessCodes}}` — the roles of «Document generator → Access rights» (the right «Changing settings») |
| `documentgenerator.role.add`, `update` | `fields[name]`, `fields[permissions]` — `{TEMPLATES: {MODIFY}, DOCUMENTS: {VIEW, MODIFY}, SETTINGS: {MODIFY}}`, `X` (also `A`, `D`) — allowed, `""` — not; `update` keeps the permissions not given → `{role}` |
| `documentgenerator.role.delete` | `id`; the default role («All employees») is not deleted |
| `documentgenerator.role.fillaccesses` | `accesses: [{roleId, accessCode}, …]` — the roles' members anew (`U<number>`, `D<number>`/`DR<number>`); the roles not listed are left without members |

- `users` of a template are access codes: `UA` — all, `U<number>` — an employee, `D<number>`/`DR<number>` — a department
  with its sub-departments.
- `download` (a template's DOCX), `downloadUrl` (a document's DOCX) and `pdfUrl` (its PDF, made by the document server)
  are signed links of go24 (`/rest/docfile?token=…`): they work without a key for a day, while the employee is active and
  still may use the template or see the card.
- The rights are those of the card's page (those who see the card see its documents, those who may change it change
  them) and of the generator's roles (GO24DEV-987): «Viewing documents» and «Changing documents». Not there: `crm.documentgenerator.document.upload`, `region` and `code` of templates, templates of
  invoices and HR documents, `imageUrl`.

## Tasks

`tasks.task.*`, `task.commentitem.*`, `task.checklistitem.*` (scope `task`; reading — `tasks:read`, changes —
`tasks:write`) — GO24DEV-820; `task.elapseditem.*`, `task.stages.*`, `task.dependence.*`, `tasks.task.result.*`,
`history.list`, `counters.get`, `task.planner.getlist`, `startwatch`, `stopwatch` — GO24DEV-995. The rights of go24 tasks hold: a list gives the tasks the caller sees, a change goes the way
of go24 (the same checks, robots, notifications, the messenger).

| Method | What |
|---|---|
| `tasks.task.getFields` | `{"fields": {ID: {title, type, primary, required, values}}}` |
| `tasks.task.list` | `filter`, `order`, `select`, `start` → `{"tasks": [...]}`, 50 a page with `total` and `next` |
| `tasks.task.get` | `taskId`, `select` → `{"task": {...}}` |
| `tasks.task.add` | `fields` → `{"task": {...}}` |
| `tasks.task.update` | `taskId`, `fields` → `{"task": {...}}`; only the fields given change |
| `tasks.task.delete` | `taskId` → `{"task": true}`; the task goes to the trash |
| `tasks.task.start`, `pause`, `complete`, `defer`, `renew`, `approve`, `disapprove` | `taskId` → `{"task": {...}}` |
| `tasks.task.delegate` | `taskId`, `userId` → `{"task": {...}}` |
| `tasks.task.favorite.add`, `favorite.remove`, `mute`, `unmute` | `taskId` → `{"task": true}` |
| `task.commentitem.getlist` | `TASKID`, `ORDER` (`ID`, `POST_DATE`, `AUTHOR_ID`), `FILTER` (`AUTHOR_ID`, `>POST_DATE`, `<ID`…) |
| `task.commentitem.get`, `add`, `update`, `delete` | `TASKID`, `ITEMID`, `FIELDS {POST_MESSAGE}`; `add` → the comment's number; one's own comment is changed |
| `task.checklistitem.getlist`, `get` | `TASKID`, `ITEMID` → `[{ID, TASK_ID, PARENT_ID, CREATED_BY, TITLE, SORT_INDEX, IS_COMPLETE, IS_IMPORTANT, TOGGLED_BY, TOGGLED_DATE}]` |
| `task.checklistitem.add`, `update`, `complete`, `renew`, `delete` | `TASKID`, `ITEMID`, `FIELDS {TITLE, IS_COMPLETE}`; `add` → the item's number |
| `task.elapseditem.getlist` | `TASKID` (none — the entries of all the tasks the caller sees), `ORDER`, `FILTER` (`ID`, `USER_ID`, `TASK_ID`, `CREATED_DATE`, `SECONDS` with `>`, `<`, `>=`, `<=`, `!`) → `[{ID, TASK_ID, USER_ID, COMMENT_TEXT, SECONDS, MINUTES, SOURCE, CREATED_DATE, DATE_START, DATE_STOP}]`, 50 a page |
| `task.elapseditem.get`, `add`, `update`, `delete` | `TASKID`, `ITEMID`, `ARFIELDS {SECONDS or MINUTES, COMMENT_TEXT, CREATED_DATE}`; `add` → the entry's number; one logs one's own time (`USER_ID` — only the caller), the author or a project manager changes an entry |
| `task.stages.get` | `entityId` — a group's number, `0` — the caller's «My plan» → `{ID: {ID, TITLE, SORT, COLOR, SYSTEM_TYPE, ENTITY_ID, ENTITY_TYPE}}`; `SYSTEM_TYPE` `NEW`, `FINISH` for a group's first and last stage, `ENTITY_TYPE` `G` or `U` |
| `task.stages.add`, `update`, `delete` | `fields {TITLE, COLOR, ENTITY_ID}`, `id`; `add` → the stage's number, a group's stage comes before «Done»; a group's stages — its owner and moderators, a plan's — its owner alone |
| `task.stages.canmovetask`, `movetask` | `entityId`, `entityType`; `id` (the task), `stageId` — as dragging on the kanban: «Done» completes the task, out of it — renews |
| `task.dependence.add`, `delete` | `taskIdFrom` (the predecessor), `taskIdTo`, `linkType` (`0` start-start, `1` start-finish, `2` finish-start — the default, `3` finish-finish); a cycle is refused |
| `tasks.task.result.list` | `taskId` → `[{id, taskId, commentId, createdBy, createdAt, updatedAt, status, text, formattedText, files}]` |
| `tasks.task.result.addFromComment`, `deleteFromComment` | `commentId` → the result, `true` |
| `tasks.task.history.list` | `taskId`, `filter {FIELD}`, `order {ID}` → `{"list": [{id, createdDate, field, value {from, to}, user}]}`; fields (`NEW`, `STATUS` in numbers, `RESPONSIBLE_ID`, `DEADLINE`…) |
| `tasks.task.counters.get` | `userId` (another's — in the admin mode), `groupId`, `type` (`view_all`, `view_role_responsible`, `view_role_accomplice`, `view_role_auditor`, `view_role_originator`) → `{expired: {counter}, new_comments: {counter}}` |
| `task.planner.getlist` | → the numbers of the tasks of the caller's plan of the day |
| `tasks.task.startwatch`, `stopwatch` | `taskId` → `{"task": true}`; anyone who sees the task becomes an auditor |

Fields — in `select`, `filter`, `order` and `fields` in capitals (`RESPONSIBLE_ID`; `responsibleId` is taken too), in
the answer in camelCase (`responsibleId`) gives them: `ID`, `PARENT_ID`, `TITLE`, `DESCRIPTION`, `MARK`
(`P`, `N`), `PRIORITY` (`0`, `1`, `2`), `STATUS` and `REAL_STATUS` (`2` pending, `3` in progress, `4` awaiting review,
`5` completed, `6` deferred), `GROUP_ID`, `CREATED_BY`, `CREATED_DATE`, `RESPONSIBLE_ID`, `CHANGED_DATE`, `CLOSED_BY`,
`CLOSED_DATE`, `DEADLINE`, `START_DATE_PLAN`, `END_DATE_PLAN`, `ALLOW_CHANGE_DEADLINE`, `MATCH_WORK_TIME`,
`TASK_CONTROL`, `TIME_ESTIMATE` (seconds), `TIME_SPENT_IN_LOGS`, `COMMENTS_COUNT`, `ACCOMPLICES`, `AUDITORS`, `TAGS`.
With `CREATED_BY`, `RESPONSIBLE_ID` and `GROUP_ID` the answer has `creator`, `responsible` (`{id, name, icon,
workPosition}`) and `group` (`{id, name}`).

- Numbers: tasks, groups (go24 projects), comments and checklist items have numbers (migration 662), time entries and
  stages too (migration 996; the stages of groups and of plans in one row), given once and never reused; the trash restores a task with its number.
- `filter`: the prefixes of CRM (`=`, `!`, `>`, `<`, `>=`, `<=`, `%`, `@`, `!@`), and `MEMBER` (a participant in any
  role), `ACCOMPLICE`, `AUDITOR` (employees' numbers), `TAG`, `STATUS` `-1` — overdue.
- `select`: none or `*` — all the fields; `ID` always.
- `add`: with no `GROUP_ID` the task goes to the project the caller last put a task into (every go24 task is in a
  project), with no `RESPONSIBLE_ID` — to the caller. `update` does not move a task to another project or parent.
- The old methods (`task.commentitem.*`, `task.checklistitem.*`) take their parameters by name or by place: a JSON list
  `[12, {"POST_MESSAGE": "…"}]` or `0=12&1[POST_MESSAGE]=…`.

## Workgroups

`sonet_group.*`, `socialnetwork.api.workgroup.*` (scope `sonet_group`; reading — `projects:read`, changes —
`projects:write`) — GO24DEV-940. A group is a go24 group or project by its number (the same as `GROUP_ID` of tasks);
the rights are those of the group's pages.

| Method | What |
|---|---|
| `sonet_group.create` | `NAME`, `DESCRIPTION`, `VISIBLE`, `OPENED`, `CLOSED`, `KEYWORDS`, `INITIATE_PERMS`, `PROJECT`, `PROJECT_DATE_START`, `PROJECT_DATE_FINISH`, `SUBJECT_ID`, `IMAGE` (`[name, base64]`), `OWNER_ID` (an administrator's) → the group's number |
| `sonet_group.update` | `GROUP_ID` and the fields of `create` to change → the group's number |
| `sonet_group.delete` | `GROUP_ID` → `true`; its owner or an administrator, a group with tasks is not deleted (as on its page) |
| `sonet_group.get` | `FILTER` (`!`, `%`, `>`, `>=`, `<`, `<=`), `ORDER` (`ID` `DESC` by default), `IS_ADMIN` → `[{ID, NAME, DESCRIPTION, DATE_CREATE, DATE_UPDATE, DATE_ACTIVITY, ACTIVE, VISIBLE, OPENED, CLOSED, SUBJECT_ID, SUBJECT_NAME, OWNER_ID, KEYWORDS, NUMBER_OF_MEMBERS, NUMBER_OF_MODERATORS, INITIATE_PERMS, PROJECT, PROJECT_DATE_START, PROJECT_DATE_FINISH, IS_EXTRANET, IMAGE}]` |
| `sonet_group.setowner` | `GROUP_ID`, `USER_ID` → `true`; one not in the group joins it first |
| `sonet_group.user.get` | `ID` → `[{USER_ID, ROLE}]` |
| `sonet_group.user.add`, `user.delete` | `GROUP_ID`, `USER_ID` (one or a list) → the numbers of those added (at once, no invitation) or taken out (not the owner) |
| `sonet_group.user.invite` | `GROUP_ID`, `USER_ID`, `MESSAGE` → the numbers of those invited; the bell asks them |
| `sonet_group.user.request` | `GROUP_ID`, `MESSAGE` → `true`: the caller joins an open group at once, asks to join a closed one |
| `sonet_group.user.groups` | the caller's groups → `[{GROUP_ID, GROUP_NAME, ROLE, GROUP_IMAGE, IS_EXTRANET}]` |
| `sonet_group.feature.access` | `GROUP_ID`, `FEATURE` (`tasks`, `files`, `calendar`, `blog`, `chat`), `OPERATION` → `true` / `false` |
| `socialnetwork.api.workgroup.get` | `params {groupId}` → the group with `MEMBERS`, `ORDINARY_MEMBERS`, `MODERATOR_MEMBERS`, `INVITED_MEMBERS`, `CHAT_ID`, `DIALOG_ID`, `TYPE` |
| `socialnetwork.api.workgroup.list` | `filter`, `order`, `select` → `{"workgroups": [{id, name, type, avatar, additionalData {role}}…]}` |

- The group's type: open — `VISIBLE` and `OPENED` `Y`, closed — `VISIBLE` alone, secret — neither; a new group is closed
  unless they say otherwise. `CLOSED` `Y` is the archive, `PROJECT` `Y` a project (or a scrum).
- Roles: `A` the owner, `E` a moderator, `K` a member. `INITIATE_PERMS`: `A` the owner, `E` the owner and moderators,
  `K` all members.
- `SUBJECT_ID` is the number of a subject («Тематика», migration 880). A new group gets the key `SG<number>` (the portal
  groups have no keys).
- `feature.access`: `view…` — one who sees the group (anyone in an open one), `create_tasks` — as the group's rights to
  its tasks, `full…`, `moderate…`, `premoderate…`, `delete…`, `edit_tasks`, `modify_folders` — its owner and
  moderators, the rest — its members; a tool switched off in «Возможности группы», or one go24 has not (`photo`), — `false`.

## Feed

GO24DEV-948. `log.blogpost.*`, `log.blogcomment.*` (scope `log`; reading — `feed:read`, changes — `feed:write`), with
the rights of the feed's page. Posts and comments go by their numbers (migration 892).

| Method | What |
|---|---|
| `log.blogpost.get` | the posts the caller sees, the newest first, 50 a page from `start`; `POST_ID` — one post; `LOG_RIGHTS` — only those to these addressees → `[{ID, TITLE, DETAIL_TEXT, AUTHOR_ID, DATE_PUBLISH, MICRO, NUM_COMMENTS, VIEWS, CATEGORY_ID, HAS_SOCNET_ALL, HAS_TAGS, HAS_IMAGES, FILES, UF_BLOG_POST_IMPRTNT, UF_IMPRTANT_DATE_END, URL}]` |
| `log.blogpost.add` | `POST_TITLE`, `POST_MESSAGE`, `DEST` (all employees when none), `IMPORTANT` `Y` with `IMPORTANT_DATE_END`, `FILES`, `UF_BLOG_POST_FILE` → the post's number |
| `log.blogpost.update` | `POST_ID`, `POST_TITLE`, `POST_MESSAGE` (the author), `DEST` readdresses the post, `FILES` and `UF_BLOG_POST_FILE` add files → the post's number |
| `log.blogpost.delete` | `POST_ID` → `true`; its author or an administrator |
| `log.blogpost.share` | `POST_ID`, `DEST` (people and departments) → `true` |
| `log.blogpost.getusers.important` | `POST_ID` → the numbers of those who confirmed the important post as read |
| `log.blogcomment.add` | `POST_ID`, `TEXT`, `FILES`, `UF_BLOG_COMMENT_FILE` → the comment's number |
| `log.blogcomment.delete` | `COMMENT_ID` → `true`; its author or an administrator |

- `DEST` (or `SPERM` of older integrations): `UA` — all employees, `U<n>` — an employee, `SG<n>` — a group, `D<n>` and
  `DR<n>` — a department with its sub-departments. A go24 post is in one group at most: the people and departments given
  with it are shared the post.
- `FILES` are `[name, base64]` pairs (one pair or a list of them); `UF_BLOG_POST_FILE` — files of the drives, `n<number>`.
  At most 10 files a post.

## Working time

GO24DEV-948, GO24DEV-978. `timeman.*` (reading — `company:read`, changes — `company:write`), with the rights of «Рабочее время».

| Method | What |
|---|---|
| `timeman.status` | `USER_ID` (the caller when none; another person — to whom they report and to administrators) → `{STATUS, TIME_START, TIME_FINISH, DURATION, TIME_LEAKS, ACTIVE, IP_OPEN, IP_CLOSE, LAT_OPEN, LON_OPEN, LAT_CLOSE, LON_CLOSE, TZ_OFFSET}`; coordinates — with «Location» on (0 — not known) |
| `timeman.open` | `LAT`, `LON` — where (kept with «Location» on); starts the day; a paused one goes on, a finished one is continued («Продолжить рабочий день»), an open one stays → the status |
| `timeman.pause` | pauses the day → the status |
| `timeman.close` | `REPORT`, `LAT`, `LON` — finishes the day with the report → the status |
| `timeman.settings` | `USER_ID` → `{UF_TIMEMAN, UF_TM_FREE, UF_TM_MAX_START, UF_TM_MIN_FINISH, UF_TM_MIN_DURATION, UF_TM_REPORT_REQ, UF_TM_ALLOWED_DELTA, ADMIN}` by the person's work schedule or the company's working time |
| `timeman.schedule.get` | `id` → `{ID, NAME, SCHEDULE_TYPE (FIXED, SHIFT, FLEXTIME), SHIFTS [{WORK_TIME_START, WORK_TIME_END, BREAK_DURATION, WORK_DAYS}], VIOLATION_RULES}`, times in seconds from midnight |
| `timeman.networkrange.get` | the office networks `[{ip_range, name}]`, `ip_range` as `first-last` |
| `timeman.networkrange.check` | `IP` → `{ip, range_name}`; `range_name` is `false` outside the offices |
| `timeman.networkrange.set` | `RANGES` `[{ip_range, name}]` — `first-last`, one address or a subnet — replaces the office networks (who manages working time) → `true` |
| `timeman.timecontrol.settings.get` | «Контроль времени»: `{active, minimum_idle_for_report, register_idle, report_request_type (all, none), …}` |
| `timeman.timecontrol.settings.set` | `ACTIVE`, `MINIMUM_IDLE_FOR_REPORT`, `REPORT_REQUEST_TYPE` (`all`, `user` — asked, `none`); what is not given stays (who manages working time) → `true` |
| `timeman.timecontrol.report.add` | `REPORT_ID` — the caller's absence, `TEXT`, `TYPE` (`WORK` — by default, `PRIVATE`) → `true` |
| `timeman.timecontrol.reports.get` | `USER_ID`, `YEAR`, `MONTH` (this month) → `{report: {days: [{index, date, workday_date_start, workday_date_finish, workday_time, workday_complete, reports: [{id, type, text, time_start, time_finish, duration}]}]}, user, info}` — the days with a working day or absences; another person — to whom they report |
| `timeman.timecontrol.reports.settings.get` | `{user_id, user_admin, department_manager, department_id, departments [{id, name}], report_view_type (full, simple), minimum_idle_for_report, active}` |
| `timeman.timecontrol.reports.users.get` | `DEPARTMENT_ID` (with sub-departments; none — everyone) → the people whose reports the caller sees `[{id, name, position, avatar}]` |

- `STATUS`: `OPENED`, `PAUSED`, `CLOSED`, `EXPIRED` (an earlier day left open); `DURATION` — worked without pauses,
  `TIME_LEAKS` — the pauses, both `HH:MM:SS`.
- `timeman.open`, `pause`, `close` with another `USER_ID` — administrators only. `TIME` (another time than now) is not
  taken: the day changes now, as on the page; «Только из офиса» and the violations work as there.

## Applications: events and placements

GO24DEV-835. Only an application calls them, with its token (`/rest/<method>?auth=…`); an incoming webhook gets
`WRONG_AUTH_TYPE`. Binding and unbinding are for administrators.

| Method | What |
|---|---|
| `events` | the events a handler can be bound to: `ONCRM<LEAD|DEAL|CONTACT|COMPANY|DYNAMICITEM><ADD|UPDATE|DELETE>`, `ONTASKADD`, `ONTASKUPDATE`, `ONTASKDELETE`, `ONUSERADD`, `ONAPPUNINSTALL`, `ONVOXIMPLANTCALLEND`, the chat bots' ones |
| `event.get` | the application's handlers `[{event, handler, auth_type, offline}]` |
| `event.bind` | `event`, `handler` (on the application's host), `auth_type` (the employee whose tokens the handler gets; 0 — the caller) → `true` |
| `event.unbind` | `event`, `handler` (none — every handler of the event) → `{count}` |
| `placement.list`, `get` | the places; the application's handlers `[{placement, handler, title, description, options}]` |
| `placement.bind`, `unbind` | `PLACEMENT`, `HANDLER`, `TITLE`, `DESCRIPTION` (or `LANG_ALL[<lang>][TITLE]`), `OPTIONS` → `true`; `{count}` |

The widgets of the messenger (GO24DEV-977) chat widgets:

| Place | Where | `PLACEMENT_OPTIONS` |
|---|---|---|
| `IM_TEXTAREA` | a button by the input field; the frame opens over it in `width`×`height` (100–800 × 50–600 px) | `dialogId`, `roomId` |
| `IM_SIDEBAR` | a button of the chat's header; the frame opens in the side panel | `dialogId`, `roomId` |
| `IM_CONTEXT_MENU` | an item of a message's «…», after «Создать задачу / встречу / пост»; the side panel | `dialogId`, `messageId`, `roomId`, `eventId` |
| `IM_NAVIGATION` | a button of the messenger's header; the application instead of a chat (`/im?app=<id>`) | — |

`OPTIONS`: `iconName` (Font Awesome: `fa-cloud` is go24's icon `cloud` when it has one, else the application's icon),
`context` — `USER` (private chats with people, «Заметки»), `CHAT` (group chats, channels, chats of tasks and
collabs), `CRM` (the chat of a CRM card), `LINES`, `ALL` (default), several with `;`; `role` — `USER` (default) or
`ADMIN` (administrators only); `color` — the names (`RED`, `AQUA`, `GRAPHITE`…); `width`, `height`; `extranet`
— `Y` lets guests of collabs see it (default `N`). `dialogId` is `chatN` or the number of the colleague (bot) of a
private chat, as `DIALOG_ID` of `im.*`; the frame opens only in a chat the employee is in. The frame posts the
employee's tokens as a card's tab, go24.js works in it (`closeApplication` closes the panel).

A handler bound by `event.bind` of `/rest/` gets the event posts it: a form of PHP arrays
(`event=ONCRMDEALADD&data[FIELDS][ID]=5&ts=…&auth[access_token]=…`), the record by its number (a deal's, a task's, an
employee's), `auth[client_endpoint]` — `https://<go24>/rest/`, `auth[user_id]` — the employee's number. A handler bound
over go24 REST (`POST /api/v1/events`) keeps JSON with the go24 ids.

## Applications: settings and data storage

GO24DEV-979. The data of an application: of its token, or — for an incoming webhook — of the webhook's own application,
so a webhook's data stay apart.

| Method | What |
|---|---|
| `app.option.get` | `option` → its value (`null` — none); without — all `{name: value}` |
| `app.option.set` | `options` — `{name: value}`, `null` removes; by administrators → `true` |
| `user.option.get`, `set` | the same for the caller's own settings |
| `user.access` | `ACCESS` — access codes → `true` when the caller holds one of them |
| `access.name` | `ACCESS` → `{code: {provider, provider_id, name}}`; unknown codes are left out |
| `entity.add` | `ENTITY` (1–13 Latin letters, digits, `_`), `NAME`, `ACCESS` — by administrators → `true` |
| `entity.get` | `ENTITY` → `{ID, ENTITY, NAME}`; without — the entities the caller may read |
| `entity.update` | `ENTITY`, `NAME`, `ENTITY_NEW`, `ACCESS` |
| `entity.delete` | `ENTITY` — with its sections, items and files |
| `entity.rights` | `ENTITY` → `{code: R|W|X}`; with `ACCESS` — sets them first |
| `entity.item.property.add`, `update`, `delete` | `ENTITY`, `PROPERTY`, `NAME`, `TYPE` (`S` a string, `N` a number, `F` a file), `PROPERTY_NEW`; a new code moves the values, a new type drops what it cannot hold |
| `entity.item.property.get` | `ENTITY`, `PROPERTY` → `{PROPERTY, NAME, TYPE}`; without — all of them |
| `entity.section.add`, `update`, `delete` | `ENTITY`, `ID`, `NAME`, `CODE`, `DESCRIPTION`, `SORT`, `ACTIVE`, `SECTION` (the one above) → the number; a section is deleted with the sections and items in it |
| `entity.section.get` | `ENTITY`, `FILTER`, `SORT`, `start` → `[{ID, CODE, NAME, DESCRIPTION, SECTION, SORT, ACTIVE, DEPTH_LEVEL, DATE_CREATE, CREATED_BY, TIMESTAMP_X, MODIFIED_BY}]` |
| `entity.item.add`, `update`, `delete` | `ENTITY`, `ID`, `NAME`, `CODE`, `ACTIVE`, `SORT`, `SECTION`, `DATE_ACTIVE_FROM`, `DATE_ACTIVE_TO`, `PREVIEW_TEXT`, `DETAIL_TEXT`, `PREVIEW_PICTURE`, `DETAIL_PICTURE`, `PROPERTY_VALUES` → the number; `update` changes the properties given, the others stay |
| `entity.item.get` | `ENTITY`, `FILTER`, `SORT`, `start` → the items with `PROPERTY_VALUES`, 50 a page |

Access codes: `U<number>` — an employee, `D<number>` — the members and the head of a department, `DR<number>` — the
same with the subdepartments, `G1` — administrators, `G2`, `AU`, `UA` — everyone. The rights: `R` — read, `W` — also
sections and items, `X` — also properties, rights and the entity; administrators may everything; by default `{"AU": "R"}`.

`FILTER` takes the fields and `PROPERTY_<code>` with the operators `=` (a list — any of them), `!`, `!=`, `>`, `<`,
`>=`, `<=`, `%` (contains), `!%`; an empty value or `false` — not filled. Numbers of `N` properties compare as numbers.
`SORT` — `{field: ASC|DESC}`. A file — `[name, base64]` or `{fileData: [name, base64]}` (up to 10 MB); `""` or
`{remove: "Y"}` removes it; `entity.item.get` gives a file as a signed address that works for a day for whoever may
read the entity.

## Employees and departments

| Method | What |
|---|---|
| `user.fields` | the fields of an employee and their titles |
| `user.search` | `user.get` with the conditions in `FILTER` or at the top; also `FIND` (`NAME_SEARCH`) — words of the name, e-mail or position, `WORK_POSITION`, `PERSONAL_CITY`, `UF_PHONE_INNER`, `UF_DEPARTMENT_NAME` |
| `user.add` | (`company:write`, who may invite) `EMAIL`, `NAME`, `LAST_NAME`, card fields, `UF_DEPARTMENT` → the employee's number; an invitation goes by e-mail as «Пригласить сотрудника» sends it; `EXTRANET=Y` is refused (guests are invited from a collab) |
| `user.update` | `ID` and the fields to change: the card and its `UF_USR_*` — the employee themself or an administrator; `UF_DEPARTMENT`, `ACTIVE` (`N` dismisses, `Y` returns, in «Журнал событий») and the extension with telephony on — administrators |
| `user.userfield.list`, `get`, `add`, `update`, `delete` | the own fields of employees' profiles, as `crm.<entity>.userfield.*` (`ENTITY_ID` `USER`, the codes `UF_USR_<code>` or `UF_USR_<number>`, migration 985); reading — `company:read`, changes — `company:write`, an administrator (GO24DEV-985) |
| `department.get` | (scope `department`, `company:read`) `[{ID, NAME, SORT, PARENT, UF_HEAD}]` — `ID`, `NAME`, `SORT`, `PARENT`, `UF_HEAD` as parameters or in `filter`; `sort` and `order`; 50 a page |
| `department.fields` | the fields of a department and their titles |
| `department.add` | `NAME`, `PARENT` (under the company by default), `UF_HEAD`, `SORT` → the department's number |
| `department.update` | `ID` and the fields to change; a new `PARENT` moves the department |
| `department.delete` | `ID` |

Departments are changed with the rights of «Структура компании → Права доступа». An employee added by `user.add` is in
go24 before they sign up — in lists, pickers and tasks; the first sign-in with that e-mail takes the record over (as it
takes over the employees imported) and greets them in the feed.

## Messenger

GO24DEV-839, 909, 915, 922, 937. Scope `im` (`im:write` — notifications and messages, `im:read` — chats and messages).

| Method | What |
|---|---|
| `im.notify.system.add` | `USER_ID` (the employee's number), `MESSAGE`, `TAG` → the notification's number: in the bell without an author, as a system notification |
| `im.notify.personal.add`, `im.notify` | the same, from the caller |
| `im.notify.delete` | `ID` or `TAG` (`SUB_TAG`) — the caller's own notifications → `true` |
| `im.message.add` | `DIALOG_ID`, `MESSAGE` → the message's number; written as the caller |
| `im.message.update` | `MESSAGE_ID`, `MESSAGE` (empty — takes it back) → `true`; the caller's own message |
| `im.message.delete` | `MESSAGE_ID` → `true` |
| `im.dialog.get`, `im.chat.get` | `DIALOG_ID` (or `CHAT_ID`) → `{id, dialog_id, name, owner, type, entity_type, entity_id, unread_count, …}` |
| `im.chat.get` | `ENTITY_TYPE=CRM`, `ENTITY_ID=DEAL\|12` (`LEAD`, `CONTACT`, `COMPANY`) → `{ID}` of the card's «Chat», `null` — none yet |
| `im.chat.get` | another `ENTITY_TYPE` and `ENTITY_ID` — those `im.chat.add` bound the chat to → `{ID}` of the newest such chat the caller is in, `null` — none |
| `im.chat.user.list` | `CHAT_ID` → the numbers of the employees in the chat |
| `im.dialog.messages.get` | `DIALOG_ID`, `LIMIT` (up to 50), `LAST_ID` (the ones before it) or `FIRST_ID` (the ones after it) → `{chat_id, messages: [{id, chat_id, author_id, date, text, params}], users: [{id, name, first_name, last_name, avatar, …}]}` |
| `im.chat.add` | `TYPE` (`CHAT`, `OPEN` — an open chat, `CHANNEL`, `OPEN_CHANNEL`), `TITLE`, `DESCRIPTION`, `USERS` (the employees' numbers), `AVATAR` (an image in base64), `MESSAGE` (the first message), `OWNER_ID` (hands the chat over), `COLOR`, `ENTITY_TYPE` and `ENTITY_ID` (an object of the application the chat is bound to) → the chat's number |
| `im.chat.user.add` | `CHAT_ID`, `USERS` → `true`, as the chat's rights let the caller |
| `im.chat.user.delete` | `CHAT_ID`, `USER_ID` → `true`: the owner or an admin; oneself — leaving |
| `im.chat.leave` | `CHAT_ID` → `true` |
| `im.chat.updateTitle`, `im.chat.updateAvatar` | `CHAT_ID`, `TITLE` / `AVATAR` (base64) → `true` |
| `im.chat.setOwner` | `CHAT_ID`, `USER_ID` (someone in the chat) → `true`; the owner only |
| `im.recent.get` | `SKIP_CHAT`, `SKIP_DIALOG`, `LAST_UPDATE` → the caller's chats, newest first: `[{id, chat_id, type (chat, user), title, counter, pinned, unread, message: {id, text, author_id, date}, date_update}]`; hidden chats are left out |
| `im.recent.list` | the same by pages: `OFFSET`, `LIMIT` (up to 50) → `{items, hasMore}` |
| `im.dialog.read` | `DIALOG_ID`, `MESSAGE_ID` (none — the whole chat) → `{dialogId, chatId, lastId, counter}` |
| `im.notify.read` | `ID`, `ONLY_CURRENT` (`Y` — it alone, otherwise with the older ones) → `true` |
| `im.notify.read.all` | → `true` |
| `im.notify.confirm` | `ID`, `NOTIFY_VALUE` — `Y` / `N`, or `tentative` for a meeting → `true`: answers an invitation to a meeting or a project, a request to join a group, as the bell's buttons (GO24DEV-896); the key needs `calendar:write` or `projects:write` too |
| `im.message.like` | `MESSAGE_ID`, `ACTION` — `plus`, `minus` or `auto` (the other way than it is) → `true`; the like is the 👍 reaction (GO24DEV-915) |
| `im.dialog.writing` | `DIALOG_ID` → `true`: the others see «typing…» of the caller |
| `im.chat.mute` | `CHAT_ID`, `ACTION` — `Y` (the default) / `N` → `true`: the chat without notifications for the caller |
| `im.recent.pin`, `im.recent.unread` | `DIALOG_ID`, `ACTION` `Y` / `N` → `true`: pinned at the top of the caller's chats, marked unread |
| `im.recent.hide` | `DIALOG_ID` → `true`: hidden from the caller's chats until a newer message |
| `im.counters.get` | → `{CHAT: {chat number: n}, DIALOG: {employee number: n}, LINES: {}, TYPE: {ALL, NOTIFY, CHAT, DIALOG, LINES}}` — the caller's unread; muted and hidden chats do not count |
| `im.user.get` | `ID` (the caller when none) → the employee in the messenger's fields: `{id, active, name, first_name, last_name, work_position, avatar, gender, birthday (dd-mm), extranet, bot, status (online, away, dnd), idle, last_activity_date, departments, absent (the last day of an absence going on), phones}` |
| `im.user.list.get` | `ID` — numbers → `{number: the employee as im.user.get, or null}` |
| `im.user.status.get`, `im.user.status.set` | the caller's status: `online`, `away`, `dnd` («Do not disturb»), as in their card; set — `STATUS` → `true` |
| `im.search.user.list` | `FIND` (3 characters at least), `OFFSET` → active employees by name, e-mail or position: `{number: the employee as im.user.get}`, 50 at most |
| `im.search.chat.list` | `FIND` → the caller's group chats and channels by name: `{number: {id, name, type (chat, channel), entity_type}}` |
| `im.notify.get` | `LIMIT` (up to 50), `LAST_ID` (older than it) → `{notifications: [{id, author_id, date, notify_type (1 — confirming, its `notify_buttons`; 2 — from a person; 4 — system), notify_event, notify_read (Y/N), text, params.URL}], users (the authors as im.user.get), total_count, total_unread_count}` (GO24DEV-922) |
| `im.notify.history.search` | `SEARCH_TEXT`, `SEARCH_TYPE` (a module — `tasks`, `calendar`, `crm`, `im`, `socialnetwork`…, or a section of go24's notification settings), `SEARCH_DATE` (a day), `LIMIT`, `LAST_ID` → as `im.notify.get` |
| `im.dialog.users.list` | `DIALOG_ID`, `SKIP_EXTERNAL` (`Y` — without guests) → the people of the chat as im.user.get |
| `im.dialog.unread` | `DIALOG_ID` → `true`: the chat marked unread for the caller (as a whole; `MESSAGE_ID` is not needed) |
| `im.department.get` | `ID` — numbers, `USER_DATA` (`Y` — the head as im.user.get in `manager_user_data`) → `[{id, name, full_name (the department, those above it, the company last), manager_user_id}]` |
| `im.department.employees.get`, `im.department.managers.get` | `ID`, `USER_DATA` → `{department number: [its active employees, the head first] / [its head]}` — numbers, or as im.user.get with `USER_DATA=Y` |
| `im.department.colleagues.list` | `USER_DATA`, `LIMIT` (up to 50), `OFFSET` → the caller's colleagues: the people of their departments and those departments' heads, for a head also the heads of the departments under theirs; with `total` and `next` |
| `im.search.department.list` | `FIND` (3 characters at least) → departments by name, as `im.department.get` |
| `im.revision.get` | → `{rest, web, mobile}` — the revision of go24's `im.*` methods |
| `im.disk.folder.get` | `CHAT_ID` or `DIALOG_ID` → `{ID}`: the chat's folder on the drive — «Saved files» of the project's drive for a chat of a project (a collab's, a task's) when the caller may write there, of the caller's «My drive» otherwise, where «Attach → Document» keeps a chat's documents; `disk.folder.uploadfile` uploads there (GO24DEV-937) |
| `im.disk.file.commit` | `CHAT_ID` or `DIALOG_ID`, `FILE_ID` (`UPLOAD_ID`, `DISK_ID`) — drive files the caller sees, one or a list —, `MESSAGE` (sent before them) → `{FILES: {uploadN: {id, chatId, date, type (image, file), name, extension, size, authorId, authorName, urlShow, urlDownload, …}}, DISK_ID, FILE_MODELS (the files as `disk.file.get`), MESSAGE_ID — the first file's message}`; each file goes into the chat as its own message |
| `im.disk.file.save` | `FILE_ID` → a copy in «Saved files» of the caller's «My drive»: `{folder: {id, name}, file: {id, name}}` |
| `im.disk.file.delete` | `CHAT_ID`, `FILE_ID` → `true`: the messages `im.disk.file.commit` sent the file with leave the chat (one's own; anyone's for the chat's owner and admins); the file stays on its drive |
| `im.chat.updateColor` | `CHAT_ID` (`DIALOG_ID`), `COLOR` — `RED`, `GREEN`, `MINT`, `LIGHT_BLUE`, `DARK_BLUE`, `PURPLE`, `AQUA`, `PINK`, `LIME`, `BROWN`, `AZURE`, `KHAKI`, `SAND`, `MARENGO`, `GRAY`, `GRAPHITE` → `true`, for those who may change the chat; `im.dialog.get` gives it back as `color` (`#f76187`); go24's pages do not show it |
| `im.message.share` | `MESSAGE_ID`, `TYPE` — `TASK`, `POST` (the feed), `CALEND` (a meeting at the next whole hour with the chat's people), `CHAT` (a new chat of the chat's people) → `true`: made of the message as its menu makes them — its first line the title, the text with a link to it, the chat's project; the message is answered with the link in its chat |
| `im.notify.answer` | `NOTIFY_ID`, `ANSWER_TEXT` → `{result_message: [...]}`: the answer becomes a comment of the notification's task, feed post or meeting; `NOTIFY_ANSWER_ERROR` — nothing to answer there |
| `im.notify.read.list` | `IDS` — the caller's notifications, `ACTION` — `Y` read (default), `N` unread → `true` |
| `im.notify.schema.get` | the kinds of notifications by module — `{module: {NAME, MODULE_ID, NOTIFY: {kind: {NAME, SITE, MAIL, PUSH, DISABLED}}}}`; `SITE` and `PUSH` — whether the caller gets the kind (their settings), `MAIL` is always off |
| `im.smile.get` | the messenger's smileys — `SETS` and `SMILE`; they are Unicode: `TYPING` is the character itself, no `IMAGE` |

- `DIALOG_ID` is `chatN` — a chat by its number — or an employee's number — the private chat with them (started when
  there is none). A Matrix chat and message get their number when an integration first meets them (migration 665).
- `MESSAGE` takes the BB codes (`[b]`, `[i]`, `[u]`, `[s]`, `[url]`, `[code]`, `[quote]`) and `[BR]`; a
  notification is plain text, the codes are dropped. A notification with the same `TAG` replaces the caller's earlier
  one; a kind the employee turned off («Integrations» in the notification settings) is not written — the answer is
  `false`.
- `im.dialog.messages.get` gives the messages newest first, with `FIRST_ID` — oldest first; the lines about the chat
  itself (added, renamed) are left out.

### Chat bots

GO24DEV-925, 937. Scope `imbot`; only an application calls them (an incoming webhook gets `WRONG_AUTH_TYPE`), as the go24
REST of bots does (`/api/v1/imbot/…`). `BOT_ID` is the bot's number — from the employees' row of numbers, as a bot of
the portal is a user: it is `DIALOG_ID` of the private chat with it; without `BOT_ID` — the application's only bot.

| Method | What |
|---|---|
| `imbot.register` | `CODE`, `TYPE` (`B`, `H`, `S` — it gets every message of its group chats; `O` — as `B`), `EVENT_HANDLER` (all three at once) or `EVENT_MESSAGE_ADD`, `EVENT_WELCOME_MESSAGE`, `EVENT_BOT_DELETE`, `PROPERTIES` (`NAME`, `LAST_NAME`, `WORK_POSITION`, `COLOR`) → `BOT_ID`; the same `CODE` again is an error |
| `imbot.update` | `BOT_ID`, `FIELDS` — the same fields, what is given changes → `true` |
| `imbot.unregister` | `BOT_ID` → `true`: the bot leaves the messenger, its handler hears `ONIMBOTDELETE`, its messages stay |
| `imbot.bot.list` | → `{number: {ID, NAME, CODE, OPENLINE, TYPE}}` — the application's bots |
| `imbot.message.add` | `BOT_ID`, `DIALOG_ID` (`chatN` — a chat the bot is in, an employee's number — its private chat with them), `MESSAGE` (BB codes, `[BR]`), `KEYBOARD` (buttons, or `{BUTTONS: […]}`), `MENU`, `ATTACH` → the message's number |
| `imbot.message.update` | `BOT_ID`, `MESSAGE_ID`, `MESSAGE`, `KEYBOARD` (`N` — takes the buttons away), `MENU`, `ATTACH` → `true`; the bot's own message |
| `imbot.message.delete` | `BOT_ID`, `MESSAGE_ID` → `true` |
| `imbot.command.register` | `BOT_ID`, `COMMAND`, `COMMON`, `HIDDEN`, `EXTRANET_SUPPORT` (`Y` / `N`), `EVENT_COMMAND_ADD`, `LANG` (`[{LANGUAGE_ID, TITLE, PARAMS}]`) → `COMMAND_ID` |
| `imbot.command.update` | `COMMAND_ID`, `FIELDS` — the same fields → `true` |
| `imbot.command.unregister` | `COMMAND_ID` → `true` |
| `imbot.command.answer` | `COMMAND_ID` (or `COMMAND` and `BOT_ID`), `MESSAGE_ID` — the command's message from its event —, `MESSAGE`, `KEYBOARD`, `MENU`, `ATTACH` → the answer's number, in the command's chat and thread |
| `imbot.chat.sendTyping` | `BOT_ID`, `DIALOG_ID` → `true`: the others see «typing…» of the bot |
| `imbot.chat.leave` | `BOT_ID`, `CHAT_ID` → `true` |
| `imbot.chat.add` | `BOT_ID` and the fields of `im.chat.add` → the chat's number: the bot makes the chat and owns it (GO24DEV-937) |
| `imbot.chat.get` | `BOT_ID`, `ENTITY_TYPE`, `ENTITY_ID` → `{ID}` of the bot's chat bound to them, `null` — none |
| `imbot.chat.updateTitle`, `imbot.chat.updateAvatar`, `imbot.chat.updateColor`, `imbot.chat.setOwner` | `BOT_ID` and the fields of the `im.chat.*` method → `true`, as the chat's rights let the bot |
| `imbot.chat.setManager` | `BOT_ID`, `CHAT_ID`, `USER_ID`, `IS_MANAGER` (`Y` — the chat's admin, `N` — no longer) → `true`; the chat's owner or an admin |
| `imbot.chat.user.add`, `imbot.chat.user.delete`, `imbot.chat.user.list` | `BOT_ID`, `CHAT_ID`, `USERS` / `USER_ID` → as `im.chat.user.*`; the list has bots too |
| `imbot.dialog.get` | `BOT_ID`, `DIALOG_ID` (`chatN`, or an employee's number — the bot's private chat with them) → as `im.dialog.get` |
| `imbot.message.like` | `BOT_ID`, `MESSAGE_ID`, `ACTION` (`plus`, `minus`, `auto`) → `true`: the bot's 👍 |

- The handlers bound here hear `ONIMBOTMESSAGEADD`, `ONIMBOTJOINCHAT`, `ONIMBOTDELETE` and `ONIMCOMMANDADD` as
  the portal posts them — a form of PHP arrays, `data[PARAMS][DIALOG_ID]`, `data[BOT][<BOT_ID>]`,
  `data[COMMAND][<COMMAND_ID>]`, `data[USER]` — with numbers: `DIALOG_ID` is `chatN` or the employee's number,
  `CHAT_ID`, `MESSAGE_ID`, `FROM_USER_ID`, `TO_USER_ID` are numbers. The handlers bound by the go24 REST get go24's ids.

### Chat bots 2.0

GO24DEV-954, 961. `imbot.v2.*`, scope `imbot`; an application calls them (OAuth) — or an incoming webhook with
`botToken` (below) —, parameters and answers in camelCase with
numbers: `botId`, `dialogId` (`chatN`, or a person's number — the bot's private chat with them), `messageId`,
`commandId`, the fields in `fields`. The bot is the same as that of `imbot.*` (a Matrix account), but its events are
those of imbot.v2: `eventMode` `fetch` — the application takes them by `imbot.v2.Event.get` (kept 24 hours), `webhook` —
posted to `webhookUrl` (on the application's own host), `data.bot` is `{id, code, auth}`.

| Method | What |
|---|---|
| `imbot.v2.Bot.register` | `fields`: `code`, `type` (`bot`, `supervisor` — every message of its group chats, `personal`, `openline`), `eventMode`, `webhookUrl`, `botToken`, `isHidden` (in no people picker), `isReactionsEnabled`, `properties` (`name`, `lastName`, `workPosition`, `color`) → `{bot, users}`; the same `code` again — the same bot |
| `imbot.v2.Bot.update`, `get` (`botId` or `code`), `list` (`filter.type`, `limit`, `offset` → `hasNextPage`), `unregister` | |
| `imbot.v2.Event.get` | `botId`, `offset`, `limit` (100, up to 1000) → `{events: [{eventId, type, date, data}], nextOffset, hasMore}`; `offset` confirms the events below it |
| `imbot.v2.Chat.Message.send`, `update`, `delete`, `get` | `fields`: `message`, `keyboard`, `menu`, `attach` → `{id, uuidMap, failureMap}`, `{result}`, `{message, user}` |
| `imbot.v2.Chat.InputAction.notify` | `dialogId` → the others see «typing…» of the bot |
| `imbot.v2.Chat.add` | `fields`: `type` (`chat`, `open`), `title`, `description`, `color`, `avatar` (base64), `userIds`, `ownerId`, `message`, `entityType`, `entityId` → `{chat, users}`; the bot is in it |
| `imbot.v2.Chat.get`, `update` | `dialogId` — a chat the bot is in → `{chat, users}`; `update` — `chatN`, `fields` `title`, `description`, `color`, `avatar`, as the chat's rights let the bot |
| `imbot.v2.Chat.User.list` | `dialogId`, `order.userId`, `limit` (50, up to 200) → the chat's people (`User`) |
| `imbot.v2.Chat.Message.read` | `dialogId`, `messageId` (or all) → `{chatId, lastId, counter, viewedMessages}` |
| `imbot.v2.Chat.Message.getContext` | `messageId`, `range` (1–50) → `{messages, users, hasPrevPage, hasNextPage}`, oldest first |
| `imbot.v2.Chat.Message.Reaction.add`, `delete` | `messageId`, `reaction` — a code (`like`, `fire`, `redHeart`…) → `{result}` |
| `imbot.v2.Command.register`, `update`, `unregister`, `list`, `answer` | `fields`: `command`, `title` and `params` by language, `common`, `hidden`, `extranetSupport` |
| `imbot.v2.Chat.TextField.enabled` | `dialogId`, `enabled` (`true` by default) → `{result}`; switched off, the chat's page shows a note in place of the input field, the bot's buttons still answer; the chat's `textFieldEnabled` is `N` |
| `imbot.v2.File.upload` | `dialogId`, `fields`: `name`, `content` (base64), `message` (the text sent before it) → `{file: {id, chatId, name, extension, size}, messageId, chatId, dialogId}` |
| `imbot.v2.File.download` | `fileId` — a file of a chat the bot is in → `{downloadUrl}`: a signed address of go24, an hour |
| `imbot.v2.Revision.get` | as `im.revision.get` |

- Events: `ONIMBOTV2MESSAGEADD`, `ONIMBOTV2MESSAGEUPDATE`, `ONIMBOTV2MESSAGEDELETE` (private chats; a supervisor — every
  chat; an edit — also when it mentions the bot), `ONIMBOTV2JOINCHAT`, `ONIMBOTV2COMMANDADD`, `ONIMBOTV2REACTIONCHANGE`
  (a reaction on the bot's message put or taken back, when `isReactionsEnabled`), `ONIMBOTV2DELETE`.
- `ONIMBOTV2CONTEXTGET`: a link of go24 `/online/?IM_DIALOG={bot's number}&BOT_CONTEXT={JSON}` (or `/im?…` with the
  same) opens the employee's chat with the bot and gives the bot `{dialogId, context, chat, user}`; the address keeps no
  context after it. `im.v2.Chat.Bot.sendContext` (`dialogId` — the bot's number, `context`) does the same over REST.
  `IM_DIALOG` alone — `chatN` or a colleague's number — just opens that chat.
- A file in a chat is a message of its own: `fileId` is the number of that message, `message.params.FILE_ID` of the
  events gives it. The REST call is at most 8 MB, so is a file sent in base64.
- An incoming webhook (a personal API key with the scope `imbot`) calls `imbot.v2.*` with `botToken`: `Bot.register`
  takes it in `fields.botToken` (or by itself), the other methods by itself — without it `BOT_TOKEN_NOT_SPECIFIED`,
  another — `BOT_OWNERSHIP_ERROR`. The bots of a webhook belong to an application of its own, made on its first
  `Bot.register` (named as the key, «Приложения»); `imbot.*` stays an application's.

### im.v2

GO24DEV-961, scope `im`; the caller acts as themselves.

| Method | What |
|---|---|
| `im.v2.Event.subscribe`, `unsubscribe` | → `true`: go24 starts (stops, the kept ones gone) keeping the events of the caller's chats |
| `im.v2.Event.get` | `offset`, `limit` (100, up to 1000) → `{events: [{eventId, type, date, data}], nextOffset, hasMore}`; kept 24 hours |
| `im.v2.File.upload` | `dialogId`, `fields` (`name`, `content`, `message`) → as `imbot.v2.File.upload`, the file sent by the caller |
| `im.v2.File.download` | `dialogId`, `fileId` → `{downloadUrl}` |
| `im.v2.Chat.Bot.sendContext` | `dialogId` — a bot's number, `context` → `{result}`; the bot gets `ONIMBOTV2CONTEXTGET` |

- Events: `ONIMV2MESSAGEADD`, `ONIMV2MESSAGEUPDATE` (`message`, `chat`, `user` — the author), `ONIMV2MESSAGEDELETE`
  (`messageId`), `ONIMV2REACTIONCHANGE` (`reaction`, `action` `add` or `delete`, `message`, `user` — whose reaction),
  `ONIMV2JOINCHAT` (`dialogId`, `user` — who joined); each with `chat` and `language`. go24 worker reads the chats of a
  subscribed employee as them, from the subscription on.

## Workflows

`bizproc.workflow.template.*`, `bizproc.workflow.*`, `bizproc.task.*` (scope `bp` → `bizproc`; lists — `bp:read`, the
rest — `bp:write`) — GO24DEV-855. The rights are those of go24 REST of workflows: the caller's processes and tasks, an
administrator's key administers.

| Method | What |
|---|---|
| `bizproc.workflow.template.list` | the templates the caller may start (an administrator — all, the inactive ones too): `ID`, `XML_ID` (go24 uuid), `MODULE_ID`, `ENTITY`, `DOCUMENT_TYPE`, `AUTO_EXECUTE` (0, 1 — on adding, 2 — on changing, 3), `NAME`, `DESCRIPTION`, `PARAMETERS`, `VARIABLES`, `CONSTANTS` (`{key: {Name, Type, Required, Multiple, Default, Options}}`), `MODIFIED`, `ACTIVE` |
| `bizproc.workflow.template.delete` | `ID` → `true`; the template goes with its processes (administrators) |
| `bizproc.workflow.start` | `TEMPLATE_ID`, `DOCUMENT_ID`, `PARAMETERS` → the process id |
| `bizproc.workflow.terminate` | `ID`, `STATUS` (the reason) → `true`, as «Stop» |
| `bizproc.workflow.kill` | `ID` → `true`; the process goes with its history (administrators) |
| `bizproc.workflow.instances`, `bizproc.workflow.instance.list` | the running processes (an administrator — everybody's, else the caller's): `ID`, `MODIFIED`, `OWNED_UNTIL`, `MODULE_ID`, `ENTITY`, `DOCUMENT_ID`, `STARTED`, `STARTED_BY`, `TEMPLATE_ID`, `WORKFLOW_STATE` (the status «Set status» set) |
| `bizproc.task.list` | the tasks waiting for the caller (an administrator — for `filter[USER_ID]` too): `ID`, `XML_ID`, `WORKFLOW_ID`, `DOCUMENT_NAME`, `NAME`, `DESCRIPTION`, `MODIFIED`, `WORKFLOW_STARTED`, `WORKFLOW_STARTED_BY`, `WORKFLOW_TEMPLATE_ID`, `WORKFLOW_TEMPLATE_NAME`, `WORKFLOW_STATE`, `STATUS`, `USER_ID`, `USER_STATUS`, `MODULE_ID`, `ENTITY`, `DOCUMENT_ID`, `ACTIVITY`, `ACTIVITY_NAME`, `PARAMETERS` (`Fields` of a request), `DOCUMENT_URL` |
| `bizproc.task.complete` | `TASK_ID`, `STATUS`, `COMMENT`, `FIELDS` → `true` |
| `bizproc.task.delegate` | `TASK_IDS`, `FROM_USER_ID`, `TO_USER_ID` → `true`, as the template's «Delegation» allows |
| `bizproc.activity.add` | `CODE`, `HANDLER` (on the application's host), `AUTH_USER_ID`, `USE_SUBSCRIPTION`, `NAME`, `DESCRIPTION`, `PROPERTIES`, `RETURN_PROPERTIES` → `true` (an application's administrator token) |
| `bizproc.activity.update` | `CODE`, `FIELDS` (those of `add`; the rest stay) → `true` |
| `bizproc.activity.delete` | `CODE` → `true`; the steps of the action fail from then on |
| `bizproc.activity.list` | the codes of the application's actions |
| `bizproc.event.send` | `EVENT_TOKEN`, `RETURN_VALUES`, `LOG_MESSAGE` → `true`; the process waiting goes on |
| `bizproc.activity.log` | `EVENT_TOKEN`, `LOG_MESSAGE` → `true`, a line of the process's history |

- Numbers: templates and tasks have numbers (migration 720), given once and never reused; a go24 uuid is taken too. A
  process is its go24 uuid — `WORKFLOW_ID` is a string as well.
- `DOCUMENT_ID`: `["crm", "CCrmDocumentDeal", "DEAL_12"]` (`LEAD_`, `CONTACT_`, `COMPANY_` — of
  `CCrmDocumentLead`, `CCrmDocumentContact`, `CCrmDocumentCompany`; a smart process —
  `["crm", "Legacy\\Crm\\Integration\\BizProc\\Document\\Dynamic", "DYNAMIC_<type>_<number>"]`), an element of a list —
  `["lists", "BizprocDocument", "<go24 id>"]`, a file of the company drive — `["disk", "Legacy\\Disk\\BizProcDocument",
  "<go24 id>"]`; a standalone process — none. A document of another kind than the template's — `ERROR_WRONG_DOCUMENT_ID`.
- `PARAMETERS`: by the keys of the template's fields; an employee — `user_<number>`, a number or a go24 uuid; `Y`/`N` for
  «Yes/No»; a list is joined by commas.
- `ACTIVITY`: `ApproveActivity`, `ReviewActivity`, `RequestInformationActivity`, `RequestInformationOptionalActivity` (a
  request that may be rejected). `STATUS` of `complete`: an approval — `1`/`yes` or `2`/`no`, a review — `3`/`ok`, a
  request — `3`/`ok` with `FIELDS` or `4`/`cancel` (`2`/`no`); another — `ERROR_WRONG_STATUS`.
- `filter` of the lists — equality (a list — any of, `!` — not equal), `order` — one field, `select`, `start`.
- Actions of applications (GO24DEV-903) — for an application's token only. `PROPERTIES` and `RETURN_PROPERTIES`:
  `{key: {Name, Description, Type, Required, Multiple, Default, Options}}`, `Type` — `string`, `text`, `int`, `double`,
  `bool`, `date`, `datetime`, `select`, `user`; `NAME` and `Name` — a string or `{"ru": …, "en": …}`. The designer offers
  the action under «Application actions». A step posts a form of PHP arrays to `HANDLER`: `workflow_id`, `code`,
  `document_id`, `document_type`, `event_token`, `properties[…]` (an employee — `user_<number>`), `use_subscription`,
  `timeout_duration` (seconds, `0` — no limit), `ts` and `auth` (the tokens of `AUTH_USER_ID`, else of the process's
  starter; `client_endpoint` — `/rest/`). With `USE_SUBSCRIPTION` = `Y` the process waits for `bizproc.event.send` (not
  longer than the step's time); `RETURN_VALUES` become the fields `app_<code>_<key>` that conditions check. A token
  answered, timed out or of another application — `ERROR_WRONG_EVENT_TOKEN`.
- Not there: `bizproc.workflow.template.add`/`update` (the portal takes `.bpt` files of its own format — templates come by
  «Import» of the designer or `POST /api/v1/bp/templates`) and `bizproc.robot.*`.

## Calendar

| Method | What |
|---|---|
| `calendar.event.get` | (scope `calendar:read`) the meetings of a calendar: `type` — `user` (`ownerId` — the employee's number, the caller by default), `group` (the project's number), `company_calendar`; `from`, `to` (a month back to three months ahead by default), `section[]` — of these calendars only; a repeated meeting — each occurrence |
| `calendar.event.getbyid` | `id` → the meeting |
| `calendar.event.get.nearest` | the caller's meetings of the next `days` (60 by default), at most `maxEventsCount` |
| `calendar.event.add` | (`calendar:write`) `type`, `ownerId`, `name`, `from`, `to`, `skip_time`, `timezone_from`, `section`, `description`, `location`, `color`, `accessibility`, `importance`, `private_event`, `remind`, `rrule`, `attendees` → the meeting's number |
| `calendar.event.update` | `id` and the fields to change; `recursion_mode` — `this` (one occurrence, `current_date_from`), `next` (it and the following), `all` by default |
| `calendar.event.delete` | `id`: the organizer deletes the meeting; an invited person declines it |
| `calendar.meeting.status.get` | `eventId` → `Y`, `N`, `Q` or `H` (the organizer) |
| `calendar.meeting.status.set` | `eventId`, `status` — `Y`, `N`, `Q` |
| `calendar.accessibility.get` | `users[]`, `from`, `to` → `{number: [{ID, NAME, DATE_FROM, DATE_TO, ACCESSIBILITY, IMPORTANCE}]}` — busy times as in the meeting planner; another's private meeting has no name |
| `calendar.section.get` | `type`, `ownerId` → the calendars: `ID`, `NAME`, `COLOR`, `CAL_TYPE`, `OWNER_ID`, `PERM` (`view_time`, `view_title`, `view_full`, `add`, `edit`, `edit_section`, `access`) |
| `calendar.section.add` | `name`, `color` → a calendar of one's own |
| `calendar.section.update` | `id`, `name`, `color`; the main calendar — the colour only |
| `calendar.section.delete` | `id` — a calendar of one's own, with its meetings; the main one — `ACCESS_DENIED` |

- Fields of a meeting: `ID`, `PARENT_ID`, `CAL_TYPE`, `OWNER_ID`, `NAME`, `DESCRIPTION`, `LOCATION`, `DATE_FROM`,
  `DATE_TO` (`dd.mm.yyyy hh:mm:ss` in the meeting's zone, the date only for an all-day one), `TZ_FROM`, `TZ_TO`,
  `DT_SKIP_TIME`, `DT_LENGTH`, `DATE_FROM_TS_UTC`, `DATE_TO_TS_UTC`, `SECT_ID`, `ACCESSIBILITY` (`busy`, `quest`, `free`,
  `absent`), `IMPORTANCE`, `PRIVATE_EVENT`, `COLOR`, `IS_MEETING`, `MEETING_STATUS`, `MEETING_HOST`, `CREATED_BY`,
  `ATTENDEE_LIST`, `ATTENDEES_CODES` (`U<number>`), `REMIND` (`[{type: "min", count}]`), `RRULE` (`FREQ`, `INTERVAL`,
  `BYDAY`, `COUNT`, `UNTIL`), `DATE_FROM_ORIGINAL` and `EXDATE` of an occurrence.
- `from`/`to` take ISO 8601, `dd.mm.yyyy hh:mm[:ss]` or a date; `remind` — `[{type: "min"|"hour"|"day", count}]`;
  `rrule` — `{FREQ: DAILY|WEEKLY|MONTHLY|YEARLY, INTERVAL, BYDAY: ["MO", …], COUNT, UNTIL}`.
- Rights are those of the calendar pages: one's own calendar and those whose owners gave «Управление»; a project's —
  its members; the company's — every employee (not a guest) reads and adds; a meeting is changed by its organizer. An invited person sees the meeting in their own
  main calendar, with their own `OWNER_ID`.
- Numbers: meetings and calendars have numbers (migration 735), given once and never reused; a calendar gets its
  number when an integration first sees it. Tasks, absences, birthdays and CRM deeds shown in the calendar are not
  meetings and are not returned.
- Not there: `calendar.resource.*` (rooms are booked in «Переговорные»), `calendar.settings.*` and
  `calendar.user.settings.*`.

## Drive

GO24DEV-886; scope `disk` (`disk:read`, changes — `disk:write`).

| Method | What |
|---|---|
| `disk.storage.getlist` | the caller's storages: «Мой диск» (`ENTITY_TYPE` `user`, `ENTITY_ID` — the employee's number), «Общий диск» (`common`), the drives of their projects (`group`, the project's number); `filter` by `ID`, `NAME`, `ENTITY_TYPE`, `ENTITY_ID` |
| `disk.storage.get`, `getfields`, `gettypes` | `id` → `{ID, NAME, CODE, MODULE_ID, ENTITY_TYPE, ENTITY_ID, ROOT_OBJECT_ID}` |
| `disk.storage.getchildren` | `id`, `filter`, `start` → the folders and files of the drive's top, folders first, 50 a page |
| `disk.storage.addfolder`, `disk.folder.addsubfolder` | `id`, `data[NAME]` → the new folder |
| `disk.storage.uploadfile`, `disk.folder.uploadfile` | `id`, `data[NAME]`, `fileContent` (`[name, base64]`), `generateUniqueName` → the new file; without `fileContent` → `{field: "file", uploadUrl}`: a POST of the form's `file` to `uploadUrl` gives `{result: file}` |
| `disk.folder.get`, `getchildren`, `getfields` | `id` (`ROOT_OBJECT_ID` of a storage is a folder too) |
| `disk.folder.rename`, `disk.file.rename` | `id`, `newName` |
| `disk.folder.moveto`, `copyto`, `disk.file.moveto`, `copyto` | `id`, `targetFolderId` — of this drive or another; `copyto` gives the copy («name (2)» when the name is taken) |
| `disk.folder.markdeleted`, `restore`, `disk.file.markdeleted`, `restore` | into the drive's trash and back; a deleted one has `DELETED_TYPE` `3`, `DELETED_BY`, `DELETE_TIME` |
| `disk.folder.deletetree`, `disk.file.delete` | for good, from the drive or from its trash → `true` |
| `disk.file.get`, `getfields` | `id` → the file |
| `disk.file.getversions` | `id` → the versions, the newest first: `ID` (the place in the history from 1), `OBJECT_ID`, `SIZE`, `NAME`, `CREATE_TIME`, `CREATED_BY`, `DOWNLOAD_URL` |
| `disk.file.uploadversion` | `id`, `fileContent` (or the `uploadUrl` it gives) → the file with its new version |
| `disk.file.restorefromversion` | `id`, `versionId` — a version of `getversions` becomes the current one again |

- Fields of a folder: `ID`, `NAME`, `CODE`, `STORAGE_ID`, `TYPE` (`folder`), `REAL_OBJECT_ID`, `PARENT_ID`,
  `DELETED_TYPE`, `CREATE_TIME`, `UPDATE_TIME`, `DELETE_TIME`, `CREATED_BY`, `UPDATED_BY`, `DELETED_BY`, `DETAIL_URL`;
  a file has `TYPE` `file`, `GLOBAL_CONTENT_VERSION` (how many versions), `FILE_ID`, `SIZE`, `DOWNLOAD_URL` too.
- `DOWNLOAD_URL` and `uploadUrl` are signed links of go24 (`/rest/download?token=…`, `/rest/upload?token=…`): they work
  without a key for a day, while the employee is active and may read (write) there, from the addresses «Ограничение
  доступа по IP» lets in.
- A name already in the folder is `DISK_OBJ_22000`; `generateUniqueName=Y` gives «name (2)».
- Rights are those of the drive's pages: one's own «Мой диск», «Общий диск» (folders closed by their rights are not
  seen), the drives of one's projects; another person's «Мой диск» — an administrator in the admin mode.
- Numbers: folders and files are numbered from one sequence (migration 772), given once and never reused; a storage
  and its `ROOT_OBJECT_ID` get their numbers when an integration first sees the drive.
- `fileContent` goes in the body of the call (up to 8 MB); a larger file — by `uploadUrl` (up to the drive's upload
  limit). Not there: `disk.storage.getforapp`, `disk.attachedObject.*`, external links and the rights of
  `disk.folder.sharetouser`.

## Telephony

GO24DEV-921; scope `telephony` (`telephony:read`, changes — `telephony:write`). The methods of an outside PBX (a cloud
PBX, a SIP provider's) that tells go24 of its calls «Интеграция с внешней АТС», and the call log.

| Method | What |
|---|---|
| `telephony.externalcall.register` | `USER_ID` (a number) or `USER_PHONE_INNER`, `PHONE_NUMBER`, `TYPE` (1 outgoing, 2 incoming, 3 incoming redirected, 4 callback, 5 info), `CALL_START_DATE`, `LINE_NUMBER`, `CRM_CREATE` (default 1), `CRM_SOURCE` (the lead's source, `CALL` by default), `CRM_ENTITY_TYPE` + `CRM_ENTITY_ID`, `SHOW` (default 1) → `{CALL_ID, CRM_ENTITY_TYPE, CRM_ENTITY_ID, CRM_CREATED_LEAD, CRM_CREATED_ENTITIES}`: the client is found by the phone (a contact, a company, a lead), an unknown number makes a lead (when the line allows), the employee gets the call in the bell with a link to the card |
| `telephony.externalcall.finish` | `CALL_ID`, `USER_ID` (who talked), `DURATION`, `STATUS_CODE` (`200` answered, `304` missed, `486` busy, `603` declined, others — failed), `FAILED_REASON`, `COST`, `COST_CURRENCY`, `RECORD_URL`, `VOTE` (1–5) → the call as `voximplant.statistic.get` gives it: it goes into the log, the client's timeline, «Входящий/Пропущенный звонок» triggers, the missed-call notification, `ONVOXIMPLANTCALLEND` and the webhook `call.ended`; the record is transcribed when that is on |
| `telephony.externalcall.attachRecord` | `CALL_ID`, `FILENAME` + `FILE_CONTENT` (base64) or `RECORD_URL` → `{CALL_ID, FILE_NAME, CALL_RECORD_URL}`; before or after `finish`; mp3, wav, ogg, m4a up to 100 MB |
| `telephony.externalcall.show`, `hide` | `CALL_ID`, `USER_ID` (one or a list; none — the call's employee) → `true`: the call in the bell is shown again or removed |
| `telephony.externalCall.searchCrmEntities` | `PHONE_NUMBER` → `[{CRM_ENTITY_TYPE, CRM_ENTITY_ID, NAME, ASSIGNED_BY_ID, ASSIGNED_BY: {ID, USER_PHONE_INNER}}]` — the cards the key's owner sees |
| `telephony.externalLine.get` | → `[{NUMBER, NAME, CRM_AUTO_CREATE}]` |
| `telephony.externalLine.add`, `update` | `NUMBER`, `NAME`, `CRM_AUTO_CREATE` (`Y`/`N`: an unknown number of the line makes a lead) → `{ID}` (the number) |
| `telephony.externalLine.delete` | `NUMBER` |
| `voximplant.statistic.get` | the call log: `filter` by the fields, `>=CALL_START_DATE` and `<=CALL_START_DATE` (the period), `order`, `select`, `start` → `ID`, `CALL_ID`, `PORTAL_USER_ID`, `PORTAL_NUMBER`, `PHONE_NUMBER`, `CALL_TYPE` (1 outgoing, 2 incoming), `CALL_DURATION`, `CALL_START_DATE`, `CALL_STATUS`, `CALL_FAILED_CODE`, `CALL_FAILED_REASON`, `CALL_VOTE`, `COST`, `COST_CURRENCY`, `CALL_RECORD_URL`, `CRM_ENTITY_TYPE`, `CRM_ENTITY_ID`, `COMMENT` |

- Rights: an employee registers, shows and finishes their own calls; for another employee and for the lines the key's
  owner must be one who may change telephony's settings (an administrator or a role with «Настройки телефонии»).
  `voximplant.statistic.get` shows the calls «Права доступа» of telephony show (own, the department's, all); the record's
  link — to one who may listen to it.
- The calls of the log are numbered (migration 840); `CALL_ID` of go24's own calls is Asterisk's id, of an outside
  PBX's — `externalCall.<hex>.<time>`. `voximplant.statistic.get` looks through the last 5000 calls of the period.
- A line not added by `externalLine.add` makes leads (as «create in CRM» is on). The records of outside calls are kept
  where Asterisk's are, `RECORD_URL` is loaded from the internet only.
- Not there: `telephony.externalcall.attachRecord` by `uploadUrl`, `CRM_ACTIVITY_ID` (go24 keeps a call in the timeline,
  not as a deed), `voximplant.*` of Voximplant's own lines and SIP accounts (go24's PBX is Asterisk, see `/api/v1/telephony`).

## Open lines

GO24DEV-930; scope `imopenlines` (`openlines:read`, actions — `openlines:write`). The lines and the dialogues of
«Открытые линии» gives them; the rights are those of the page: an operator sees their own dialogues, the
unassigned ones of their lines, those they are invited into and their subordinates'; administrators and the lines'
heads — all. A dialogue is one number for both `CHAT_ID` and `SESSION_ID` (migration 866); `DIALOG_ID` `chatN` and
`USER_CODE` `connector|line|N|…` are read too. A client's next dialogue is a new number.

| Method | What |
|---|---|
| `imopenlines.config.list.get` | `PARAMS[filter][ACTIVE]`, `PARAMS[filter][ID]`, `OPTIONS[QUEUE]` = `Y` (with the queues) → the lines |
| `imopenlines.config.get` | `CONFIG_ID`, `WITH_QUEUE` (default `Y`) → `ID`, `ACTIVE`, `LINE_NAME`, `QUEUE` (the operators' numbers), `QUEUE_TYPE` (`all`, `evenly`, `strictly`), `QUEUE_TIME`, `NO_ANSWER_TIME`, `CRM_CREATE` (`lead`, `none`), `CRM_FORWARD`, `WORKTIME_ENABLE`, `WORKTIME_FROM`/`TO` (`9.30`), `WORKTIME_DAYOFF` (`SA`, `SU`…), `WORKTIME_DAYOFF_TEXT`, `WELCOME_MESSAGE_TEXT`, `NO_ANSWER_TEXT`, `CLOSE_TEXT`, `AUTO_CLOSE_TIME`, `VOTE_MESSAGE`, `VOTE_MESSAGE_1_TEXT`, `AGREEMENT_MESSAGE`, `MAX_CHAT`, `TYPE_MAX_CHAT` (`ANSWERED`, `CLOSED`), `KPI_FIRST_ANSWER_TIME`, `KPI_FURTHER_ANSWER_TIME`, `HISTORY_SEND_ON`; the times are seconds |
| `imopenlines.config.add` | `PARAMS` — the fields above → the new line's number (administrators) |
| `imopenlines.config.update` | `CONFIG_ID`, `PARAMS` — only the given ones change → `true` (administrators) |
| `imopenlines.config.delete` | `CONFIG_ID` → `true` (administrators) |
| `imopenlines.config.path.get` | → `{SERVER_ADDRESS, PUBLIC_PATH}` — where the lines are set up |
| `imopenlines.dialog.get` | `CHAT_ID` / `SESSION_ID` / `DIALOG_ID` / `USER_CODE` → `{id, dialog_id, name, owner (the operator's number, 0 — none), type: lines, entity_id (connector\|line\|chat\|0), entity_data_1 (Y\|LEAD\|number\|…), status, spam, pinned, waiting, connector_id, connector_title, client: {name, phone, email, page}}` |
| `imopenlines.session.history.get` | `CHAT_ID` or `SESSION_ID` → `{chatId, sessionId, canJoin, canVoteAsHead, sessionVoteHead, sessionCommentHead, message: {id: {id, chat_id, author_id (0 — the client), date, text, system, params}}, usersMessage, users}`; the dialogue stays unread |
| `imopenlines.operator.answer` | `CHAT_ID` — the key's owner takes the dialogue |
| `imopenlines.operator.skip` | `CHAT_ID` — the operator turns their dialogue down: it goes to the next one of the queue |
| `imopenlines.operator.transfer` | `CHAT_ID`, `TRANSFER_ID` — an employee's number or `queueN` (the line N) |
| `imopenlines.operator.spam` | `CHAT_ID` — closed as spam |
| `imopenlines.operator.finish` | `CHAT_ID` — the operator closes their own dialogue |
| `imopenlines.operator.another.finish` | `CHAT_ID` — anyone's (administrators, the lines' heads) |
| `imopenlines.session.intercept` | `CHAT_ID` — «Перехватить диалог» |
| `imopenlines.session.start` | `CHAT_ID` — a closed dialogue opens again |
| `imopenlines.session.join` | `CHAT_ID` — the key's owner joins a dialogue they see as an invited colleague (GO24DEV-1008) |
| `imopenlines.session.mode.pin` | `CHAT_ID`, `ACTIVATE` (`Y`/`N`) — «Закрепить за собой» |
| `imopenlines.session.mode.pinAll`, `unpinAll` | → the numbers of the key owner's open dialogues pinned or unpinned |
| `imopenlines.session.mode.silent` | → `true`; a hidden message is `crm.message.add` with `HIDDEN` = `Y` |
| `imopenlines.session.head.vote` | `SESSION_ID`, `RATING` (4–5 — 👍, 1–2 — 👎, 0 — none), `COMMENT` — the head's rating (administrators, the lines' heads, the operator's managers) |
| `imopenlines.crm.chat.get` | `CRM_ENTITY_TYPE` (`LEAD`, `CONTACT`), `CRM_ENTITY`, `ACTIVE_ONLY` → `[{CHAT_ID, CONNECTOR_ID, CONNECTOR_TITLE}]`, the latest first |
| `imopenlines.crm.chat.getLastId` | `CRM_ENTITY_TYPE`, `CRM_ENTITY` → the number of the card's latest dialogue the key's owner sees (GO24DEV-1008) |
| `imopenlines.crm.chat.user.add` | `CRM_ENTITY_TYPE`, `CRM_ENTITY`, `USER_ID`, `CHAT_ID` (default — the latest) → the dialogue's number; the employee is invited into it, as «Пригласить в диалог» (GO24DEV-1008) |
| `imopenlines.crm.chat.user.delete` | the same → `true`; the employee leaves the dialogue (themselves, its operator, a line's head, an administrator) (GO24DEV-1008) |
| `imopenlines.crm.lead.create` | `CHAT_ID` — a lead of the dialogue, or its binding to the client CRM knows by the phone or e-mail (`crm:write` too) |
| `imopenlines.crm.message.add` | `CHAT_ID`, `MESSAGE`, `HIDDEN` (`Y` — a hidden message), `USER_ID` (the key's owner) → the message's id; the first to answer takes the dialogue |
| `imopenlines.message.quick.save` | `CHAT_ID`, `MESSAGE_ID` → `true`; the message becomes the key owner's own quick answer, its first line the title (GO24DEV-1008) |
| `imopenlines.revision.get` | → `{rest, web, mobile}` |

- The connectors: `livechat` (the widget), `telegrambot`, `vkgroup`, `viber`, `ok`, `avito`, `whatsappbyedna`, `max`
  and the applications' own (below).
- Not there: `imopenlines.bot.*` (chat bots of `imbot`), `imopenlines.network.join` and
  `imopenlines.message.session.start`. The go24 REST of the lines is `/api/v1/openlines`.

### Applications' channels

GO24DEV-989; an application's token only (`openlines:write`). An installed application connects its own messenger to
a line, as `imconnector.*`: the clients' messages it passes become the line's dialogues (channel «app»: the
queue, automatic messages, the CRM lead, statistics); the operators' answers and the line's messages go to it as
events. A client is one `chat.id` and `user.id` of the messenger; their next message joins the same dialogue (a closed
one opens again).

| Method | What |
|---|---|
| `imconnector.register` | `ID` (the code: `a-z0-9_.-`, one for the portal), `NAME`, `ICON[DATA_IMAGE]`, `PLACEMENT_HANDLER` → `true`; again — changes the name and icon; another application's code is refused (administrators) |
| `imconnector.unregister` | `ID` → `true`; the channel leaves every line, its dialogues stay (administrators) |
| `imconnector.list` | → `{code: name}` — go24's channels and the applications' |
| `imconnector.activate` | `CONNECTOR`, `LINE`, `ACTIVE` (`1`/`0`) → `true` (administrators) |
| `imconnector.status` | `CONNECTOR`, `LINE` → `{LINE, CONNECTOR, ERROR, CONFIGURED, STATUS}` |
| `imconnector.connector.data.set` | `CONNECTOR`, `LINE`, `DATA[id, url, url_im, name]` — the account on the line, shown in its settings (administrators) |
| `imconnector.send.messages` | `CONNECTOR`, `LINE`, `MESSAGES[]` = `{user: {id, name, last_name, email, phone}, message: {id, text, files: [{url, name}]}, chat: {id, name, url}}` → `{SUCCESS, DATA: {RESULT: [{SUCCESS, user, chat: {id}, message: [id], session: {ID, CHAT_ID}}]}}`; the first file of a message is taken into go24 (up to 50 MB), the others are links; a message of a channel not on is `SUCCESS: false` with `ERROR` |
| `imconnector.update.messages` | `MESSAGES[]` = `{message: {id, text}, chat: {id}}` — the client's message changes |
| `imconnector.delete.messages` | `MESSAGES[]` = `{message: {id}, chat: {id}}` — marked deleted |
| `imconnector.send.status.delivery` | taken (go24 shows only what was read) |
| `imconnector.send.status.reading` | `MESSAGES[]` = `{im: {message_id}, chat: {id}}` — the client has read the answers up to it |
| `imconnector.chat.name.set` | `CONNECTOR`, `LINE`, `CHAT_ID`, `NAME` — the dialogue's title |

- Events (`event.bind`): `ONIMCONNECTORMESSAGEADD` — `{CONNECTOR, LINE, MESSAGES: [{im: {chat_id, message_id}, message:
  {user_id, user_name, text, date, files: [{name, type, size, link}], keyboard}, chat: {id}}]}` for each operator's answer
  and the line's message (hidden ones and notes are not sent); `ONIMCONNECTORDIALOGFINISH` — `{CONNECTOR, LINE, CHAT_ID,
  SESSION_ID}` once a dialogue is closed; `ONIMCONNECTORSTATUSDELETE` — `{connector, line}` when an administrator turns
  the channel off in the line's settings («Каналы приложений»). The events are retried as the others of applications.
- Not there: `ONIMCONNECTORMESSAGEUPDATE`/`DELETE` (go24's operators do not edit sent answers), the channel's settings
  page in the line (`PLACEMENT_HANDLER` is kept), its icon in the lists.
