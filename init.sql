CREATE DATABASE IF NOT EXISTS dist;
USE dist;

CREATE TABLE IF NOT EXISTS counter (
    id INT AUTO_INCREMENT PRIMARY KEY,
    count INT NOT NULL,
    last_updated_by VARCHAR(50),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO counter (count, last_updated_by) 
VALUES (0, 'init')
ON DUPLICATE KEY UPDATE count = count;