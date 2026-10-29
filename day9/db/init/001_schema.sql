CREATE TABLE IF NOT EXISTS tasks (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title VARCHAR(200) NOT NULL,
    description TEXT NOT NULL DEFAULT '', 
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    priority smallint NOT NULL DEFAULT 3, -- 优先级，越大越优先
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT task_title__not_blank
        CHECK (length(btrim(title)) > 0), -- 不能为空字符串

    CONSTRAINT task_status_valid
        CHECK (status IN ('pending', 'in_progress', 'completed')), -- 只能是三种状态之一

    CONSTRAINT task_priority_valid
        CHECK (priority >= 1 AND priority <= 5) -- 优先级范围为1-5    
);

CREATE TABLE IF NOT EXISTS task_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    task_id BIGINT NOT NULL
        REFERENCES tasks(id) ON DELETE CASCADE,
    event_type VARCHAR(50) NOT NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT task_event_type_not_blank
        CHECK (length(btrim(event_type)) > 0) -- 不能为空字符串
);

-- 先按status查找，在相同的status下按created_at倒序查找
CREATE INDEX IF NOT EXISTS idx_task_status_created_at
    ON tasks (status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_task_events_task_id_created_at
    ON task_events (task_id, created_at DESC);
