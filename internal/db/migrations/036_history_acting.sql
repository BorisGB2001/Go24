-- A change made by someone acting for an absent person («исполняющий обязанности») is signed «(ИО за …)».
ALTER TABLE task_history ADD COLUMN acting_for uuid REFERENCES users(id) ON DELETE SET NULL;

CREATE FUNCTION go24_history_acting() RETURNS trigger AS $$
BEGIN
    IF NEW.acting_for IS NULL AND NOT EXISTS (
        SELECT 1 FROM tasks t WHERE t.id = NEW.task_id AND NEW.actor_id IN (t.creator_id, t.responsible_id)
        UNION ALL SELECT 1 FROM task_participants p WHERE p.task_id = NEW.task_id AND p.user_id = NEW.actor_id) THEN
        SELECT a.user_id INTO NEW.acting_for FROM absences a JOIN tasks t ON t.id = NEW.task_id
        WHERE a.acting_id = NEW.actor_id AND current_date BETWEEN a.starts_on AND a.ends_on
          AND (a.user_id IN (t.creator_id, t.responsible_id)
               OR EXISTS (SELECT 1 FROM task_participants p WHERE p.task_id = t.id AND p.user_id = a.user_id))
        LIMIT 1;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER task_history_acting BEFORE INSERT ON task_history FOR EACH ROW EXECUTE FUNCTION go24_history_acting();
