-- Возврат меток в метаданные. Значения не восстанавливаются: колонка создаётся пустой,
-- потому что после переезда метки жили во входных данных Excel, а не в ответе модели.

ALTER TABLE article_metadata
    ADD COLUMN tags TEXT;

ALTER TABLE article_inputs
    DROP COLUMN tags;
