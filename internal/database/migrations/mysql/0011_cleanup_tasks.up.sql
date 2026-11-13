CREATE TABLE IF NOT EXISTS cleanup_tasks(
    id INT(11) NOT NULL AUTO_INCREMENT,
    bookmark_id INT(11) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY cleanup_tasks_bookmark_id_UNIQUE (bookmark_id)
) CHARACTER SET utf8mb4;
