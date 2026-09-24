-- Executed once by the gvenzl/oracle-free entrypoint (as SYSDBA, in the CDB
-- root) after APP_USER has been created in FREEPDB1.
-- Grants the app user what dbtest needs to create an isolated schema (user)
-- per test and hand it the privileges a migration test needs.
ALTER SESSION SET CONTAINER = FREEPDB1;

GRANT CREATE USER, ALTER USER, DROP USER TO gormgate;
GRANT CREATE SESSION TO gormgate WITH ADMIN OPTION;
GRANT CREATE TABLE, CREATE VIEW, CREATE SEQUENCE, CREATE PROCEDURE,
      CREATE TRIGGER, CREATE SYNONYM, CREATE TYPE, CREATE MATERIALIZED VIEW
      TO gormgate WITH ADMIN OPTION;
GRANT UNLIMITED TABLESPACE TO gormgate WITH ADMIN OPTION;
GRANT SELECT ON sys.v_$session TO gormgate;
GRANT ALTER SYSTEM TO gormgate;
