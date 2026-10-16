-- 000002_audit_names.down.sql — 回滚审计名称快照列

ALTER TABLE `live_rec_audit_logs`
  DROP COLUMN `operator_name`,
  DROP COLUMN `resource_name`;
