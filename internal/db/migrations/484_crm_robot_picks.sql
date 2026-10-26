-- The robot «Сменить ответственного» giving cards in turn: whom each such robot gave a card last time.
CREATE TABLE crm_robot_picks (
    robot_id uuid PRIMARY KEY REFERENCES crm_robots(id) ON DELETE CASCADE,
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE
);
