package mail

import (
	"os"
	"path/filepath"
)

// Sample is a representative message, for looking at the layout.
type Sample struct {
	Name    string
	Message Message
}

// Samples are messages that use every kind of block, in Xenibase's layout and
// in a customer's neutral one, for `xenibase-server mail-preview`.
func Samples() []Sample {
	app := baseURL()
	if app == "" {
		app = "https://app.example.com"
	}
	notice := FromText("Backup is overdue", "The nightly backup of Orders hasn't run in 26 hours.\n\nWhat we checked:\n\n- the schedule is on\n- the node is reachable\n- the last attempt failed with a timeout\n\n"+app+"/projects/1/backups")
	notice.SignOff = SignOff
	neutral := FromText("Confirm your email", "Confirm your email address for PayQuick:\n\nhttps://payquick.example/confirm?token=abc123\n\nThe link and code expire in 10 minutes. If you didn't sign up, ignore this email.")
	neutral.Neutral, neutral.BrandName = true, "PayQuick"
	return []Sample{
		{"verify", Compose([]string{"ada@example.com"}, "Confirm your email to finish setting up Xenibase", Content{
			Preheader: "One quick step and your Xenibase account is ready.", Title: "One quick step to get started",
			Blocks:  []Block{P(Hi("Ada")), P("Welcome to Xenibase. Please confirm your email address to finish creating your account."), Button("Confirm my email", app+"/verify-email?token=abc"), P("This link works for 48 hours.")},
			SignOff: SignOff, Note: "If you didn't sign up, you can safely ignore this email.",
		})},
		{"code", Compose([]string{"ada@example.com"}, "Your sign-in code", Content{
			Title: "Your sign-in code", Blocks: []Block{P("Use this code to sign in:"), Code("482 913"), P("It expires in 10 minutes.")},
		})},
		{"invoice", Compose([]string{"billing@example.com"}, "Xenibase invoice XB-2026-000012 for October 2026", Content{
			Preheader: "Invoice XB-2026-000012: ₦61,160.00, due 8 November 2026.", Title: "Your invoice for October 2026",
			Blocks: []Block{P("Hello,"), P("Thank you for building with Xenibase. Here is the invoice for Acme's usage in October 2026."),
				Facts(Fact{"Invoice", "XB-2026-000012"}, Fact{"Priced in dollars", "$38.00 before VAT, $40.85 with VAT"}, Fact{"Exchange rate", "NGN 1,497.06 per USD 1"},
					Fact{"Subtotal", "₦56,888.28"}, Fact{"VAT", "₦4,266.62"}, Fact{"Total", "₦61,154.90"}, Fact{"Due", "8 November 2026"}),
				Button("View and download the invoice", app+"/org/billing")},
			SignOff: SignOff, Note: "You're getting this because you're a billing contact for Acme.",
		})},
		{"alert", Message{To: []string{"ops@example.com"}, Subject: "[Xenibase] Backup is overdue", Content: &notice, Body: notice.Text()}},
		{"project-email", Message{To: []string{"user@example.com"}, Subject: "Confirm your email", Content: &neutral, Body: neutral.Text()}},
	}
}

// WritePreviews writes each sample's HTML and text into dir, and returns the
// number written.
func WritePreviews(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range Samples() {
		html := ""
		if s.Message.Content != nil {
			html = s.Message.Content.HTML()
		}
		if html != "" {
			if err := os.WriteFile(filepath.Join(dir, s.Name+".html"), []byte(html), 0o644); err != nil {
				return n, err
			}
		}
		if err := os.WriteFile(filepath.Join(dir, s.Name+".txt"), []byte(s.Message.Body), 0o644); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
