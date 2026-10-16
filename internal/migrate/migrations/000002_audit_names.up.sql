-- 000002_audit_names.up.sql — 审计日志增加名称快照列
-- 对象三元组：resource(类型枚举) + resource_id(对象ID) + resource_name(对象名称)。
-- resource_name 是唯一且通用的对象名称列（用户→用户名、任务→任务名，未来新类型复用，零加列）。
-- operator_name 为操作者名称快照（与 user_id 配对，回答"是谁干的"）。
-- 名称写入时随行落库：资源日后被删除，审计行仍能读出名称。

ALTER TABLE `live_rec_audit_logs`
  ADD COLUMN `operator_name` VARCHAR(64) NULL AFTER `user_id`,
  ADD COLUMN `resource_name` VARCHAR(255) NULL AFTER `resource_id`;

-- 历史行回填（关联不上的——资源已删除——保持 NULL，前端仅显示 ID）
UPDATE `live_rec_audit_logs` a JOIN `live_rec_users` u ON a.`user_id` = u.`id`
  SET a.`operator_name` = u.`username` WHERE a.`operator_name` IS NULL;
UPDATE `live_rec_audit_logs` a JOIN `live_rec_users` u
  ON a.`resource` = 'user' AND a.`resource_id` = CAST(u.`id` AS CHAR) COLLATE utf8mb4_unicode_ci
  SET a.`resource_name` = u.`username` WHERE a.`resource_name` IS NULL;
UPDATE `live_rec_audit_logs` a JOIN `live_rec_tasks` t
  ON a.`resource` = 'task' AND a.`resource_id` = t.`id`
  SET a.`resource_name` = t.`name` WHERE a.`resource_name` IS NULL;
