Русская версия: [ru/api-research.md](ru/api-research.md)

# API Analysis: the portal, OpenProject, Jira, Confluence

Goal: design the go24 task tracker as close as possible to the portal tasks and project management in OpenProject. Also lay the foundation for data import from these systems and compatibility with them.

Sources verified on 2026-10-11: openproject.org/docs/api, developer.atlassian.com. Where pages were truncated, information was supplemented with known API behavior; such places are marked *(verify)*.

## 1. the portal — Tasks and Projects

**Protocol:** REST over HTTP, call `https://<portal>/rest/<method>`. Authentication — inbound webhook (`/rest/<user>/<token>/`) or application OAuth 2.0. Legacy methods use the `task` scope, new REST 3.0 methods use the `tasks` scope. Lists return 50 entries per page; the next page is requested via `start`.

| Entity | Methods |
|---|---|
| Task | `tasks.task.add/update/get/list/delete`, `getFields`, `getaccess`, `history.list`, `counters.get`; in REST 3.0 the same methods and `access.*`, `field.*` |
| Status change | `tasks.task.start/pause/defer/complete/renew`, `delegate`, `approve/disapprove` (creator review) |
| Personal | `startwatch/stopwatch`, `favorite.add/remove`, `pin/unpin`, `mute/unmute` |
| Task result | `tasks.task.result.add/addFromComment/list/update/delete` |
| Checklists | `task.checklistitem.add/update/get/getlist/moveafteritem/complete/renew/delete` |
| Comments | `task.commentitem.*` (in the new task card, comments are replaced by chat: `tasks.task.chat.message.send`) |
| Time tracking | `task.elapseditem.add/update/get/getlist/delete` |
| Dependencies for Gantt | `task.dependence.add/delete`, `tasks.task.gantt.link.list` |
| Kanban / "My plan" | `task.stages.add/update/get/movetask/canmovetask/delete` |
| Custom fields | `task.item.userfield.*` |
| Flows | `tasks.flow.Flow.create/get/update/delete/activate` |
| Groups and projects | `sonet_group.create/update/get/delete`, `socialnetwork.api.workgroup.list/get`, `sonet_group.user.add/update/delete/get` (member roles), `sonet_group.setowner` |
| Scrum | Group with `SCRUM_MASTER_ID`; `tasks.api.scrum.*` methods: backlog, sprints, epics, sprint kanban *(verify)* |
| Events | `OnTaskAdd/OnTaskUpdate/OnTaskDelete`, `onSonetGroupAdd/Update/Delete`, outbound webhooks |

**Task model:**
- **People:** creator `CREATED_BY`, responsible/assignee `RESPONSIBLE_ID` (exactly one), accomplices `ACCOMPLICES[]`, auditors/observers `AUDITORS[]`.
- **Relations:** `GROUP_ID` (group or project), `PARENT_ID` (subtasks), `DEADLINE`, plan `START_DATE_PLAN/END_DATE_PLAN`, `PRIORITY` (0/1/2 — low, medium, high).
- **Statuses:** 2 — pending (awaiting execution), 3 — in progress, 4 — awaiting review, 5 — completed, 6 — deferred.
- **Flags:** `TASK_CONTROL` (accept work / review), `ALLOW_CHANGE_DEADLINE`, `ALLOW_TIME_TRACKING`, `TIME_ESTIMATE`, `TAGS`.
- **Bindings:** `UF_CRM_TASK` (`C_3`, `CO_5` ...), `UF_TASK_WEBDAV_FILES` (Drive files), `UF_MAIL_MESSAGE`.
- **Kanban stages** are linked to a group, to "My plan", or to a sprint independently of the task status.

**What to adopt in go24:** task member roles (creator, assignee, accomplices, observers), result control and "Task result", checklists, time tracking, kanban stages independent of status, "My plan", counters ("overdue", "awaiting review", "new comments").

## 2. OpenProject — Projects and Work Packages (API v3)

**Protocol:** REST, HAL+JSON (`application/hal+json`). Links are passed in `_links`, embedded objects in `_embedded`, collections have `_type: Collection` and fields `total`, `count`, `pageSize`, `offset`. Authentication — API key (Basic `apikey:<key>`) or OAuth 2.0. Endpoints inside a project are deprecated, replaced by `/api/v3/workspaces/{id}/...`.

| Entity | Endpoints |
|---|---|
| Work packages | `GET/POST /api/v3/work_packages`, `GET/PATCH/DELETE /work_packages/{id}`, `/form` (validation before saving), `/schemas` |
| Comments and history | `GET/POST /work_packages/{id}/activities` |
| Relations | `POST /api/v3/relations`. Types: relates, duplicates, blocks, precedes/follows (with lag), includes, requires |
| Watchers | `/work_packages/{id}/watchers`, `available_watchers` |
| Attachments | `/attachments`, direct upload, file links to Nextcloud/OneDrive |
| Time tracking | `/api/v3/time_entries` (+ activities) |
| Catalogs | `/types`, `/statuses`, `/priorities`, `/categories`, `/versions`, `/roles`, `/memberships` |
| Projects | `/api/v3/projects` (hierarchy via parent), statuses, members with roles |
| Saved queries | `/api/v3/queries`: filters, columns, grouping, sums — these are "views" (table, Gantt, board) |
| Other | `watch/unwatch`, `copy`, `move`, `customActions`, reminders, `previewMarkup` |

**Work package model:**
- **Fields:** `subject`, `description` (`format`, `raw`, `html`), `startDate`/`dueDate`/`date` (milestones), `scheduleManually`, `estimatedTime`/`remainingTime`/`spentTime`, `percentageDone`, `storyPoints`, `lockVersion` (optimistic locking), derived from children `derived*`.
- **Links (`_links`):** `type`, `status`, `priority`, `project` — required; `assignee`, `responsible` (accountable person is a separate role), `version`, `parent`, `children`, `category`, `sprint`, `projectPhase`, `relations`, `watchers`, `attachments`, `timeEntries`, `customFieldN`.
- **Filters:** JSON array like `[{"status_id":{"operator":"o","values":null}}]`. Sorting: `sortBy=[["id","asc"]]`. Also `groupBy`, `showSums`, `select`.

**What to adopt in go24:**
- work package types (task, milestone, phase, epic, bug) with customizable field sets;
- status workflow by roles and types;
- relations with scheduling where precedes/follows shift dates, and Gantt chart;
- versions and sprints, project hierarchy, roles and permissions at the project level;
- saved queries as the basis for all views;
- `lockVersion` and `/form` validation; calculated parent values.

## 3. Jira Cloud (REST v3 + Agile)

**Protocol:** REST, `/rest/api/3/...`. Description and comment text is stored in **ADF** (Atlassian Document Format, JSON tree). Authentication — Basic (email + API token) or OAuth 2.0 (3LO). JQL search: `GET/POST /rest/api/3/search/jql` with paginated output via `nextPageToken`; legacy `/rest/api/3/search` is decommissioned *(verify date)*.

| Entity | Endpoints |
|---|---|
| Issues | `POST /issue`, `GET/PUT/DELETE /issue/{key}`, `POST /issue/bulk`, `GET /issue/createmeta/{project}/issuetypes` |
| Workflow transitions | `GET/POST /issue/{key}/transitions` |
| Comments, time tracking | `/issue/{key}/comment`, `/issue/{key}/worklog` |
| Links | `POST /issueLink`, `/issueLinkType` |
| Attachments, watchers | `/issue/{key}/attachments`, `/issue/{key}/watchers` |
| Projects and schemes | `/project`, `/issuetype`, `/priority`, `/status`, `/workflow/search`, `/field` (custom fields) |
| Agile | `/rest/agile/1.0/board`, `/board/{id}/sprint`, `/sprint/{id}/issue`, `/epic`, `/backlog/issue` |
| Events | Webhooks `jira:issue_created/updated/deleted`, `comment_*`, `sprint_*` |

**What to adopt in go24:**
- human-readable task keys `PROJ-123`;
- query language like JQL (our own simplified "GQL");
- workflow as a set of transitions with conditions and validators;
- Scrum and Kanban boards, backlog, epics;
- cursor pagination and bulk operations.

## 4. Confluence Cloud (REST v2) — Knowledge Base

**Protocol:** REST, `/wiki/api/v2/...`. Cursor pagination: `limit` + `cursor`, link to the next page in `Link: rel="next"` and `_links.next`. Authentication — Basic (API token) or OAuth 2.0. CQL search remains in v1: `/wiki/rest/api/search?cql=...`.

| Entity | Endpoints |
|---|---|
| Spaces | `/spaces`, space permissions and properties |
| Pages | `GET/POST /pages`, `GET/PUT/DELETE /pages/{id}`, `PUT /pages/{id}/title`, `/spaces/{id}/pages`, `/labels/{id}/pages` |
| Tree | `/pages/{id}/children`, `/ancestors`, `/descendants`, folders (`/folders`) |
| History | `/pages/{id}/versions`; on update, `version.number` (+1) must be passed |
| Other | blog posts, comments (footer/inline), attachments, labels, likes, page tasks, whiteboards, databases |

**Body format:** `body-format=storage` (XHTML) or `atlas_doc_format` (ADF). A new page is published by default, draft is set via `status: draft`. The page stores `spaceId`, `parentId`, and `parentType`.

**What to adopt in go24:**
- "Knowledge" module: spaces, page tree, and versioning;
- "task ↔ page" links;
- document format based on a JSON tree, like ADF or ProseMirror, shared across task descriptions, comments, and pages.

## 5. Unified go24 Model

| go24 Concept | the portal | OpenProject | Jira | Confluence |
|---|---|---|---|---|
| Workspace | portal | instance | site | site |
| Project (hierarchy) | group or project (`sonet_group`) | project | project | space |
| Task (type, `PREFIX-N` key) | task | work package | issue | — |
| Task type | — | type | issue type | — |
| Status + workflow | 5 fixed statuses | status + workflow by roles | status + transitions | — |
| Board stage | `task.stages` | board column (query) | board column | — |
| Assignee | `RESPONSIBLE_ID` | assignee | assignee | — |
| Accountable | — | responsible | — | — |
| Accomplices / observers | `ACCOMPLICES` / `AUDITORS` | watchers | watchers | watchers |
| Creator + review | `CREATED_BY` + `TASK_CONTROL` | author | reporter | — |
| Subtasks | `PARENT_ID` | parent/children | sub-task / parent | page tree |
| Relations | `task.dependence` (Gantt) | relations (precedes, etc.) | issue links | — |
| Checklist | `checklistitem` | — | (plugins) | task list |
| Time tracking | `elapseditem` | time entries | worklog | — |
| Versions / sprints / epics | scrum | versions, sprints | sprints, epics | — |
| Comments and history | task chat, `history.list` | activities | comments, changelog | comments, versions |
| Attachments | Drive | attachments | attachments | attachments |
| Custom fields | `UF_*` | `customFieldN` | `customfield_N` | content properties |
| Saved filters / views | list filters | queries | JQL filters | CQL |
| Events | `OnTask*` | webhooks | webhooks | webhooks |

## 6. Conclusions for go24 API

1. **Native API** — REST + JSON, resources as in the table above:
   - cursor pagination, like Jira and Confluence;
   - `version` for optimistic locking, like `lockVersion`;
   - JSON structure filters, like OpenProject, and human-readable string queries, like JQL;
   - webhooks for all changes.
2. **Document (description, comment, page)** is stored as a JSON tree in the spirit of ProseMirror or ADF, Markdown and HTML are served as representations.
3. **Import** — a dedicated `importer` service with `portal`, `openproject`, and `jira+confluence` adapters. Each adapter normalizes data into the unified model. the portal comes first: there are 335 tasks on the portal, which can be retrieved directly from MySQL `sitemanager` or via REST.
4. **Compatibility.** Later, a layer accepting `tasks.task.*` calls following the portal pattern can be added so that existing integrations continue to work without rewrites.

## 7. Inter-product Communication via API Keys

Decision of 2026-10-11: integrations between go24 and other systems (in both directions) work via API keys. OAuth applications are not needed.

| System | Key | How it is passed |
|---|---|---|
| the portal | Inbound webhook: user + token, permissions by scope (`task`, `tasks`, `sonet`, `crm`, `disk`) | In URL: `https://<portal>/rest/<user_id>/<token>/<method>` |
| OpenProject | User API key ("My account → Access tokens") | `Authorization: Basic base64("apikey:<key>")` |
| Jira Cloud | Atlassian account API token | `Authorization: Basic base64("<email>:<token>")` |
| Confluence Cloud | Same Atlassian API token | Same as Jira |
| **go24 (inbound requests)** | Native go24 keys | `Authorization: Bearer go24_<id>_<secret>` |

**go24 keys:**
- Created in the UI: for a user or a service account. They act on behalf of the owner with scopes (`tasks:read`, `tasks:write`, `projects:*`, `files:*`, `admin`) and an expiration date.
- Only the hash (SHA-256 + salt) is stored in the database; the secret is shown once upon creation. The `go24_<id>_` prefix allows looking up the key without table scans and seeing it in logs without exposing the secret.
- An access log is kept: timestamp, source IP, method called. The key can be revoked.

**External system keys in go24:**
- Stored in the `integrations` table (type, base URL, login or email, key). The key is encrypted with AES-GCM; the master key is passed via an environment variable or `docker secret`.
- Used by `importer` (one-time migration) and `sync` (two-way webhook synchronization).
- Inbound webhooks, Jira, and OpenProject are validated by secret in the URL or by signature.

**Inside go24**, services communicate via service keys or over an internal network; user sign-in goes exclusively through Authentik.
