Русская версия: [ru/install.md](ru/install.md)

# Installing go24 for a test deployment

This guide sets up the full go24 stack on a single server in a local network: tasks, CRM, mail, drive, messenger, calls,
documents, and telephony. Every component runs in Docker on that server; no cloud services are required.

This is sufficient for testing on a local network. Do **not** expose this server to the internet: the test setup has no
real TLS certificates and no external proxy.

## 1. Requirements

|                | Minimum                                              | Recommended                                    |
|----------------|------------------------------------------------------|------------------------------------------------|
| CPU            | 4 cores                                              | 8 cores                                        |
| RAM            | 12 GB                                                | 16 GB or more                                  |
| Disk           | 40 GB free                                           | 100 GB (files, backups, call recordings)       |
| OS             | Debian 12/13 or Ubuntu 24.04, 64-bit                 | —                                              |
| Network        | a static IP on the local network, e.g. `192.168.1.50` | —                                            |

The biggest memory consumers are ONLYOFFICE (documents), Whisper (voice transcription), Synapse (messenger), and
Authentik (authentication). With 8 GB the stack will start, but it will be slow.

## 2. Prepare the server

Run once, as root or via `sudo`.

```bash
apt update && apt install -y git make openssl gettext-base curl
curl -fsSL https://get.docker.com | sh
useradd -m -s /bin/bash -G docker go24
mkdir -p /srv/go24 && chown go24:go24 /srv/go24
```

All subsequent steps are run as the `go24` user:

```bash
su - go24
```

> The `make` commands in this guide work when the code is in `/srv/go24` and are run by the `go24` user. It is best not
> to change the path or the username.

## 3. Download go24

```bash
git clone https://github.com/BorisGB2001/Go24.git /srv/go24
cd /srv/go24
git checkout $(git tag --sort=-v:refname | head -1)   # latest released version
cat VERSION
```

## 4. Configuration

1. Create the configuration file `deploy/.env` with random passwords:

   ```bash
   make secrets
   ```

2. Set the server IP. Replace `192.168.1.50` with your address:

   ```bash
   IP=192.168.1.50
   sed -i \
     -e "s|^BIND_IP=.*|BIND_IP=$IP|" \
     -e "s|^GO24_BASE_URL=.*|GO24_BASE_URL=http://$IP:8080|" \
     -e "s|^GO24_OIDC_ISSUER=.*|GO24_OIDC_ISSUER=http://$IP:9000/application/o/go24/|" \
     -e "s|^GO24_OIDC_ISSUER_BASE=.*|GO24_OIDC_ISSUER_BASE=http://$IP:9000|" \
     -e "s|^MATRIX_PUBLIC_URL=.*|MATRIX_PUBLIC_URL=https://$IP:8448|" \
     -e "s|^MATRIX_ELEMENT_URL=.*|MATRIX_ELEMENT_URL=https://$IP:8443|" \
     deploy/.env
   ```

   The time zone is set on the corresponding line in the same file: `GO24_TZ=Europe/Moscow`, etc.

3. File storage. For a test deployment the local server disk is sufficient:

   ```bash
   cp deploy/rclone/rclone.conf.example deploy/rclone/rclone.conf
   ```

4. Build the messenger (Matrix) configuration from templates:

   ```bash
   make matrix-config
   ```

`deploy/.env` contains all passwords. It is readable only by its owner and is excluded from git. Keep a copy in a
safe place: without it you cannot open the database or restore backups.

## 5. First start

```bash
make up
```

The first run takes 15–30 minutes: Docker images (several gigabytes) are downloaded and go24 is built. Verify that
everything started:

```bash
make ps
```

All services should be in `running` or `healthy` state. Authentik takes an additional 2–3 minutes after start.

**Updates.** The stack includes the `go24-updater` service, which installs new versions from **Settings → Update**
(see [update.md](update.md)). It sees the Docker socket `/var/run/docker.sock` — this gives it root rights on the
server, as `make up` itself has — and the installation folder, mounted at the same path as on the server. It publishes
no ports and only acts on jobs go24 leaves in the shared volume `updates`. If go24 is installed somewhere other than
`/srv/go24`, set `GO24_INSTALL_DIR=<that folder>` in `deploy/.env`.

## 6. Admin account and messenger

1. **Admin password.** Open `http://<IP>:9000/if/flow/initial-setup/` in a browser and set the password for the
   `akadmin` user. This user is the go24 administrator.
2. **Messenger bot.** Created once; then go24 is restarted with its key:

   ```bash
   make matrix-bot
   make up
   ```

3. **HTTPS certificate.** The messenger (Element), calls, and the camera work only over HTTPS. Certificates are
   issued by the built-in Caddy, and its root certificate must be trusted once on every computer you use to access
   go24:

   ```bash
   make dev-ca          # saves deploy/go24-dev-ca.crt
   ```

   Copy that file to your computer and add it to the trust store:
   - **macOS:** double-click → Keychain Access → System → set the certificate to *Always Trust*;
   - **Windows:** double-click → Install Certificate → Local Machine → Trusted Root Certification Authorities;
   - **Linux (Chrome/Firefox):** import in the browser settings → Certificate Authorities.

## 7. Access

| What                                  | Address                                                  |
|---------------------------------------|----------------------------------------------------------|
| **go24**                              | `http://<IP>:8080` (calls and camera — `https://<IP>:8444`) |
| Authentication and users (Authentik)  | `http://<IP>:9000`                                       |
| Messenger standalone client (Element) | `https://<IP>:8443`                                      |
| Automation (n8n)                      | `http://<IP>:5678`                                       |

Sign in to go24 as `akadmin` with the password you set in step 6.

## 8. First steps in go24

1. **Admin mode** — button in the top bar: shows all settings, projects, and tasks.
2. **Settings → General settings:** company name and other portal settings.
3. **Employees → Invite employees:** e-mail and name, one per line. An invitation link is generated for each person;
   it can be copied from the page if mail is not yet configured.
4. **Employees → Company structure:** departments and managers.
5. **Tasks:** create a project and a few tasks; check the Kanban board, Gantt chart, and My plan.
6. **Messenger:** send a message to an invited employee, create a group chat.
7. **Mail:** connect your mailbox under Mail (IMAP and SMTP of your mail server).
8. **Calls:** open go24 at `https://<IP>:8444` and call from a chat — the browser will ask for camera and microphone
   access.
9. **Settings → Backup:** enable database and file backups. By default they are stored in `deploy/backups`; a
   different disk can be specified with the `GO24_BACKUP_DIR` variable in `deploy/.env`.

Upgrading to a new version — [update.md](update.md).

## Troubleshooting

| Symptom                                           | What to check                                                                                                                 |
|---------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------|
| go24 page does not open                           | `make ps` — is `go24` running; `make logs s=go24` — startup error                                                            |
| "Login error" or infinite redirect to login       | IP in `GO24_BASE_URL` and `GO24_OIDC_ISSUER` matches the address you use to open go24; Authentik is up (`make logs s=authentik-server`) |
| Messenger reports it cannot reach the server      | Is the certificate from step 6.3 trusted; `make logs s=synapse`                                                              |
| No camera or microphone in a call                 | go24 must be opened at `https://<IP>:8444`, not `http://…:8080`                                                              |
| Everything is slow, server is struggling          | `free -h`: more than 2 GB of free RAM is required; add memory                                                                |

Stop everything: `make down` (data is preserved in Docker volumes). Start again: `make up`.
