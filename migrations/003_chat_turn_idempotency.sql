-- 聊天轮次的幂等与状态。
--
-- client_id          客户端为「一次发送」生成的稳定标识；重试复用同一个值。
--                    仅 user 行有值，assistant 行为 NULL（MySQL UNIQUE 允许多个 NULL）。
-- reply_to_id        assistant 行指向它回复的 user 行，保证「一轮只有一条回复」。
-- formation_status   记忆写入的完成状态，与 status（回复的完成状态）分开记录。
--                    none    历史行与 assistant 行，Formation 不再（重复）执行
--                    pending 新建的 user 行，尚未尝试
--                    running 已被某次请求认领
--                    applied 已执行完毕（含「无任何 action」）
--                    failed  模型调用失败，可再次尝试
-- updated_at         formation_status 与 status 的流转时间，用于观测。
--
-- 全部为可空列或带默认值，历史行不需要回填，迁移是纯增量的。

ALTER TABLE chat_messages
  ADD COLUMN client_id varchar(64) NULL AFTER content,
  ADD COLUMN reply_to_id bigint unsigned NULL AFTER client_id,
  ADD COLUMN formation_status varchar(16) NOT NULL DEFAULT 'none' AFTER status,
  ADD COLUMN updated_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  ADD UNIQUE KEY uk_chat_client_id (client_id),
  ADD UNIQUE KEY uk_chat_reply_to (reply_to_id);
