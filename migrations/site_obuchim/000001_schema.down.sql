-- Откат каталога услуг obuchim-specialista.ru. Схема сносится целиком: данные в ней —
-- зеркало площадки, и восстанавливаются повторным `make obuch-1 catalog-sync`, а не бэкапом.
-- Каталога соседней площадки (схема site) это не касается.
DROP SCHEMA IF EXISTS site_obuchim CASCADE;
