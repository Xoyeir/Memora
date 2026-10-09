ALTER TABLE chat_messages
  ADD COLUMN status varchar(16) NOT NULL DEFAULT 'ok' AFTER content;

UPDATE chat_messages SET status = 'pending'
WHERE role = 'user'
  AND NOT EXISTS (
    SELECT 1 FROM (SELECT id, role FROM chat_messages) a
    WHERE a.role = 'assistant'
      AND a.id > chat_messages.id
      AND a.id < IFNULL(
        (SELECT MIN(b.id) FROM (SELECT id, role FROM chat_messages) b
         WHERE b.role = 'user' AND b.id > chat_messages.id),
        18446744073709551615)
  );
