CREATE OR REPLACE FUNCTION app_audit_row()
RETURNS trigger
LANGUAGE plpgsql
AS $audit$
DECLARE
    actor_id text := NULLIF(current_setting('app.actor_id', true), '');
BEGIN
    IF actor_id IS NULL THEN
        RAISE EXCEPTION 'app.actor_id must be set for audited writes';
    END IF;

    IF TG_OP = 'INSERT' THEN
        NEW.created_at := statement_timestamp();
        NEW.updated_at := NEW.created_at;
        NEW.created_by := actor_id;
        NEW.updated_by := actor_id;
        NEW.version := 1;
        NEW.deleted_at := NULL;
        NEW.deleted_by := NULL;
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        NEW.created_at := OLD.created_at;
        NEW.created_by := OLD.created_by;
        NEW.updated_at := statement_timestamp();
        NEW.updated_by := actor_id;
        NEW.version := OLD.version + 1;
        IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL THEN
            NEW.deleted_at := statement_timestamp();
            NEW.deleted_by := actor_id;
        ELSIF OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL THEN
            NEW.deleted_by := NULL;
        ELSE
            NEW.deleted_at := OLD.deleted_at;
            NEW.deleted_by := OLD.deleted_by;
        END IF;
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'physical DELETE is forbidden on audited table %, use deleted_at', TG_TABLE_NAME;
END;
$audit$;
CREATE OR REPLACE FUNCTION app_enable_audit(target regclass)
RETURNS void
LANGUAGE plpgsql
AS $audit$
BEGIN
    EXECUTE format('DROP TRIGGER IF EXISTS app_audit_row_trigger ON %s', target);
    EXECUTE format(
        'CREATE TRIGGER app_audit_row_trigger BEFORE INSERT OR UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION app_audit_row()',
        target
    );
END;
$audit$;
