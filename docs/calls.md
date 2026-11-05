Русская версия: [ru/calls.md](ru/calls.md)

# Calls, voice messages, and telephony — choice of services

The principle is the same as with storage (rclone): **go24 is not tied to a single service**. There is a common interface in code
("calls provider", "telephony provider"), and the administrator chooses the implementation in settings:
cloud or self-hosted. Users and tasks are unaware of this.

## Three different tasks

| What | Example | How we handle it |
|---|---|---|
| Calls inside go24 (audio/video between employees, from task chat, group «Чаты и встречи») | call in messenger | **LiveKit** (recommended) or Jitsi |
| Voice messages in chats | microphone button in chat | **no external services needed**: in-browser recording (MediaRecorder, Opus) → file in go24 storage (rclone) |
| Telephony (landline/mobile numbers, incoming/outgoing, recording, caller CRM card) | Telephony (Voximplant + SIP) | **Asterisk** + SIP trunk of any operator, or cloud PBX via API |

## 1. Calls inside go24 — LiveKit

- Open-source server (Apache 2.0, Go), WebRTC SFU: groups, screen sharing, recording (Egress).
- **The exact same API** for the self-hosted server and LiveKit Cloud — choosing "cloud / self-hosted" is only a matter of the address and keys
  in go24 settings. Exactly what is needed.
- go24 issues tokens for joining the room itself (Go SDK) — permission check based on task/chat participants, sign-in via Authentik.
- Self-hosted server: a single container `livekit-server` (+ built-in TURN), deployed into our Swarm.
- Call recording (GO24DEV-454): container `livekit-egress` (Chrome inside records the room as a video grid or audio
  only) and `redis` (through which LiveKit dispatches tasks to Egress). Egress writes the file to the shared volume `call-recordings`
  (`GO24_RECORD_DIR`), go24-worker moves it to the Drive of whoever started the recording ("Call recordings"), and posts to the call's
  chat or task. Enabled by administrator: "Settings → Calls".
- Recording transcription (GO24DEV-798): under "Transcribe call recordings", the saved recording waits in the queue
  (`call_recordings.transcript = 'pending'`), go24-worker picks one per minute: for video, ffmpeg extracts audio
  (16 kHz, mono, Opus, ~10 MB per hour), Whisper (`output=json`) returns speech chunks with timestamps, text as a file beside the
  recording. If the worker crashes mid-transcription — up to three attempts (`transcript_tries`). Whisper does not distinguish speakers.
- Meeting summary (GO24DEV-838): under "Sum up meetings (CoPilot)", after transcription, go24-worker sends the text to the AI of whoever
  started the recording (`ai.Store.Write`, agent `calls`; if none configured — company connection) and requests JSON: `summary`,
  `agreements`, `tasks` (`title`, `who`, `due`), `next`. Summaries go to the call's chat or task (for a task — link
  `/tasks/new?title=…&parent=KEY`), as a “… — meeting summary.txt” file beside the recording, and into `call_recordings.summary`.
  Non-JSON response — forwarded as is; AI error — "Could not sum up the meeting".
- External services (GO24DEV-499): Zoom — POST /v2/users/{email|me}/meetings with a Server-to-Server OAuth token
  (account_credentials); Yandex Telemost — POST cloud-api.yandex.net/v1/telemost-api/conferences ("OAuth <token>",
  telemost-api:conferences.create); VK Calls — calls.start (VK ID user token, group_id from community) →
  join_link. Telegram (Bot API) and Max (Bot API) have no calls; a Telegram group call by link is only in
  client MTProto (phone.createConferenceCall) from a live account — not used, employees provide their own links.

Alternative: **Jitsi Meet** (Apache 2.0, has 8x8 JaaS cloud). Simpler "room by link", but heavier
(prosody + jvb + jicofo) and harder to embed into our own interface. We connect it as a second provider if needed.

Not recommended as a foundation: Matrix (Synapse + Element Call) — this is an entire messenger with its own data model and
federation; the rule "administrator cannot see direct chats" and our permissions are simpler to maintain in our own code.

### Element Call — the same call from Element (GO24DEV-1024)

A chat's call in go24 and the call of that room in Element (web, Element X) is one call in the same LiveKit room
(`im-…`, as go24 names it). No lk-jwt-service: go24 is the focus itself.

- Synapse names go24 in its MatrixRTC transports (`experimental_features.msc4143_enabled`, `matrix_rtc.transports`:
  `livekit_service_url` = `https://<go24>:8444/matrixrtc`); Element Call asks there `POST /matrixrtc/sfu/get` with the
  person's OpenID token (`/get_token` answers 404 — Element falls back). go24 checks the token at Synapse's `openid`
  listener (`/_matrix/federation/v1/openid/userinfo`), that the person is an active employee joined to the room, and
  gives the LiveKit token of the chat's call: identity `@user:server:DEVICE` (Element finds people's tracks so), the
  go24 name. Delegated leave events (`delay_id`) are declined with `M_BAD_JSON`: Element Call keeps them itself
  (`max_event_delay_duration`, MSC4140). The call's line and history in go24 count the person.
- go24's call page of a chat publishes the person's `org.matrix.msc3401.call.member` (device `GO24WEB`, go24 as the
  `oldest_membership` focus with the LiveKit alias) and joins LiveKit as `@user:server:GO24WEB`, so Element shows them;
  go24-worker empties the membership of those no longer in LiveKit (a minute's grace for connecting).
- Rooms let their writers send the call events (`org.matrix.msc3401.call`, `…call.member`) at the level of messages,
  as Element's «Enable calls» does: rooms go24 makes, and once at go24-worker start every room of the server it (the
  bot or an employee who may) can change.
- Element Web: `element_call.use_exclusively` — calls of rooms are Element Call's (embedded), on our LiveKit.

## 2. Voice messages

No separate service needed: browser records Opus/WebM, go24 saves it as a message attachment in the selected
storage (built-in, Nextcloud, S3…), chat displays a player. Speech transcription — later, optionally
(Whisper in its own container or cloud API — also by choice).

## 3. Telephony — Asterisk (as planned)

- Self-hosted: **Asterisk** (GPLv2) in a container, go24 manages it via **ARI** (REST + WebSocket):
  incoming call → popup card, click-to-call outbound, conversation recording in storage, call history.
- Numbers: SIP trunk of **any** carrier (cloud part is only the line). Changed in settings.
- Employees call from the browser (WebRTC → Asterisk) or from SIP phones/softphones.
- Second provider option: cloud PBX via API (Voximplant — used by the portal; Zadarma, Mango, etc.) —
  for those who do not want to run their own Asterisk.

## Order of work (proposal)

1. go24 messenger (direct and group chats; task chat already exists) + voice messages.
2. Calls via LiveKit (own server in Docker; cloud configured the same way).
3. Section "Telephony": Asterisk + ARI, then cloud provider.

In the left menu, the "Messenger" and "Telephony" sections will appear alongside the modules (the menu is already split into sections).
