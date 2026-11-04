Русская версия: [ru/architecture.md](ru/architecture.md)

# go24 Architecture

## Goal

In-house product replacing the portal. **The main objective is a task tracker and project management**, as close as possible to the portal tasks and OpenProject (API analysis in [api-research.md](api-research.md)). Store, B2B, leads, and sales funnel are not being developed yet. Mail, contacts, and companies are second priority.

The infrastructure is distributed; containers can be split across servers ([distributed.md](distributed.md)). Integration with other products (the portal, OpenProject, Jira, Confluence) is handled via API keys.

## Diagram

```
                 Employees and developers
                                 │  currently SSH tunnel, later Traefik + TLS
                                 ▼
 ┌──────────────── server (Docker), ports bound only to BIND_IP ──────────────┐
 │                                                                            │
 │  [authentik-server :9000] ◄─ login (OIDC) ── [go24 :8080]                  │
 │  [authentik-worker]                            │  │                        │
 │          │                                     │  │ S3 API                 │
 │          ▼                                     ▼  ▼                        │
 │     [postgres] databases: authentik, go24 [rclone serve s3 :8333]          │
 │                                                │                           │
 └────────────────────────────────────────────────┼───────────────────────────┘
                                                  ▼
               own storage: local disk, SFTP, Garage/SeaweedFS, Nextcloud
```

## Decisions

| Decision | Why |
|---|---|
| Users and authentication — Authentik, go24 is only an OIDC client | We do not write our own authentication, 2FA, password recovery, or groups. go24 takes roles from Authentik groups (`groups` claim) |
| go24 session — HMAC-signed cookie | The service does not need to store state, making it easier to scale |
| Self-hosted product: all components are installed on own servers, cloud services are not used | Customer requirement. Details in [selfhosted.md](selfhosted.md) |
| Files via `rclone serve s3`, with go24 communicating via S3 API | Storage is changed by editing `RCLONE_REMOTE`, leaving code untouched. Can later be replaced with real S3 without changes to go24 |
| Ports published only on `BIND_IP` (127.0.0.1), access during development via SSH tunnel | On the development server, 80/443 is occupied by Legacy nginx. Public access later via Traefik with TLS. NetBird is excluded from the stack |
| Postgres — single container for `go24` and `authentik` databases | Fewer containers on the development server. Can be separated in production |
| SQL migrations embedded in the binary (`embed`) and applied on startup | No separate migration tool needed |
| Go is built inside Docker (`golang:1-alpine`), `alpine` image | No Go required on the host. The server is CentOS 7; installing a toolchain there is undesirable |

## Roadmap

1. **Skeleton (completed):** login via Authentik, users, file uploading and serving via rclone, migrations.
2. **Task tracker core (first version completed 2026-10-11: projects, tasks, statuses and roles, subtasks, checklists, comments, history, "My tasks"):**
   - projects (hierarchy, participants, roles);
   - tasks with `PREFIX-N` key, types and statuses with workflow;
   - task participant roles: creator, assignee, participants, observers, result review;
   - subtasks, checklists, comments, history, attachments.
3. **go24 API keys and REST API v1** — completed 2026-10-11 (keys with permissions and expiration, REST API for tasks and projects). Webhooks remain to be added.
4. **Views:** completed 2026-10-11 — Kanban by status, "My plan", counters, time tracking, Gantt chart with dependencies. Next: list with filters and saved queries, dragging bars on Gantt, auto-shifting dependent tasks.
5. **Agile:** versions, sprints, epics, backlog (Scrum and Kanban).
6. **Import** (the portal completed 2026-10-11: 335 tasks, comments, participants, with anonymization): `importer` with adapters for the portal (tasks and groups from the portal), OpenProject, Jira, and Confluence — via API keys.
7. **Knowledge Base:** spaces and pages, like in Confluence, with links to tasks.
8. **Distributed deployment:** Docker Swarm (encrypted overlay network, firewall between nodes), own image registry, CI.
9. **Phase two:** mail (IMAP), contacts, and companies.
10. **File storage — Nextcloud (long-term perspective), login via Authentik:**
    - rclone connects to Nextcloud via WebDAV (`RCLONE_REMOTE=nextcloud:...`), go24 code remains unchanged;
    - a second OIDC provider (blueprint) is added to Authentik for the Nextcloud `user_oidc` app — single account for go24 and Nextcloud;
    - later: opening and collaborative editing of task attachments in Nextcloud.
11. **"Storages" section** (completed 2026-10-11) — rclone remains the primary storage layer. Users connect their own storage (Nextcloud, SFTP, S3, server disk) directly from the go24 interface via rclone's built-in management API (`rclone rcd`: `config/create`, `operations/list`), without modifying files on the server.
12. **Asterisk telephony (future plans) telephony:**
    - a separate `telephony` service connects to its own Asterisk via ARI or AMI;
    - click-to-call from the card, pop-up card on incoming call;
    - call log and call recordings in storage via rclone;
    - task or comment from a call.
