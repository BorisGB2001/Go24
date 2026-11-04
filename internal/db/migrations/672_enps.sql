-- «eNPS» (GO24DEV-852): an anonymous survey «would you recommend the company as a place to work», 0–10.
-- The audience is fixed when the survey is made; an answer keeps no person and no time — only the score, the comment
-- and the respondent's departments (for the breakdown, shown from enough answers), while enps_audience knows only
-- who has answered.
CREATE TABLE enps_surveys (
    id          bigserial PRIMARY KEY,
    question    text NOT NULL CHECK (length(question) BETWEEN 1 AND 300),
    starts_on   date NOT NULL,
    ends_on     date NOT NULL CHECK (ends_on >= starts_on),
    departments uuid[] NOT NULL DEFAULT '{}', -- with sub-departments; '{}' — the whole company
    closed_at   timestamptz,                  -- finished before ends_on
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE enps_audience (
    survey_id bigint NOT NULL REFERENCES enps_surveys(id) ON DELETE CASCADE,
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    answered  boolean NOT NULL DEFAULT false,
    PRIMARY KEY (survey_id, user_id)
);
CREATE INDEX enps_audience_user ON enps_audience (user_id) WHERE NOT answered;
CREATE TABLE enps_answers (
    id          bigserial PRIMARY KEY,
    survey_id   bigint NOT NULL REFERENCES enps_surveys(id) ON DELETE CASCADE,
    score       smallint NOT NULL CHECK (score BETWEEN 0 AND 10),
    comment     text NOT NULL DEFAULT '' CHECK (length(comment) <= 1000),
    departments uuid[] NOT NULL DEFAULT '{}'
);
CREATE INDEX enps_answers_survey ON enps_answers (survey_id);
