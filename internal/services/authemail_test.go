package services

import (
	"strings"
	"testing"

	"github.com/israel-duff/xenibase/internal/edgeapi"
	"github.com/israel-duff/xenibase/internal/mail"
)

func TestAuthEmailButtonsSayWhatTheyDo(t *testing.T) {
	body := "Reset your password for https://payquick.ng:\n\nhttps://payquick.ng/reset?t=1\n\nOr enter this code: 123456"
	c := authEmailContent(edgeapi.EmailRecovery, "Reset your password", body, "PayQuick")
	var buttons []string
	for _, b := range c.Blocks {
		if b.Kind == mail.BlockButton {
			buttons = append(buttons, b.Text)
		}
	}
	if len(buttons) != 1 || buttons[0] != "Reset my password" {
		t.Errorf("buttons %v", buttons)
	}
	if !c.Neutral || c.BrandName != "PayQuick" {
		t.Error("a project's email isn't under the project's name")
	}
	if h := c.HTML(); strings.Contains(h, "Xenibase") {
		t.Error("Xenibase's name is in a customer's email")
	}
}
