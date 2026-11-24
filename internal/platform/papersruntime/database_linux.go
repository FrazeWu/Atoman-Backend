//go:build linux

package papersruntime

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	workerRole   = "atoman_papers_worker"
	launcherRole = "atoman_papers_launcher_ro"
)

var readableTables = map[string]bool{"paper_compile_jobs": true, "paper_compile_slots": true, "paper_projects": true, "paper_access_grants": true}
var workerUpdates = map[string]map[string]bool{
	"paper_compile_jobs":  {"status": true, "attempts": true, "started_at": true, "finished_at": true, "sandbox_id": true, "worker_id": true, "lease_token": true, "lease_epoch": true, "updated_at": true, "cancel_requested": true, "failure_code": true, "pdf_digest": true, "pdf_size": true},
	"paper_compile_slots": {"job_id": true, "worker_id": true, "lease_token": true, "epoch": true, "lease_expires_at": true, "updated_at": true, "blocked": true, "block_reason": true},
}

func queueDB(ctx context.Context, password string, readOnly bool) (*gorm.DB, error) {
	if !validDBPassword(password) {
		return nil, ErrConfig
	}
	role := workerRole
	if readOnly {
		role = launcherRole
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(role, password), Host: "127.0.0.1:5432", Path: "/atoman_prod"}
	q := u.Query()
	q.Set("sslmode", "disable")
	q.Set("connect_timeout", "5")
	q.Set("search_path", "public")
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return nil, ErrConfig
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, ErrConfig
	}
	sqlDB.SetMaxOpenConns(3)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	if err = sqlDB.PingContext(ctx); err == nil {
		err = verifyDatabaseRole(ctx, db, role, "atoman_prod", readOnly)
	}
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func verifyDatabaseRole(ctx context.Context, db *gorm.DB, role, database string, readOnly bool) error {
	var identity struct {
		Role      string
		Database  string
		Readonly  string
		Elevated  bool
		CanCreate bool
		Member    bool
	}
	err := db.WithContext(ctx).Raw(`
 SELECT current_user AS role, current_database() AS database,
 current_setting('default_transaction_read_only') AS readonly,
 (rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls) AS elevated,
 (has_database_privilege(current_user,current_database(),'CREATE') OR has_schema_privilege(current_user,'public','CREATE')) AS can_create,
 EXISTS(SELECT 1 FROM pg_auth_members WHERE member=r.oid) AS member
 FROM pg_roles r WHERE rolname=current_user
 `).Scan(&identity).Error
	if err != nil || identity.Role != role || identity.Database != database || identity.Elevated || identity.CanCreate || identity.Member ||
		(readOnly && identity.Readonly != "on") || (!readOnly && identity.Readonly != "off") {
		return fmt.Errorf("papers runtime: database role identity or privileges rejected")
	}
	var rows []struct {
		TableName    string
		ColumnName   string
		CanSelect    bool
		CanUpdate    bool
		CanInsert    bool
		CanDelete    bool
		CanTruncate  bool
		CanTrigger   bool
		CanReference bool
	}
	err = db.WithContext(ctx).Raw(`
 SELECT c.relname AS table_name,a.attname AS column_name,
 has_column_privilege(current_user,c.oid,a.attnum,'SELECT') AS can_select,
 has_column_privilege(current_user,c.oid,a.attnum,'UPDATE') AS can_update,
 has_column_privilege(current_user,c.oid,a.attnum,'INSERT') AS can_insert,
 has_table_privilege(current_user,c.oid,'DELETE') AS can_delete,
 has_table_privilege(current_user,c.oid,'TRUNCATE') AS can_truncate,
 has_table_privilege(current_user,c.oid,'TRIGGER') AS can_trigger,
 has_column_privilege(current_user,c.oid,a.attnum,'REFERENCES') AS can_reference
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_attribute a ON a.attrelid=c.oid
 WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f')
 AND a.attnum>0 AND NOT a.attisdropped
 `).Scan(&rows).Error
	if err != nil {
		return ErrConfig
	}
	seen := map[string]bool{}
	updates := map[string]map[string]bool{}
	for _, r := range rows {
		wantSelect := readableTables[r.TableName]
		wantUpdate := !readOnly && workerUpdates[r.TableName][r.ColumnName]
		if r.CanSelect != wantSelect || r.CanUpdate != wantUpdate || r.CanInsert || r.CanDelete || r.CanTruncate || r.CanTrigger || r.CanReference {
			return fmt.Errorf("papers runtime: unexpected database permissions")
		}
		if wantSelect {
			seen[r.TableName] = true
		}
		if wantUpdate {
			if updates[r.TableName] == nil {
				updates[r.TableName] = map[string]bool{}
			}
			updates[r.TableName][r.ColumnName] = true
		}
	}
	if len(seen) != len(readableTables) {
		return fmt.Errorf("papers runtime: compile database schema not ready")
	}
	if !readOnly {
		for table, cols := range workerUpdates {
			if len(updates[table]) != len(cols) {
				return ErrConfig
			}
		}
	}
	var count int64
	if err = db.WithContext(ctx).Raw("SELECT COUNT(*) FROM public.paper_compile_slots WHERE id=1").Scan(&count).Error; err != nil || count != 1 {
		return fmt.Errorf("papers runtime: compile slot not initialized")
	}
	return nil
}
