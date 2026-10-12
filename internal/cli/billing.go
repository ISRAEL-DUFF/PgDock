package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/client"
)

// naira formats kobo: 1500000 → "NGN 15,000.00".
func naira(kobo int64) string {
	sign := ""
	if kobo < 0 {
		sign, kobo = "-", -kobo
	}
	whole := fmt.Sprint(kobo / 100)
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return fmt.Sprintf("%sNGN %s.%02d", sign, b.String(), kobo%100)
}

func printLines(w io.Writer, lines []client.InvoiceLine) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', tabwriter.AlignRight)
	for _, l := range lines {
		fmt.Fprintf(tw, "%s\t%s\t\n", l.Description, naira(l.Amount))
	}
	_ = tw.Flush()
}

func (a *App) billingShow(args []string) error {
	if _, err := parse(flag.NewFlagSet("billing show", flag.ContinueOnError), args); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetOrgBillingWithResponse(c, org)
	if err := check(r, err); err != nil {
		return err
	}
	f, err := a.api.GetOrgForecastWithResponse(c, org)
	if err := check(f, err); err != nil {
		return err
	}
	out := struct {
		Account  *client.BillingAccount  `json:"account"`
		Forecast *client.BillingForecast `json:"forecast"`
	}{r.JSON200, f.JSON200}
	return a.emit(out, func(w io.Writer) {
		b := r.JSON200
		fmt.Fprintf(w, "Plan: %s (%s), price book %d\n", b.PlanName, b.Term, b.PriceBookVersion)
		if b.PendingChange != nil {
			fmt.Fprintf(w, "Changes to %s (%s) on %s\n", b.PendingChange.ToPlan, b.PendingChange.ToTerm, b.PendingChange.EffectiveAt.Format("2 Jan 2006"))
		}
		fc := f.JSON200
		fmt.Fprintf(w, "%s forecast: %s before VAT (usage %s)\n", fc.Month, naira(fc.SpendMinor), naira(fc.UsageMinor))
		if b.BudgetMinor != nil {
			fmt.Fprintf(w, "Budget: %s\n", naira(*b.BudgetMinor))
		}
		if b.SpendCapMinor != nil {
			state := ""
			if fc.Capped {
				state = " (reached: new billable resources are paused)"
			}
			fmt.Fprintf(w, "Spend cap: %s%s\n", naira(*b.SpendCapMinor), state)
		}
	})
}

func (a *App) billingPlan(args []string) error {
	fs := flag.NewFlagSet("billing plan", flag.ContinueOnError)
	annual := fs.Bool("annual", false, "pay a year in advance")
	now := fs.Bool("now", false, "apply a downgrade now, with a credit for the unused part")
	dry := fs.Bool("dry-run", false, "show what it costs without changing anything")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "billing plan free|pro|team [--annual] [--now] [--dry-run]"); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	term := client.PlanChangeRequestTermMonthly
	if *annual {
		term = client.PlanChangeRequestTermAnnual
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ChangeOrgPlanWithResponse(c, org, client.PlanChangeRequest{Plan: pos[0], Term: &term, Immediately: now, DryRun: dry})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		p := r.JSON200
		switch {
		case *dry:
			fmt.Fprintf(w, "%s (%s) → %s (%s) would take effect %s.\n", p.FromPlan, p.FromTerm, p.ToPlan, p.ToTerm, p.EffectiveAt.Format("2 Jan 2006"))
		case p.Applied:
			fmt.Fprintf(w, "Now on %s (%s).\n", p.ToPlan, p.ToTerm)
		default:
			fmt.Fprintf(w, "%s (%s) from %s.\n", p.ToPlan, p.ToTerm, p.EffectiveAt.Format("2 Jan 2006"))
		}
		if len(p.Lines) > 0 {
			printLines(w, p.Lines)
			fmt.Fprintf(w, "On the next invoice: %s + VAT\n", naira(p.TotalMinor))
		}
	})
}

func (a *App) billingInvoices(args []string) error {
	if _, err := parse(flag.NewFlagSet("billing invoices", flag.ContinueOnError), args); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListOrgInvoicesWithResponse(c, org)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NUMBER\tPERIOD\tTOTAL\tSTATUS\tID")
		for _, i := range r.JSON200.Items {
			num := "-"
			if i.Number != nil {
				num = *i.Number
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", num, i.PeriodStart.Format("2006-01"), naira(i.TotalMinor), i.Status, i.Id)
		}
		_ = tw.Flush()
	})
}

// invoiceID finds an invoice by id or number.
func (a *App) invoiceID(org uuid.UUID, ref string) (uuid.UUID, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListOrgInvoicesWithResponse(c, org)
	if err := check(r, err); err != nil {
		return uuid.Nil, err
	}
	for _, i := range r.JSON200.Items {
		if i.Number != nil && strings.EqualFold(*i.Number, ref) {
			return i.Id, nil
		}
	}
	return uuid.Nil, fmt.Errorf("no invoice %s", ref)
}

func (a *App) billingInvoice(args []string) error {
	fs := flag.NewFlagSet("billing invoice", flag.ContinueOnError)
	pdf := fs.String("pdf", "", "save the PDF to this file")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "billing invoice <number|id> [--pdf file.pdf]"); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	id, err := a.invoiceID(org, pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	if *pdf != "" {
		r, err := a.api.GetOrgInvoicePdfWithResponse(c, org, id)
		if err := check(r, err); err != nil {
			return err
		}
		if err := os.WriteFile(*pdf, r.Body, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "Saved %s (%d bytes)\n", *pdf, len(r.Body))
		return nil
	}
	r, err := a.api.GetOrgInvoiceWithResponse(c, org, id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		inv := r.JSON200.Invoice
		num := "(draft)"
		if inv.Number != nil {
			num = *inv.Number
		}
		fmt.Fprintf(w, "Invoice %s, %s, %s\n", num, inv.PeriodStart.Format("January 2006"), inv.Status)
		printLines(w, r.JSON200.Lines)
		fmt.Fprintf(w, "Subtotal %s, VAT %s, total %s\n", naira(inv.SubtotalMinor), naira(inv.VatMinor), naira(inv.TotalMinor))
		if inv.WhtExpectedMinor > 0 {
			fmt.Fprintf(w, "WHT you may deduct: %s\n", naira(inv.WhtExpectedMinor))
		}
		for _, cn := range r.JSON200.CreditNotes {
			fmt.Fprintf(w, "Credit note %s: -%s (%s)\n", cn.Number, naira(cn.AmountMinor+cn.VatMinor), cn.Reason)
		}
	})
}

func (a *App) billingPay(args []string) error {
	fs := flag.NewFlagSet("billing pay", flag.ContinueOnError)
	wallet := fs.Bool("wallet", false, "pay with an iSpend wallet instead of a card")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "billing pay <number|id> [--wallet]"); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	id, err := a.invoiceID(org, pos[0])
	if err != nil {
		return err
	}
	ch := client.StartCheckoutJSONBodyChannelCard
	if *wallet {
		ch = client.StartCheckoutJSONBodyChannelWallet
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.StartCheckoutWithResponse(c, org, client.StartCheckoutJSONRequestBody{Channel: ch, Purpose: client.StartCheckoutJSONBodyPurposeInvoice, InvoiceId: &id})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON201, func(w io.Writer) {
		fmt.Fprintf(w, "Pay %s at:\n%s\n", naira(r.JSON201.AmountMinor), r.JSON201.CheckoutUrl)
	})
}

func (a *App) billingTransfer(args []string) error {
	if _, err := parse(flag.NewFlagSet("billing transfer", flag.ContinueOnError), args); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.OrgVirtualAccountWithResponse(c, org)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		v := r.JSON200
		fmt.Fprintf(w, "Transfer to %s, %s (%s).\n", v.AccountNumber, v.BankName, v.AccountName)
		fmt.Fprintln(w, "The account is this organisation's alone; transfers settle the oldest open invoice and anything over becomes credit.")
	})
}

func (a *App) billingPayments(args []string) error {
	if _, err := parse(flag.NewFlagSet("billing payments", flag.ContinueOnError), args); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListOrgPaymentsWithResponse(c, org)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "RECEIVED\tAMOUNT\tVIA\tREFERENCE")
		for _, p := range r.JSON200.Items {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.ReceivedAt.Format("2006-01-02"), naira(p.AmountMinor), p.Channel, p.ProviderRef)
		}
		_ = tw.Flush()
	})
}
