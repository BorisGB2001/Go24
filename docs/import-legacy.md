Русская версия: [ru/import-legacy.md](ru/import-legacy.md)

# Importing Tasks (On-Premise)

The import transfers tasks, subtasks, participants (co-executors), observers, and comments from MySQL of an on-premise the portal instance into a go24 project. Personal data is anonymized:

| What | How |
|---|---|
| Employee full names | Random full names of the same gender, consistent for each person everywhere. Also substituted in text, with declensions: "Иванову" → "Белову" |
| Mentions `[USER=17]…[/USER]` | `@Pseudonym` of that user |
| Phone numbers | Random digits preserving the format (`+7 (9xx) …`, `8-9xx-…`, local `xx-xx-xx`) |
| Email | Employees — `user<ID>@portal.import`; other addresses — `contact<hash>@<same-domain>` |
| Comments without author (emails, guests) | Shared user "Внешний автор", sender name is not transferred |
| Companies, task texts | Preserved as is |

**Limitation.** Names of external people (clients) who are not the portal users are not recognized or replaced in text. Phone numbers and emails are replaced for everyone.

The task number is preserved: task 123 becomes `EXT-123`. The import runs in a single transaction and can be rerun without duplicates: records are matched by `legacy_id`. Legacy forum service messages (`SERVICE_TYPE`) are not transferred.

## Procedure

1. **Export on the Legacy server** (MySQL, `sitemanager` database). Texts are base64-encoded, directory is accessible only to root:

```bash
D=/root/go24-export; install -d -m 700 $D; cd $D
B64='REPLACE(TO_BASE64(IFNULL(%s,"")),"\n","")'   # template for text columns
mysql sitemanager -N -B -e 'select ID, <base64 NAME>, <base64 LAST_NAME>, <base64 SECOND_NAME>, IFNULL(PERSONAL_GENDER,""), <base64 EMAIL>, <base64 LOGIN> from b_user where ID in (select USER_ID from b_tasks_member union select AUTHOR_ID from b_forum_message where XML_ID like "TASK_%" and SERVICE_TYPE is null)' > users.tsv
mysql sitemanager -N -B -e 'select ID, <base64 TITLE>, <base64 DESCRIPTION>, STATUS, PRIORITY, CREATED_BY, RESPONSIBLE_ID, IFNULL(PARENT_ID,0), TASK_CONTROL, IFNULL(DEADLINE,""), IFNULL(START_DATE_PLAN,""), IFNULL(END_DATE_PLAN,""), CREATED_DATE, IFNULL(CHANGED_DATE,""), IFNULL(CLOSED_DATE,""), IFNULL(CLOSED_BY,0) from b_tasks where ZOMBIE="N" order by ID' > tasks.tsv
mysql sitemanager -N -B -e 'select TASK_ID, USER_ID, TYPE from b_tasks_member' > members.tsv
mysql sitemanager -N -B -e 'select m.ID, cast(substring(m.XML_ID,6) as unsigned), m.AUTHOR_ID, m.POST_DATE, <base64 POST_MESSAGE> from b_forum_message m where m.XML_ID like "TASK_%" and m.SERVICE_TYPE is null order by m.ID' > comments.tsv
chmod 600 *.tsv
```

`<base64 X>` means `REPLACE(TO_BASE64(IFNULL(X,"")),"\n","")`.

2. **Transfer** files to `/srv/go24-import/` on the go24 server (permissions 700, outside codebase — `make sync` does not touch this directory).
3. **Import**: `make import-legacy`. On the first run, a random anonymization seed is created in `/srv/go24-import/seed`. Save it: with it, repeated imports will produce the same pseudonyms.
4. **Delete original export files** everywhere (`users.tsv`, `tasks.tsv`, `members.tsv`, `comments.tsv`), leaving only `seed`.

The `EXT` project is created with its owner set to the first user who logged into go24 via Authentik (or `-owner <email>`). All imported employees are added as members. Logging in as imported users is not possible: their `oidc_sub = legacy:<ID>`.

## Users in Authentik and first login

Imported employees are also created in Authentik via the `make authentik-users` command. It writes the blueprint `deploy/authentik/blueprints/generated/legacy-users.yaml` from the go24 database (pseudonyms only), which Authentik applies automatically. Users are located in the `go24/portal` section, their usernames are `portal-user<ID>`, and email addresses are `user<ID>@portal.import`. They have no password: an administrator sets it in Authentik or sends a password recovery link.

Upon first login, go24 finds the imported record with the matching email and links it to the Authentik account. All tasks and comments of this employee become theirs; no new user is created. When employees are given real accounts, simply set their real email in Authentik and update it in go24 at the same time — or log in under the pseudonym and update the details in Authentik.

## Attachments

`make import-legacy-files` transfers attachments of tasks and comments as **empty**: the anonymized name, type, and task association are preserved, but the file content is not copied. Reason: documents (spreadsheets, PDFs, scans) cannot be anonymized. Attachments from comments are attached to the task. For empty files, no object is created in storage; downloading returns an empty file.

Export — `users.tsv` (as above) and `files.tsv`:

```
ATTACHED_ID, TASK_ID, <base64 NAME>, CONTENT_TYPE, CREATED_BY, task|comment, CREATE_TIME
```

Rows are taken from `b_utm_tasks_task` (field `UF_TASK_WEBDAV_FILES`) → `b_disk_attached_object` → `b_disk_object`, and for comments — from `b_disk_attached_object` with `ENTITY_TYPE` `ForumMessageConnector` → `b_forum_message.XML_ID = TASK_<id>`.
