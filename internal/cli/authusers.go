package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/client"
)

// A project's app users (V4 §4.9, §8.2).

func printAuthUsers(w io.Writer, l *client.AuthUserList) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tEMAIL\tSTATUS\tLAST SIGN-IN\tCREATED")
	for _, u := range l.Items {
		email, status, last := "", "confirmed", "never"
		if u.Email != nil {
			email = *u.Email
		}
		switch {
		case u.BannedUntil != nil:
			status = "banned until " + u.BannedUntil.Local().Format("2006-01-02 15:04")
		case u.EmailConfirmedAt == nil && u.InvitedAt != nil:
			status = "invited"
		case u.EmailConfirmedAt == nil:
			status = "unconfirmed"
		}
		if u.LastSignInAt != nil {
			last = u.LastSignInAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Id, email, status, last, u.CreatedAt.Local().Format("2006-01-02"))
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "%d of %d users (%d confirmed, %d banned, %d sessions)\n", len(l.Items), l.Total, l.Stats.Confirmed, l.Stats.Banned, l.Stats.Sessions)
}

func (a *App) authUsersList(args []string) error {
	fs := flag.NewFlagSet("auth users list", flag.ContinueOnError)
	search := fs.String("search", "", "an email, phone or user id fragment")
	page := fs.Int("page", 1, "page (50 a page)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "auth users list <project> [--search …] [--page n]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	params := &client.ListAuthUsersParams{Page: page}
	if *search != "" {
		params.Q = search
	}
	r, err := a.api.ListAuthUsersWithResponse(c, p.Id, params)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) { printAuthUsers(w, r.JSON200) })
}

// authUserArgs resolves <project> <user id>.
func (a *App) authUserArgs(args []string, usage string) (client.Project, uuid.UUID, error) {
	pos, err := parse(flag.NewFlagSet(usage, flag.ContinueOnError), args)
	if err != nil {
		return client.Project{}, uuid.Nil, err
	}
	if err := need(pos, 2, usage); err != nil {
		return client.Project{}, uuid.Nil, err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return client.Project{}, uuid.Nil, err
	}
	id, err := uuid.Parse(pos[1])
	if err != nil {
		return client.Project{}, uuid.Nil, fmt.Errorf("%q isn't a user id", pos[1])
	}
	return p, id, nil
}

func (a *App) authUsersShow(args []string) error {
	p, id, err := a.authUserArgs(args, "auth users show <project> <user id>")
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetAuthUserWithResponse(c, p.Id, id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		d := r.JSON200
		printAuthUsers(w, &client.AuthUserList{Items: []client.AuthUser{d.User}, Total: 1})
		fmt.Fprintf(w, "\n%d session(s)\n", len(d.Sessions))
		for _, s := range d.Sessions {
			ua := ""
			if s.UserAgent != nil {
				ua = *s.UserAgent
			}
			fmt.Fprintf(w, "  %s  since %s  %s\n", s.Id, s.CreatedAt.Local().Format("2006-01-02 15:04"), ua)
		}
		fmt.Fprintln(w, "\nRecent activity")
		for _, e := range d.Audit {
			fmt.Fprintf(w, "  %s  %s\n", e.At.Local().Format("2006-01-02 15:04:05"), e.Action)
		}
	})
}

func (a *App) authUsersInvite(args []string) error {
	pos, err := parse(flag.NewFlagSet("auth users invite", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "auth users invite <project> <email>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	invite := true
	r, err := a.api.CreateAuthUserWithResponse(c, p.Id, client.CreateAuthUserJSONRequestBody{Email: pos[1], Invite: &invite})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON201, func(w io.Writer) { fmt.Fprintf(w, "Invited %s (user %s)\n", pos[1], r.JSON201.Id) })
}

func (a *App) authUsersBan(args []string) error {
	fs := flag.NewFlagSet("auth users ban", flag.ContinueOnError)
	dur := fs.String("for", "876000h", `how long ("24h"); for good by default`)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	return a.authBan(pos, *dur, "auth users ban <project> <user id> [--for 24h]")
}

func (a *App) authUsersUnban(args []string) error {
	pos, err := parse(flag.NewFlagSet("auth users unban", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	return a.authBan(pos, "none", "auth users unban <project> <user id>")
}

func (a *App) authBan(pos []string, dur, usage string) error {
	if err := need(pos, 2, usage); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	id, err := uuid.Parse(pos[1])
	if err != nil {
		return fmt.Errorf("%q isn't a user id", pos[1])
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.UpdateAuthUserWithResponse(c, p.Id, id, client.UpdateAuthUserJSONRequestBody{BanDuration: &dur})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		if r.JSON200.BannedUntil != nil {
			fmt.Fprintf(w, "Banned until %s (sessions end at their next refresh)\n", r.JSON200.BannedUntil.Local().Format("2006-01-02 15:04"))
		} else {
			fmt.Fprintln(w, "Unbanned")
		}
	})
}

func (a *App) authUsersSignOut(args []string) error {
	p, id, err := a.authUserArgs(args, "auth users signout <project> <user id>")
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.SignOutAuthUserWithResponse(c, p.Id, id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) { fmt.Fprintf(w, "Ended %d session(s)\n", r.JSON200.SessionsEnded) })
}

func (a *App) authUsersDelete(args []string) error {
	p, id, err := a.authUserArgs(args, "auth users delete <project> <user id>")
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.DeleteAuthUserWithResponse(c, p.Id, id)
	if err := check(r, err); err != nil {
		return err
	}
	if a.json {
		return a.emit(map[string]string{"deleted": id.String()}, nil)
	}
	fmt.Fprintf(a.Stdout, "Deleted user %s\n", id)
	return nil
}

func (a *App) authRotateKey(args []string) error {
	pos, err := parse(flag.NewFlagSet("auth rotate-key", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "auth rotate-key <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.RotateSigningKeyWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		for _, k := range r.JSON200.Items {
			fmt.Fprintf(w, "%s  %s\n", k.Kid, k.Status)
		}
	})
}

func (a *App) printAuthConfig(c *client.AuthConfig) {
	_ = a.emit(c, func(w io.Writer) {
		st := c.Settings
		fmt.Fprintf(w, "Auth API     %s\n", c.AuthUrl)
		if c.OauthCallbackUrl != nil {
			fmt.Fprintf(w, "Callback     %s\n", *c.OauthCallbackUrl)
		}
		var oauth []string
		if st.Oauth != nil {
			for name, o := range *st.Oauth {
				if o.Enabled {
					oauth = append(oauth, name)
				}
			}
		}
		sort.Strings(oauth)
		var chans []string
		if st.PhoneChannels != nil {
			for _, ch := range *st.PhoneChannels {
				chans = append(chans, string(ch))
			}
		}
		fmt.Fprintf(w, "OAuth        %s\n", orNone(strings.Join(oauth, ", ")))
		fmt.Fprintf(w, "Phone        %s (countries %s)\n", orNone(strings.Join(chans, ", ")), strings.Join(derefList(st.PhoneCountries), ", "))
		if st.MfaPolicy != nil {
			fmt.Fprintf(w, "MFA          %s\n", *st.MfaPolicy)
		}
		if c.Phone != nil {
			fmt.Fprintf(w, "Codes today  %d of %d\n", c.Phone.Sent24h, c.Phone.DailyCap)
			cur := ""
			if c.Phone.Currency != nil {
				cur = *c.Phone.Currency + " "
			}
			for _, m := range c.Phone.Month {
				fmt.Fprintf(w, "This month   %s: %d messages, %s%.2f\n", m.Channel, m.Messages, cur, float64(m.CostMinor)/100)
			}
		}
		if c.HookRole != nil {
			fmt.Fprintf(w, "Hook role    %s\n", *c.HookRole)
		}
	})
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func derefList(s *[]string) []string {
	if s == nil {
		return nil
	}
	return *s
}

func (a *App) authConfig(args []string) error {
	pos, err := parse(flag.NewFlagSet("auth config", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "auth config <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetAuthConfigWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	a.printAuthConfig(r.JSON200)
	return nil
}

func (a *App) authSet(args []string) error {
	pos, err := parse(flag.NewFlagSet("auth set", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, `auth set <project> '{"settings":{...}}'`); err != nil {
		return err
	}
	var up client.AuthConfigUpdate
	dec := json.NewDecoder(strings.NewReader(pos[1]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&up); err != nil {
		return fmt.Errorf("the change must be an AuthConfigUpdate as JSON: %w", err)
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.UpdateAuthConfigWithResponse(c, p.Id, up)
	if err := check(r, err); err != nil {
		return err
	}
	a.printAuthConfig(r.JSON200)
	return nil
}

func (a *App) authHooks(args []string) error {
	pos, err := parse(flag.NewFlagSet("auth hooks", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "auth hooks <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListAuthHookDeliveriesWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		if len(r.JSON200.Items) == 0 {
			fmt.Fprintln(w, "No webhook deliveries yet.")
			return
		}
		for _, d := range r.JSON200.Items {
			state := "retrying"
			switch {
			case d.DeliveredAt != nil:
				state = "delivered"
			case d.FailedAt != nil:
				state = "failed"
			}
			last := ""
			if d.LastError != nil && d.DeliveredAt == nil {
				last = "  " + *d.LastError
			}
			fmt.Fprintf(w, "%s  %-13s %-9s tries=%d%s\n", d.CreatedAt.Format(time.RFC3339), d.Event, state, d.Attempts, last)
		}
	})
}
