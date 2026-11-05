// mail_admin is the operator entry point for existing reservations. It never
// guesses an administrator, bypasses gates, or prints service credentials.
package main

import (
	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/modules/mail"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/google/uuid"
	"os"
	"time"
)

func fail(m string) { fmt.Fprintln(os.Stderr, m); os.Exit(1) }
func main() {
	op := flag.String("operation", "status", "status, provision or migrate-mail")
	target := flag.String("user", "", "target Atoman UUID")
	address := flag.String("address", "", "existing reserved mailbox address")
	actor := flag.String("actor", "", "existing Atoman administrator UUID (required for provision)")
	flag.Parse()
	if os.Getenv("DATABASE_URL") == "" || os.Getenv("DATABASE_TYPE") == "" {
		fail("Database configuration unavailable")
	}
	db, e := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")})
	if e != nil {
		fail("Cannot connect to application database")
	}
	sql, e := db.DB()
	if e != nil {
		fail("Database session unavailable")
	}
	defer sql.Close()
	if *op == "migrate-mail" {
		if *target != "" || *address != "" || *actor != "" {
			fail("migrate-mail does not accept account targets")
		}
		// Accounts migration includes invitations and encrypted access credentials.
		if e := migrations.RunMailAccountsMigration(db); e != nil {
			fail("Mail accounts migration failed")
		}
		if e := migrations.RunMailOutboxMigration(db); e != nil {
			fail("Mail outbox migration failed")
		}
		if e := migrations.RunMailWorkspaceMigration(db); e != nil {
			fail("Mail workspace migration failed")
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"migrated": true, "scope": "mail-only"})
		return
	}

	var row model.MailAccount
	q := db
	if *target != "" {
		id, e := uuid.Parse(*target)
		if e != nil {
			fail("Invalid user UUID")
		}
		q = q.Where("user_id = ?", id)
	} else if *address != "" {
		q = q.Where("address = ?", *address)
	} else {
		fail("Provide exactly one --user or --address")
	}
	if *target != "" && *address != "" {
		fail("Provide exactly one target")
	}
	if e = q.Take(&row).Error; e != nil {
		fail("Reservation not found")
	}
	if *op == "status" {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"user_id": row.UserID, "address": row.Address, "status": row.Status, "linked": row.StalwartAccountID != nil})
		return
	}
	if *op != "provision" {
		fail("Unsupported operation")
	}
	aid, e := uuid.Parse(*actor)
	if e != nil {
		fail("Provide a valid --actor administrator UUID")
	}
	var u model.User
	if e = db.Where("uuid = ? AND is_active = ?", aid, true).Take(&u).Error; e != nil || !authctx.RoleAtLeast(u.Role, authctx.RoleAdmin) {
		fail("Active Atoman administrator required")
	}
	p, e := mail.NewConfiguredMailProvision(db)
	if e != nil || p == nil {
		fail("Mail provisioning configuration or operational gates unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, e := p.ProvisionReserved(ctx, authctx.CurrentUser{ID: u.UUID, Role: u.Role, Username: u.Username}, row.UserID)
	if e != nil {
		fail("Provisioning did not complete; inspect reservation status before any retry")
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
