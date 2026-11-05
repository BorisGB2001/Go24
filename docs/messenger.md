Русская версия: [ru/messenger.md](ru/messenger.md)

# go24 Messenger: Telegram, Matrix, and others — how to connect

User feature request (2026-10-12): messages from go24 are actually sent to the messenger; go24 groups = messenger
groups; task discussions are threads in the group; everything is closed to outsiders; dismissed employees lose access
automatically. Options: Telegram, Mattermost, Slack, Matrix.

## General scheme (one for all messengers)

go24 is the source of permissions. In code, there is a "messenger provider" interface; the administrator chooses the implementation:

| go24 | Matrix | Telegram | Mattermost / Slack |
|---|---|---|---|
| Company | Space | — | Team / Workspace |
| Project (group) | Room in Space | Supergroup with topics | Channel (private) |
| Task | Thread in project room (or own room) | **Topic** of project supergroup | Thread in channel |
| Direct chat | Direct room | Direct chat with bot / between people — outside go24 | DM |
| Project/task members | invite / kick by go24 bot | invite link for 1 person + ban | invite / remove |
| Dismissal | kick from all rooms + account deactivation | ban in all groups | deactivate |
| Sign in | SSO via Authentik (OIDC) | Telegram account, linked via bot | SSO via Authentik |
| Where messages are stored | **on our side** (self-hosted) | in Telegram cloud | on our side / in Slack cloud |

Synchronization is two-way: a message in the go24 task chat → to the messenger thread; a reply in the messenger →
to the go24 task chat (via bot webhook / application service).

## Matrix — recommended as primary

- Fully self-hosted server (Synapse or lightweight Conduit/conduwuit in Docker), Element clients — web, iOS, Android, desktop.
- Sign-in via Authentik (OIDC) — same accounts; dismissal = block in Authentik + kick by bot from all rooms → access is revoked immediately.
- go24 works as an **application service**: creates spaces, rooms, and threads itself, invites and removes people.
- Closed rooms (invite-only, without a public directory), optional E2EE encryption.
- Calls — LiveKit (see `docs/calls.md`), built-in voice messages. A chat's call is one call for go24 and Element (web, Element X): go24 is the room's MatrixRTC focus (GO24DEV-1024) — see «Element Call» in `docs/calls.md`.
- **Telegram bridge** — through the company bot («Notification settings» → Telegram, GO24DEV-1009): new chat messages reach an employee in Telegram while they are away from go24, and a reply to them (Reply) goes to the go24 chat on their behalf; a group chat is linked to a Telegram group (members → «Telegram group», the /link command in the group): the builtin bridge bot «Telegram» carries messages both ways, an employee with Telegram connected writes from the group as themselves; the correspondence stays on our side. A full bridge under a personal account (mautrix-telegram) — only if needed: it requires Telegram's api_id/api_hash.

## Telegram — as a second provider (with platform limitations)

- The bot **cannot create a group** (the Bot API does not support this). Therefore: the administrator creates a
  supergroup for a project once (or one for the whole company) with topics enabled and adds the go24 bot as an administrator.
  After that, the bot operates on its own: creates a topic for each task (`createForumTopic`), posts there, and closes the topic when the task is closed.
- The bot **cannot add a person** — it can only issue a personal invite link (limit: 1 join,
  with a join request that the bot approves only for employees). An employee links their
  Telegram to go24 once via the bot (`/start` with a one-time code).
- Dismissal: the bot bans the person in all company groups (`banChatMember`) — access is revoked immediately.
- Outsiders: group without a public link, joining only by request, which the bot checks against go24.
- Drawbacks: correspondence is stored in the Telegram cloud (not self-hosted); on mobile, this is the employee's personal account.

## Mattermost / Slack

- Mattermost: self-hosted, SSO via Authentik (GitLab/OIDC), channels and threads — straightforward integration via REST API.
- Slack: cloud, everything is done via the bot API; for a self-hosted product — an option for clients who already use Slack.

## Proposed order

1. Chat inside go24 (tasks already have it) + provider interface.
2. Matrix (own server in Docker, Authentik SSO, application service) + Telegram bridge.
3. Telegram bot as an independent provider (supergroups with topics).
4. Mattermost / Slack on demand.

## Where correspondence is stored (decision 2026-10-13)

**All correspondence is stored in Matrix**: task chats, feed (posts, comments, "Like"), direct and group chats.
go24 writes to Matrix first (on behalf of the author), and only then adds the record to its own database — this is an **index** for
fast listings, search, and permissions; it can be restored from Matrix. If Matrix is unavailable, the write does not succeed,
the user sees an error, and their text remains in the form.

- Task chat: task thread in the project room (or the task room for "members only" projects).
  Old task correspondence (prior to the messenger, imports) is migrated to Matrix upon the first opening of the thread — with original dates.
- Feed: "Company feed" (all employees) or project room; comments — post thread; "Like" — 👍.
- Messenger in go24 (/im) reads and writes to Matrix using the employee's own access — everyone sees strictly their own chats.
- Element — a separate client for the same messenger; messages written there appear in the appropriate place in go24 and vice versa.
