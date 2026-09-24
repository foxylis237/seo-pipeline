-- Откат 000002: числа программы.
--
-- После отката объём, срок, стоимость, документ и аттестацию снова выводит промпт из вида
-- программы диапазонами — поведение до миграции. Значения колонок теряются безвозвратно и
-- восстанавливаются повторным импортом из книги.

ALTER TABLE article_inputs
    DROP COLUMN IF EXISTS hours,
    DROP COLUMN IF EXISTS duration,
    DROP COLUMN IF EXISTS price,
    DROP COLUMN IF EXISTS document,
    DROP COLUMN IF EXISTS attestation;
