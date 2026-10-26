-- Business processes in the live feed (GO24DEV-390): a standalone process started by hand is a post of
-- its starter, seen by them and everyone the process gives a task to; the standard templates (Отпуск, Командировка, Счёт
-- на оплату, Заявление на выплату) are installed once each, std says which one a template is.
ALTER TABLE bp_templates ADD COLUMN std text;
CREATE UNIQUE INDEX bp_templates_std_idx ON bp_templates (std) WHERE std IS NOT NULL;
ALTER TABLE feed_posts ADD COLUMN bp_instance uuid REFERENCES bp_instances(id) ON DELETE SET NULL;
CREATE INDEX feed_posts_bp_idx ON feed_posts (bp_instance) WHERE bp_instance IS NOT NULL;
