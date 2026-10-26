-- External services of calls (GO24DEV-499): Zoom, Яндекс Телемост and VK Звонки make a meeting by their API with the
-- company's keys; Telegram and Max have no API for calls — an employee keeps a link of their own. enabled — the
-- administrators let the service be used; config — what is not secret (Zoom's account and client, VK's community),
-- secret — the key sealed with go24's own secret (Zoom's client secret, the OAuth tokens of Телемост and VK).
CREATE TABLE call_services (
    service    text PRIMARY KEY CHECK (service IN ('zoom', 'telemost', 'vk', 'telegram', 'max')),
    enabled    boolean NOT NULL DEFAULT false,
    config     jsonb NOT NULL DEFAULT '{}',
    secret     bytea,
    updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- An employee's own link of a service: a personal room of Zoom or Телемост, a call of VK, t.me/… of Telegram,
-- max.ru/… of Max. Used when the service makes no meeting by its API.
CREATE TABLE user_call_links (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    service    text NOT NULL CHECK (service IN ('zoom', 'telemost', 'vk', 'telegram', 'max')),
    url        text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, service)
);
