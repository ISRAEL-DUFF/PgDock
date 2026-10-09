# Go SDK

The Go SDK is for servers, scripts and command-line tools. It needs Go
1.25 or later.

    go get github.com/israel-duff/pgdock/sdk/go

```go
import pgdock "github.com/israel-duff/pgdock/sdk/go"

c, err := pgdock.New("https://k7f3m2q9.api.pgdock.ng", os.Getenv("PGDOCK_SECRET_KEY"))
```

A client made with the **secret key** acts as the service role, which
skips row-level security. Use it for jobs and admin work.

To act for a user, make a client with the **publishable key** and call
`WithToken` with the user's access token. Verify the token first:

```go
func handler(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := app.Auth.VerifyToken(r.Context(), tok) // ES256 against the project's JWKS, expiry, audience
	if err != nil {
		http.Error(w, "sign in", http.StatusUnauthorized)
		return
	}
	asUser := app.WithToken(tok) // app := pgdock.New(url, publishableKey)
	todos, _, err := pgdock.List[db.Todo](r.Context(), asUser.Data.From(db.TodosTable).Select("").Eq("done", false))
	…
	_ = claims.Subject() // the user's id
}
```

## Errors

API failures are `*pgdock.Error` values with `Status`, `Code`, `Message`
and `RequestID`. Test for a code with `pgdock.IsCode(err, "rls_required")`.
A network failure is returned as Go's own error.

## Data

```go
type Todo struct { // or the generated db.Todo
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

rows, page, err := pgdock.List[Todo](ctx, c.Data.From("todos").Select("id,title,done").
	Eq("done", false).Or(pgdock.Cond("a", pgdock.OpEq, 1), pgdock.Cond("b", pgdock.OpEq, 2)).
	Order("created_at", true).Limit(20).Count("exact"))
next, _, err := pgdock.List[Todo](ctx, c.Data.From("todos").Select("").Cursor(page.NextCursor))

var created []Todo
n, err := c.Data.From("todos").Insert(map[string]any{"title": "Buy milk"}).Exec(ctx, &created)
n, err = c.Data.From("todos").Update(map[string]any{"done": true}).Eq("id", 42).AtMost(1).Exec(ctx, nil)
n, err = c.Data.From("todos").Delete().Key(42).Exec(ctx, nil)

var total int
err = c.Data.RPC(ctx, "count_open", map[string]any{"list_id": 7}, &total, true)
results, err := c.Data.Batch(ctx, []pgdock.BatchOp{{Op: "insert", Table: "orders", Rows: []any{map[string]any{"item": "tea"}}}})
```

The generated Go types (`pgdock gen types --lang go --package db`) give a
struct per table and view, `…Insert` and `…Update` structs, and a
`…Table` constant per name, to use with `From`.

## Auth

A client signs a user in and keeps the session, as an app or CLI would:

```go
s, err := c.Auth.SignInWithPassword(ctx, pgdock.Credentials{Email: email, Password: pw})
err = c.Auth.SignInWithOTP(ctx, pgdock.OTP{Phone: "+2348031234567", Channel: "whatsapp"})
s, err = c.Auth.VerifyOTP(ctx, pgdock.Verify{Type: "whatsapp", Phone: "+2348031234567", Token: "123456"})
c.Auth.AutoRefresh(ctx) // refresh in the background until ctx ends
```

The token is also refreshed before any request that would carry an expired
one. Only one refresh runs at a time, because a refresh token works once.

For OAuth, `SignInWithOAuth(provider, redirectTo)` returns the authorize
URL and a PKCE verifier. When the provider redirects back, pass the code
and the verifier to `ExchangeCode`.

## Storage

```go
b := c.Storage.Bucket("avatars")
f, err := b.Upload(ctx, uid+"/me.png", file, size, pgdock.UploadOptions{ContentType: "image/png", Upsert: true})
body, hdr, err := b.Download(ctx, uid+"/me.png", "")   // the caller closes body
url, err := b.SignedURL(ctx, uid+"/me.png", time.Hour)
items, next, err := b.List(ctx, uid+"/", "", 100)
_, err = b.Remove(ctx, uid+"/old.png")
```

## Realtime

```go
ch := c.Realtime.Channel("orders").
	OnChange(pgdock.ChangeFilter{Event: "INSERT", Table: "orders"}, func(ch pgdock.Change) { … }).
	OnResync(func() { … refetch … })
if err := ch.Subscribe(ctx); err != nil { … }
defer c.Realtime.Close()
```

The connection reconnects by itself. After a reconnect it rejoins every
channel and calls each one's `OnResync`, because changes made while it was
away were missed.
