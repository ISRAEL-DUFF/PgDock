package edge

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/projauth"
)

// Anonymous users and identities (V4 §4.1): an anonymous user has no
// credentials until it links one (an email or phone change, or an OAuth
// identity), which makes it permanent.

func (e *Edge) signinAnonymous(c *call, data json.RawMessage) {
	if !c.p.cfg.Auth.AnonymousEnabled {
		c.fail(http.StatusUnprocessableEntity, "anonymous_provider_disabled", "anonymous sign-ins are turned off for this project")
		return
	}
	ctx := c.r.Context()
	var out tokenResponse
	err := e.authTx(c, func(tx pgx.Tx) error {
		u, err := e.createUser(ctx, c, tx, projauth.NewUser{Anonymous: true, UserMetadata: data}, "anonymous")
		if err != nil {
			return err
		}
		out, err = e.startSession(ctx, c, tx, u, "anonymous")
		return err
	})
	if err != nil {
		e.authDBError(c, err)
		return
	}
	c.json(http.StatusOK, out)
}

// userOut is a user with its identities, the shape clients read.
type userOut struct {
	*projauth.User
	Identities []projauth.Identity `json:"identities"`
	Factors    []projauth.Factor   `json:"factors,omitempty"`
}

func (e *Edge) userWithIdentities(c *call, tx pgx.Tx, u *projauth.User) (userOut, error) {
	ids, err := projauth.Identities(c.r.Context(), tx, u.ID)
	if err != nil {
		return userOut{}, err
	}
	fs, err := projauth.Factors(c.r.Context(), tx, u.ID)
	if err != nil {
		return userOut{}, err
	}
	if ids == nil {
		ids = []projauth.Identity{}
	}
	return userOut{User: u, Identities: ids, Factors: fs}, nil
}

// unlinkIdentity is DELETE /auth/v1/user/identities/{id}.
func (e *Edge) unlinkIdentity(c *call, req Request, raw string) {
	uid, sid, ok := sessionOf(c, req)
	if !ok {
		return
	}
	if !c.p.cfg.Auth.ManualLinking {
		c.fail(http.StatusForbidden, "manual_linking_disabled", "linking and unlinking identities is turned off for this project")
		return
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		c.fail(http.StatusNotFound, "identity_not_found", "no such identity")
		return
	}
	ctx := c.r.Context()
	var fail *apiErr
	err = e.authTx(c, func(tx pgx.Tx) error {
		u, f, err := e.liveUser(ctx, tx, uid, sid, true)
		if err != nil || f != nil {
			fail = f
			return err
		}
		ident, err := projauth.UnlinkIdentity(ctx, tx, uid, id)
		switch {
		case errors.Is(err, projauth.ErrNotFound):
			fail = refuse(http.StatusNotFound, "identity_not_found", "no such identity")
			return nil
		case errors.Is(err, projauth.ErrLastIdentity):
			fail = refuse(http.StatusUnprocessableEntity, "single_identity_not_deletable", "the user's only identity can't be unlinked")
			return nil
		case err != nil:
			return err
		}
		// Unlinking the phone or email identity clears that sign-in method.
		up := projauth.Update{}
		changed := false
		if ident.Provider == "phone" && u.Phone != nil {
			none := ""
			up.Phone, changed = &none, true
		}
		if ident.Provider == "email" && u.Email != nil {
			none := ""
			up.Email, changed = &none, true
		}
		if changed {
			if _, err := projauth.UpdateUser(ctx, tx, uid, up); err != nil {
				return err
			}
		}
		return projauth.Audit(ctx, tx, &uid, projauth.ActUnlinked, c.ip, map[string]any{"provider": ident.Provider})
	})
	if err != nil {
		e.authDBError(c, err)
		return
	}
	if fail != nil {
		fail.send(c)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}
