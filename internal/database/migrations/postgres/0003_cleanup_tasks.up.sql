CREATE TABLE IF NOT EXISTS cleanup_tasks(
    id SERIAL,
    bookmark_id INTEGER NOT NULL,
    created_at TIMESTAMP(0) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    CONSTRAINT cleanup_tasks_bookmark_id_UNIQUE UNIQUE (bookmark_id)
);
