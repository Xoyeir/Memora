-- Formation 的执行归属与租约。
--
-- 目的：让 formation_status=running 不再是一个「进程崩溃就永久卡死」的终态，
-- 同时保证被接管的旧执行者不能再提交自己的结果。
--
-- formation_owner       本次执行的随机标识。执行者只在仍然持有它时才能写结果；
--                       提交成功或标记失败后立刻清空。
-- formation_started_at  本次认领的时刻，用来判断 running 是否已经过期。
--                       running 超过租约时长仍然没有结果，才允许被重新认领。
--
-- 纯增量，不动已有列，也不回填历史行（历史行 formation_status='none'，
-- 既不参与认领也不会被接管）。

ALTER TABLE chat_messages
  ADD COLUMN formation_owner varchar(64) NULL AFTER formation_status,
  ADD COLUMN formation_started_at datetime NULL AFTER formation_owner,
  ADD KEY idx_chat_formation_lease (formation_status, formation_started_at);
