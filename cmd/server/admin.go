package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/config"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/logging"
	"github.com/israel-duff/pgdock/internal/store"
)

const adminUsage = `usage: pgdock-server admin <command> [email]

Recovery for when no platform admin can sign in, run on the server (it talks
to the metadata database directly, so it needs the same environment as the
server):

  list                    the platform admins
  promote <email>         make an existing account a platform admin (also
                          approves it, verifies its address and re-enables it)
  demote <email>          make a platform admin an ordinary user (refuses to
                          demote the last one)
  reset-2fa <email>       remove the account's authenticator and recovery codes;
                          its next sign-in sets up a new one
  reset-password <email>  print a one-hour, single-use password reset link
                          (when email doesn't work)

With docker compose:  docker compose exec pgdock-server pgdock-server admin list
Every change is recorded in the platform audit log as done on the server.`

// runAdmin is `pgdock-server admin …`.
func runAdmin(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Println(adminUsage)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	keyring, err := crypto.NewKeyring(cfg.MasterKey, cfg.PreviousMasterKeys...)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	// The services' own warnings (no mail transport here) would only confuse.
	log := logging.New(io.Discard, "text", slog.LevelError)
	svc := auth.NewService(pool, keyring, auth.Config{PublicURL: strings.TrimRight(cfg.Insight.PublicURL, "/")}, "", log)
	return adminCommand(ctx, pool, svc, args, os.Stdout)
}

// adminCommand runs one admin command (separate from runAdmin for tests).
func adminCommand(ctx context.Context, db *pgxpool.Pool, svc *auth.Service, args []string, out io.Writer) error {
	cmd := args[0]
	if cmd == "list" {
		admins, err := svc.PlatformAdmins(ctx)
		if err != nil {
			return err
		}
		if len(admins) == 0 {
			fmt.Fprintln(out, "no platform admins: open the web UI to run first-time setup, or promote an account")
		}
		for _, u := range admins {
			state := "active"
			if u.DisabledAt != nil {
				state = "disabled"
			}
			twofa := "2FA set up"
			if len(u.TotpSecret) == 0 {
				twofa = "no 2FA yet"
			}
			fmt.Fprintf(out, "%s\t%s\t%s\n", u.Email, state, twofa)
		}
		return nil
	}
	if len(args) != 2 {
		return fmt.Errorf("%s needs one email address\n\n%s", cmd, adminUsage)
	}
	u, err := svc.UserByEmail(ctx, args[1])
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("no account with the email %q", args[1])
	}
	if err != nil {
		return err
	}
	switch cmd {
	case "promote":
		if _, err := svc.SetPlatformRole(ctx, u.ID, auth.RolePlatformAdmin, auth.RoleChange{Recovery: true}); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s is a platform admin. They sign in again; if they have no authenticator yet they will set one up.\n", u.Email)
	case "demote":
		if _, err := svc.SetPlatformRole(ctx, u.ID, auth.RoleUser, auth.RoleChange{Recovery: true}); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s is an ordinary user again.\n", u.Email)
	case "reset-2fa":
		if err := svc.ResetTOTP(ctx, u.ID); err != nil {
			return err
		}
		fmt.Fprintf(out, "Two-factor reset for %s. Their next sign-in (password, then a new authenticator) sets it up again.\n", u.Email)
	case "reset-password":
		link, err := svc.PasswordResetLink(ctx, u.Email)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Open this link within one hour (it works once):\n\n%s\n", link)
		if !strings.HasPrefix(link, "http") {
			fmt.Fprintln(out, "\nPGDOCK_PUBLIC_URL isn't set here, so the link is relative: put your web UI's address in front of it.")
		}
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, adminUsage)
	}
	return recordAdminAction(ctx, db, cmd, u)
}

// recordAdminAction writes the platform audit entry for a change made on the
// server.
func recordAdminAction(ctx context.Context, db *pgxpool.Pool, cmd string, u store.User) error {
	target, id := "user", u.ID.String()
	detail, _ := json.Marshal(map[string]string{"by": "an operator on the server (pgdock-server admin)", "email": u.Email})
	return store.New(db).InsertAudit(ctx, store.InsertAuditParams{
		Action: "admin." + cmd, TargetType: &target, TargetID: &id, Detail: detail, Outcome: "success", ActorKind: "system",
	})
}
