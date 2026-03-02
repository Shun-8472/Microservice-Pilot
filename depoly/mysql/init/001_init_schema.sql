CREATE DATABASE IF NOT EXISTS schedule_events;
USE schedule_events;

CREATE TABLE IF NOT EXISTS calendar_events (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  title VARCHAR(255) NOT NULL,
  start_time DATETIME NOT NULL,
  end_time DATETIME NOT NULL,
  source_input TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_event_time (start_time, end_time)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
