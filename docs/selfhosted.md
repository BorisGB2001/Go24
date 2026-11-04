Русская версия: [ru/selfhosted.md](ru/selfhosted.md)

# go24 — Self-Hosted

go24 is deployed entirely on your own servers. Cloud services (SaaS) are not used in the stack: neither for operating the product, nor for building, nor for data storage.

## Components and where they come from

| Component | What we use | Where it runs |
|---|---|---|
| Authentication, users, 2FA | Authentik (open source) | own container |
| Database | PostgreSQL | own container |
| Files | rclone `serve s3` on top of own storage: local disk, SFTP to own file server, Garage/SeaweedFS, Nextcloud | own container + own storage |
| Inbound traffic and TLS | Traefik (later) | own container |
| Orchestration | Docker Swarm | own servers |
| Git, image registry, CI | Forgejo (git + Container Registry + Actions) or `registry:2` | own server |
| Backups | `pg_dump` + rclone to own storage (different server or disk) | own servers |

## Rules

- **No external SaaS dependencies.** External addresses are allowed only for downloading images and packages during installation and updates. Later they can be replaced with your own mirror.
- **Integrations with other products** (the portal, OpenProject, Jira, Confluence) connect via API keys only if the customer enabled them. The product works without them.
- **Network security:**
  - externally only Traefik (80/443) and SSH are exposed;
  - between nodes — private network or firewall by IP plus encrypted Swarm overlay network;
  - NetBird has been removed from the stack (decision of 2026-10-11).
- **Secrets** are stored in `deploy/.env` (mode 600, not committed to git), in Swarm — in `docker secret`.
