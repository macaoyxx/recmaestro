-- 000001_init.down.sql — 回滚初始建表（逆序删除）

DROP TABLE IF EXISTS `live_rec_audit_logs`;
DROP TABLE IF EXISTS `live_rec_nodes`;
DROP TABLE IF EXISTS `live_rec_task_logs`;
DROP TABLE IF EXISTS `live_rec_task_files`;
DROP TABLE IF EXISTS `live_rec_tasks`;
DROP TABLE IF EXISTS `live_rec_users`;
