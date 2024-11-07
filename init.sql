CREATE DATABASE IF NOT EXISTS dist_lock;
USE dist_lock;

CREATE TABLE IF NOT EXISTS counter (
    id INT PRIMARY KEY,
    value INT
);

INSERT INTO counter (id, value) VALUES (1, 0)
    ON DUPLICATE KEY UPDATE value = value;