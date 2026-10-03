package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/israel-duff/pgdock/internal/api/client"
)

func (a *App) orgsList(args []string) error {
	if _, err := parse(flag.NewFlagSet("orgs list", flag.ContinueOnError), args); err != nil {
		return err
	}
	if err := a.connectAPI(); err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListOrgsWithResponse(c)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Items, func(w io.Writer) {
		fmt.Fprintln(w, "NAME\tROLE\tPROJECTS\tSTATUS\tID")
		for _, o := range r.JSON200.Items {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", o.Name, o.Role, o.ProjectCount, o.Status, o.Id)
		}
	})
}

func (a *App) orgsCreate(args []string) error {
	pos, err := parse(flag.NewFlagSet("orgs create", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "orgs create <name>"); err != nil {
		return err
	}
	if err := a.connectAPI(); err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.CreateOrgWithResponse(c, client.CreateOrgRequest{Name: pos[0]})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON201, func(w io.Writer) {
		fmt.Fprintf(w, "Created %s (%s). Tokens act in one organisation: log in again to use it.\n", r.JSON201.Name, r.JSON201.Id)
	})
}

func (a *App) orgMembers(args []string) error {
	if _, err := parse(flag.NewFlagSet("org members", flag.ContinueOnError), args); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListOrgMembersWithResponse(c, org)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Items, func(w io.Writer) {
		fmt.Fprintln(w, "EMAIL\tROLE\tPROJECTS\tJOINED")
		for _, m := range r.JSON200.Items {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", m.Email, m.Role, len(m.Projects), when(&m.JoinedAt))
		}
	})
}

// member finds an org member by email.
func (a *App) member(org uuid.UUID, email string) (client.OrgMember, error) {
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListOrgMembersWithResponse(c, org)
	if err := check(r, err); err != nil {
		return client.OrgMember{}, err
	}
	for _, m := range r.JSON200.Items {
		if strings.EqualFold(m.Email, email) {
			return m, nil
		}
	}
	return client.OrgMember{}, &apiError{Status: 404, Code: "not_found", Message: email + " is not a member"}
}

func (a *App) orgInvite(args []string) error {
	fs := flag.NewFlagSet("org invite", flag.ContinueOnError)
	role := fs.String("role", "member", "member, admin or owner")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "org invite <email> --role member|admin|owner"); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.InviteOrgMemberWithResponse(c, org, client.InviteRequest{Email: openapi_types.Email(pos[0]), Role: client.OrgRole(*role)})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON201, func(w io.Writer) {
		fmt.Fprintf(w, "Invited %s as %s.\n", pos[0], *role)
		if r.JSON201.Url != "" {
			fmt.Fprintf(w, "Link (if the email doesn't arrive):\t%s\n", r.JSON201.Url)
		}
	})
}

func (a *App) orgRemove(args []string) error {
	pos, err := parse(flag.NewFlagSet("org remove", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "org remove <email>"); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	m, err := a.member(org, pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.RemoveOrgMemberWithResponse(c, org, m.UserId)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(map[string]string{"removed": m.Email}, func(w io.Writer) { fmt.Fprintf(w, "Removed %s.\n", m.Email) })
}

func (a *App) orgUsage(args []string) error {
	fs := flag.NewFlagSet("org usage", flag.ContinueOnError)
	from := fs.String("from", "", "start date, YYYY-MM-DD (default: the start of this month)")
	to := fs.String("to", "", "end date, YYYY-MM-DD (default: now)")
	metric := fs.String("metric", "", "one metric")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	params := &client.GetOrgUsageParams{}
	for _, v := range []struct {
		s   string
		dst **time.Time
	}{{*from, &params.From}, {*to, &params.To}} {
		if v.s == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", v.s)
		if err != nil {
			return usageErrorf("dates are YYYY-MM-DD: %s", v.s)
		}
		*v.dst = &t
	}
	if *metric != "" {
		params.Metric = metric
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetOrgUsageWithResponse(c, org, params)
	if err := check(r, err); err != nil {
		return err
	}
	u := r.JSON200
	units := map[string]string{}
	for _, m := range u.Metrics {
		units[m.Name] = m.Unit
	}
	return a.emit(u, func(w io.Writer) {
		fmt.Fprintf(w, "From %s to %s\n\n", u.From.Local().Format("2006-01-02 15:04"), u.To.Local().Format("2006-01-02 15:04"))
		fmt.Fprintln(w, "METRIC\tTOTAL")
		for _, t := range u.Totals {
			fmt.Fprintf(w, "%s\t%s %s\n", t.Metric, strconv.FormatFloat(float64(t.Quantity), 'f', -1, 32), units[t.Metric])
		}
	})
}

func (a *App) orgQuotas(args []string) error {
	if _, err := parse(flag.NewFlagSet("org quotas", flag.ContinueOnError), args); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetOrgQuotasWithResponse(c, org)
	if err := check(r, err); err != nil {
		return err
	}
	q := r.JSON200
	return a.emit(q, func(w io.Writer) {
		fmt.Fprintf(w, "Plan: %s\n\n", q.Plan)
		fmt.Fprintln(w, "LIMIT\tUSED\tMAX")
		for _, it := range q.Items {
			limit := "unlimited"
			if it.Max != nil {
				limit = strconv.FormatInt(*it.Max, 10)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", it.Limit, strconv.FormatFloat(float64(it.Used), 'f', -1, 32), limit)
		}
	})
}

// ---- Project members ------------------------------------------------------------

func (a *App) membersList(args []string) error {
	pos, err := parse(flag.NewFlagSet("members list", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "members list <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListProjectMembersWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Items, func(w io.Writer) {
		fmt.Fprintln(w, "EMAIL\tROLE\tVIA")
		for _, m := range r.JSON200.Items {
			via := "project"
			if m.Implicit {
				via = "org " + string(deref(m.OrgRole))
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", m.Email, m.Role, via)
		}
	})
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func (a *App) membersInvite(args []string) error {
	fs := flag.NewFlagSet("members invite", flag.ContinueOnError)
	role := fs.String("role", "developer", "admin, developer or read_only")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "members invite <project> <email> --role admin|developer|read_only"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.AddProjectMemberWithResponse(c, p.Id, client.ProjectMemberRequest{Email: openapi_types.Email(pos[1]), Role: client.ProjectRole(*role)})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(map[string]string{"project": p.Name, "email": pos[1], "role": *role}, func(w io.Writer) {
		fmt.Fprintf(w, "Added %s to %s as %s.\n", pos[1], p.Name, *role)
	})
}

func (a *App) membersRemove(args []string) error {
	pos, err := parse(flag.NewFlagSet("members remove", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "members remove <project> <email>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListProjectMembersWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	var user uuid.UUID
	for _, m := range r.JSON200.Items {
		if strings.EqualFold(m.Email, pos[1]) {
			user = m.UserId
		}
	}
	if user == uuid.Nil {
		return &apiError{Status: 404, Code: "not_found", Message: pos[1] + " is not a member of " + p.Name}
	}
	d, err := a.api.RemoveProjectMemberWithResponse(c, p.Id, user)
	if err := check(d, err); err != nil {
		return err
	}
	return a.emit(map[string]string{"removed": pos[1]}, func(w io.Writer) { fmt.Fprintf(w, "Removed %s from %s.\n", pos[1], p.Name) })
}

// ---- Tokens ----------------------------------------------------------------------

func (a *App) tokensList(args []string) error {
	if _, err := parse(flag.NewFlagSet("tokens list", flag.ContinueOnError), args); err != nil {
		return err
	}
	if err := a.connectAPI(); err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListMyTokensWithResponse(c)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Items, func(w io.Writer) {
		fmt.Fprintln(w, "ID\tNAME\tPREFIX\tSCOPES\tPROJECTS\tSTATUS\tEXPIRES\tLAST USED")
		for _, t := range r.JSON200.Items {
			sc := make([]string, len(t.Scopes))
			for i, s := range t.Scopes {
				sc[i] = string(s)
			}
			projects := "all"
			if t.ProjectIds != nil {
				projects = strconv.Itoa(len(*t.ProjectIds))
			}
			fmt.Fprintf(w, "%s\t%s\t%s…\t%s\t%s\t%s\t%s\t%s\n", t.Id, t.Name, t.Prefix, strings.Join(sc, ","), projects, t.Status, when(&t.ExpiresAt), when(t.LastUsedAt))
		}
	})
}

// parseExpiry reads "90d", "12h", or a plain number of days.
func parseExpiry(s string) (int, error) {
	switch {
	case strings.HasSuffix(s, "d"):
		return strconv.Atoi(strings.TrimSuffix(s, "d"))
	case strings.HasSuffix(s, "h"):
		h, err := strconv.Atoi(strings.TrimSuffix(s, "h"))
		if err != nil || h%24 != 0 {
			return 0, fmt.Errorf("expiry in hours must be whole days")
		}
		return h / 24, nil
	}
	return strconv.Atoi(s)
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func (a *App) tokensCreate(args []string) error {
	fs := flag.NewFlagSet("tokens create", flag.ContinueOnError)
	name := fs.String("name", "", "what the token is for, e.g. \"GitHub Actions — blog\"")
	scopes := fs.String("scopes", "read", "read, write, admin (comma-separated; admin requires write)")
	expires := fs.String("expires", "90d", "lifetime, e.g. 30d (at most the platform maximum)")
	var projects multi
	fs.Var(&projects, "project", "restrict the token to this project (repeatable)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" {
		return usageErrorf("usage: pgdock tokens create --name … --scopes read,write [--project <p>] [--expires 90d]")
	}
	days, err := parseExpiry(*expires)
	if err != nil {
		return usageErrorf("--expires: %v", err)
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	req := client.CreateTokenRequest{Name: *name, OrgId: org, ExpiresInDays: &days}
	for _, s := range strings.Split(*scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			req.Scopes = append(req.Scopes, client.TokenScope(s))
		}
	}
	if len(projects) > 0 {
		ids := []uuid.UUID{}
		for _, ref := range projects {
			p, err := a.project(ref)
			if err != nil {
				return err
			}
			ids = append(ids, p.Id)
		}
		req.ProjectIds = &ids
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.CreateTokenWithResponse(c, req)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON201, func(w io.Writer) {
		fmt.Fprintln(w, r.JSON201.Secret)
		fmt.Fprintf(a.Stderr, "Token %s created; it is shown once and expires %s.\n", r.JSON201.Token.Id, when(&r.JSON201.Token.ExpiresAt))
	})
}

func (a *App) tokensRevoke(args []string) error {
	pos, err := parse(flag.NewFlagSet("tokens revoke", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "tokens revoke <id>"); err != nil {
		return err
	}
	id, err := uuid.Parse(pos[0])
	if err != nil {
		return usageErrorf("not a token id: %s (pgdock tokens list)", pos[0])
	}
	if err := a.connectAPI(); err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.RevokeMyTokenWithResponse(c, id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(map[string]string{"revoked": id.String()}, func(w io.Writer) { fmt.Fprintf(w, "Revoked %s.\n", id) })
}
