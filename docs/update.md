Русская версия: [ru/update.md](ru/update.md)

# Updating go24

New versions are published on GitHub: https://github.com/BorisGB2001/Go24/releases. Each release has a version number
`X.Y.Z` and a changelog. The numbering works as follows:

- **last digit** (0.5.**3**) — bug fixes and minor improvements; safe to update at any time;
- **middle digit** (0.**6**.0) — new sections and major changes; read the changelog before updating.

## Updating by the button (main way)

**Settings → Update** (administrators) shows the installed version, the latest release on GitHub with its changes,
and the buttons **Check**, **Update to X.Y.Z** and **Roll back to X.Y.Z**. go24 checks GitHub by itself every 6 hours;
a new version is shown on this page, it is not pushed as a notification.

**Update** does everything the manual steps below do:

1. go24 downloads `go24-X.Y.Z.tar.gz` and its signature `go24-X.Y.Z.tar.gz.sig` from the release and checks the
   signature (ed25519; the public key is built into go24, the private one is kept only by the publisher). An archive
   with a foreign or damaged signature, or one not newer than the installed version, is not installed.
2. The service `go24-updater` takes over and writes every step to the page, live:
   - checks the signature again (it does not trust go24);
   - dumps all databases to `deploy/backups/before-<old version>-<date>.sql.gz` and keeps the current files, `deploy/.env`
     and the go24 images for a rollback;
   - lays the new files over the installation; `deploy/.env`, the theme, your locales, the messenger's generated
     configuration and the backups are never touched;
   - appends the parameters that appeared in the new `deploy/.env.example` to `deploy/.env` (empty passwords and
     secrets are generated) and lists them in the log;
   - rebuilds the messenger configuration if its templates changed (as `make matrix-config`);
   - `docker compose pull` and `up -d --build`: new versions of the components come with the pinned image tags of the
     release;
   - waits up to 10 minutes for `go24` and `go24-worker` to become healthy and for `/healthz` to answer.
3. If go24 did not come up, the previous version is put back by itself: its files, settings and images, and the
   database from the dump; the status says **rolled back** with the reason.
4. The administrators get a notification (and a message in the messenger, by their settings): «updated to X.Y.Z» or
   «rolled back: reason».

Updating from any older version straight to the latest one works: the migrations of all versions in between are
applied in order.

**Roll back** puts back the version kept before the last update, together with its database. Everything changed in
go24 since that update is lost, so it is a last resort.

A development build (version with a suffix such as `0.6.0-dev`, or `GO24_UPDATE=off` in `deploy/.env`) has no
update button: its code is not from GitHub releases and is updated with git.

## Updating manually (fallback)

Use this when the button is unavailable (no access to GitHub from the server, `go24-updater` is not running).

### Overview

```
  1. backup  ──►  2. new code    ──►  3. new settings?  ──►  4. restart  ──►  5. verify
  (2 min)         (git checkout)      (usually none)          (make up)        (open go24)
```

The database is updated automatically: on startup go24 applies any new migrations. Data, files, and settings are
preserved.

### Steps

All commands are run on the server as the `go24` user in `/srv/go24`.

**1. Backup before updating.** On a test server the easiest option is a virtual-machine snapshot. If snapshots are
not available, dump all databases:

```bash
cd /srv/go24
mkdir -p deploy/backups
docker exec go24-postgres-1 pg_dumpall -U postgres | gzip > deploy/backups/before-$(cat VERSION)-$(date +%F).sql.gz
```

A backup of the database and files can also be made from within go24: **Settings → Backup → Back up now**.

**2. New code.**

```bash
git fetch --tags
git checkout $(git tag --sort=-v:refname | head -1)   # latest version; or a specific one: git checkout v0.5.3
cat VERSION
```

**3. New settings.** Shows parameters that appeared in the new version but are not yet in your `deploy/.env`:

```bash
comm -13 <(grep -o '^[A-Z0-9_]*=' deploy/.env | sort) <(grep -o '^[A-Z0-9_]*=' deploy/.env.example | sort)
```

- Empty output — proceed to step 4.
- Lines are shown — find them in `deploy/.env.example` (each has a comment next to it) and append them to
  `deploy/.env`. If the parameter is a password or secret and the example value is empty, generate one with
  `openssl rand -hex 32`.

If the changelog for this version mentions the messenger (Matrix), rebuild its configuration: `make matrix-config`.

**4. Restart.**

```bash
make up
```

A new go24 image is built and, if necessary, newer versions of other services are downloaded. This usually takes
3–10 minutes; go24 is unavailable for about a minute at the end.

**5. Verify.**

```bash
make ps                  # all services running/healthy
make logs s=go24         # no errors at startup (Ctrl+C to exit)
```

Open go24 in a browser and sign in.

## If something breaks after the update

After an update by the button: **Settings → Update → Roll back** puts back the previous version with its database
(the log of the update is on the same page, and `make logs s=go24-updater`). After a manual update:

1. Check the error: `make logs s=go24` and `make logs s=go24-worker`.
2. Roll back the code to the previous version: `git checkout v<previous version>` and `make up`.
3. If the new version already migrated the database and the previous version no longer starts, restore the
   virtual-machine snapshot from step 1.

Restoring a database from a `pg_dumpall` dump manually is a delicate operation. If you need to do it, ask the
developers rather than guessing.
