-- 000001_init.up.sql — 初始建表（RecMaestro 全量表，前缀 live_rec_）

CREATE TABLE IF NOT EXISTS `live_rec_users` (
  `id`                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `username`          VARCHAR(64)     NOT NULL,
  `password_hash`     VARCHAR(255)    NOT NULL,
  `role`              VARCHAR(16)     NOT NULL DEFAULT 'user'    COMMENT 'admin | user',
  `status`            VARCHAR(16)     NOT NULL DEFAULT 'enabled' COMMENT 'enabled | disabled',
  `failed_login_count` INT            NOT NULL DEFAULT 0,
  `last_login_at`     DATETIME        NULL,
  `created_at`        DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`        DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_username` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `live_rec_tasks` (
  `id`                 VARCHAR(64)     NOT NULL,
  `name`               VARCHAR(255)    NOT NULL DEFAULT '',
  `created_by`         BIGINT UNSIGNED NULL,
  `stream_url`         TEXT            NOT NULL,
  `start_time`         DATETIME        NULL,
  `end_time`           DATETIME        NULL,
  `actual_start_time`  DATETIME        NULL,
  `actual_end_time`    DATETIME        NULL,
  `status`             VARCHAR(32)     NOT NULL DEFAULT 'pending',
  `worker_id`          VARCHAR(128)    NULL,
  `priority`           INT             NOT NULL DEFAULT 0,
  `segment_seconds`    INT             NOT NULL DEFAULT 300,
  `max_retry`          INT             NOT NULL DEFAULT 3,
  `retry_count`        INT             NOT NULL DEFAULT 0,
  `output_path`        VARCHAR(512)    NULL,
  `error_code`         VARCHAR(64)     NULL,
  `error_msg`          TEXT            NULL,
  `last_heartbeat_at`  DATETIME        NULL,
  `created_at`         DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`         DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_status_start_time` (`status`, `start_time`),
  KEY `idx_worker_id` (`worker_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `live_rec_task_files` (
  `id`          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `task_id`     VARCHAR(64)     NOT NULL,
  `node_id`     VARCHAR(128)    NULL,
  `filename`    VARCHAR(255)    NOT NULL,
  `type`        VARCHAR(16)     NOT NULL DEFAULT 'segment' COMMENT 'segment | output',
  `path`        VARCHAR(512)    NOT NULL,
  `seq`         INT             NOT NULL DEFAULT 0,
  `size_bytes`  BIGINT          NOT NULL DEFAULT 0,
  `duration_ms` BIGINT          NOT NULL DEFAULT 0,
  `file_hash`   VARCHAR(128)    NULL,
  `status`      VARCHAR(16)     NOT NULL DEFAULT 'ok' COMMENT 'ok | partial | corrupt',
  `created_at`  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `deleted_at`  DATETIME        NULL,
  PRIMARY KEY (`id`),
  KEY `idx_task_seq` (`task_id`, `seq`),
  KEY `idx_node_id` (`node_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `live_rec_task_logs` (
  `id`        BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `task_id`   VARCHAR(64)     NOT NULL,
  `ts`        DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `level`     VARCHAR(16)     NOT NULL DEFAULT 'info' COMMENT 'info | warn | error',
  `source`    VARCHAR(32)     NOT NULL DEFAULT 'worker' COMMENT 'scheduler | worker | ffmpeg-record | ffmpeg-merge',
  `stage`     VARCHAR(32)     NOT NULL DEFAULT '',
  `message`   TEXT            NOT NULL,
  `exit_code` INT             NULL,
  `node_id`   VARCHAR(128)    NULL,
  PRIMARY KEY (`id`),
  KEY `idx_task_ts` (`task_id`, `ts`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `live_rec_nodes` (
  `id`                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `node_id`           VARCHAR(128)    NOT NULL,
  `host`              VARCHAR(255)    NULL,
  `ip`                VARCHAR(64)     NULL,
  `status`            VARCHAR(16)     NOT NULL DEFAULT 'offline' COMMENT 'online | offline',
  `disk_total`        BIGINT          NOT NULL DEFAULT 0,
  `disk_free`         BIGINT          NOT NULL DEFAULT 0,
  `cpu`               DOUBLE          NOT NULL DEFAULT 0,
  `mem`               DOUBLE          NOT NULL DEFAULT 0,
  `ffmpeg_version`    VARCHAR(64)     NULL,
  `capacity`          INT             NOT NULL DEFAULT 0,
  `current_tasks`     INT             NOT NULL DEFAULT 0,
  `last_heartbeat_at` DATETIME        NULL,
  `registered_at`     DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_node_id` (`node_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `live_rec_audit_logs` (
  `id`          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`     BIGINT UNSIGNED NULL,
  `action`      VARCHAR(64)     NOT NULL,
  `resource`    VARCHAR(64)     NOT NULL DEFAULT '',
  `resource_id` VARCHAR(64)     NOT NULL DEFAULT '',
  `detail`      TEXT            NULL,
  `ip`          VARCHAR(64)     NULL,
  `user_agent`  VARCHAR(512)    NULL,
  `created_at`  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_created_at` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
