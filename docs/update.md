Русская версия: [ru/update.md](ru/update.md)

# Updating go24

New versions are published on GitHub: https://github.com/BorisGB2001/Go24/releases. Each release has a version number
`X.Y.Z` and a changelog. The numbering works as follows:

- **last digit** (0.5.**3**) — bug fixes and minor improvements; safe to update at any time;
- **middle digit** (0.**6**.0) — new sections and major changes; read the changelog before updating.

## Overview

```
  1. backup  ──►  2. new code    ──►  3. new settings?  ──►  4. restart  ──►  5. verify
  (2 min)         (git checkout)      (usually none)          (make up)        (open go24)
```

The database is updated automatically: on startup go24 applies any new migrations. Data, files, and settings are
preserved.

## Steps

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

1. Check the error: `make logs s=go24` and `make logs s=go24-worker`.
2. Roll back the code to the previous version: `git checkout v<previous version>` and `make up`.
3. If the new version already migrated the database and the previous version no longer starts, restore the
   virtual-machine snapshot from step 1.

Restoring a database from a `pg_dumpall` dump manually is a delicate operation. If you need to do it, ask the
developers rather than guessing.
