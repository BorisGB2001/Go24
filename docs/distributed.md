Русская версия: [ru/distributed.md](ru/distributed.md)

# Distributed Infrastructure of go24

## Objective

Distribute containers across multiple own servers (the product is self-hosted). For example, go24 and rclone on a low-spec server, Postgres and Authentik on a powerful one. A new server must be added without reworking configuration.

## Solution: Docker Swarm

NetBird has been excluded from the stack (decision of 2026-10-11). Communication between nodes is built using Docker Swarm's own mechanisms and a firewall.

```
                       Users (HTTPS)
                              │
                              ▼
 node-3 (edge)  ┌──────────────────────────┐
 role=edge      │ traefik :80/:443 (TLS)   │  ← single entry point from the outside
                └────────────┬─────────────┘
                             │  Swarm overlay network, IPsec encryption (--opt encrypted)
        ┌────────────────────┴──────────────────────┐
        ▼                                           ▼
 node-1 (low-spec) role=app                 node-2 (high-spec) role=db
 ┌──────────────────────┐                   ┌──────────────────────┐
 │ go24 ×N, rclone      │                   │ postgres             │
 │ worker, importer     │                   │ authentik-server     │
 └──────────────────────┘                   │ authentik-worker     │
                                            └──────────────────────┘
```

- Swarm is built into Docker: the same `docker-compose.yml` is deployed with `docker stack deploy`. No separate orchestrator is required.
- Where a service runs is configured by node labels and the `deploy.placement.constraints` constraint, for example `node.labels.role == db`. To move a service to another node, changing the label is sufficient.
- Nodes are interconnected as follows:
  - if the provider offers a **private network (VLAN)** between servers, Swarm runs over it;
  - if there is no private network, it runs over public IPs, but Swarm ports (2377/tcp, 7946/tcp+udp, 4789/udp, and ESP for encryption) are **open in the firewall only to the IPs of other nodes**.
- The overlay network is created with `--opt encrypted`, so traffic between containers on different nodes is sent over IPsec.
- Only Traefik (80/443) is exposed to the outside. Other services do not publish ports. Admins and developers use an SSH tunnel.
- Stateless services (go24, rclone, future `mail-sync` and `importer`) are scaled via `deploy.replicas`.
- Postgres is bound to a single node because of the data volume. A replica (streaming replication or Patroni) can be added later.
- Images are stored in our **own registry** because Swarm does not build them on nodes. Options: Forgejo (git + container registry + Actions for CI in a single product) or `registry:2`. External GitHub and GHCR are not used.

## Alternatives

| Option | Pros | Cons |
|---|---|---|
| **Docker Swarm** | Simplest: same compose files, no new tools | Smaller ecosystem, no auto-scaling |
| k3s (lightweight Kubernetes) | Industry standard, rich Helm ecosystem | Harder to maintain, requires separate skills |
| HashiCorp Nomad | Lightweight, flexible | Yet another specification language (HCL), BSL license |

## Migration Plan

1. Currently: single node, `docker compose`. This is how `deploy/docker-compose.yml` is set up.
2. On the first node: `docker swarm init --advertise-addr <private or public IP>`. Remaining nodes join via `docker swarm join`. Firewall between nodes is configured according to the list above.
3. Add `deploy:` sections with labels and replicas to compose. Secrets are moved to `docker secret`, the network is created with `encrypted`, and the go24 image is published to our own registry.
4. Node labels: `docker node update --label-add role=db node-2`.

## go24 Core: web and worker (decision 2026-10-13)

The core (tasks, projects, feed, calendar, employees) remains a single service: these parts are tightly coupled (shared permissions,
single transaction per action). Two containers are run from the same image:

| Container | `GO24_ROLE` | What it does |
|---|---|---|
| `go24` | `web` | pages, REST API, live updates (SSE); multiple copies can be run |
| `go24-worker` | `worker` | bridge to Matrix (sync, user accounts, rooms), messenger notifications, task change log in threads, recurring tasks, meeting reminders, cleanup |

They communicate via PostgreSQL: `LISTEN/NOTIFY` (for example, `go24_matrix_wake` — web asks worker to update rooms) and
queue tables with rows "yet to be sent". On startup, migrations are applied by whichever instance acquires the lock first.
`GO24_ROLE=all` — both modes in a single process, for the simplest installations.

Modules that live on their own are implemented as separate services with their own API: calls (LiveKit), telephony (Asterisk),
mail (IMAP), CRM, speech recognition (Whisper — already so).

**Message broker (decision 2026-10-13).** Kafka and RabbitMQ are not installed: communication between web and worker goes through PostgreSQL —
an event is written within the same transaction as the data, so it is never lost. When the first separate module arrives
(telephony, mail, CRM), we will connect lightweight NATS JetStream via an outbox table, without rewriting the core.
