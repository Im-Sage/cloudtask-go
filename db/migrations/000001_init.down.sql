BEGIN;

-- 先删除引用别人的表，再删除被引用的表
DROP TABLE task_events;
DROP TABLE tasks;

COMMIT;