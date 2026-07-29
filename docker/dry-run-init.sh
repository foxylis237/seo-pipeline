#!/bin/sh
set -eu

for migration in /migrations/*.up.sql; do
  psql --set ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --file "$migration"
done
