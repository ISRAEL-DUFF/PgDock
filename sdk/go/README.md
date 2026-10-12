# PGDock Go SDK

The Go client for servers and tools. It covers the data API, auth
(including verifying users' tokens against the JWKS), storage and
realtime.

    go get github.com/israel-duff/pgdock/sdk/go

```go
c, _ := pgdock.New("https://<ref>.api.pgdock.ng", os.Getenv("PGDOCK_SECRET_KEY"))
todos, _, err := pgdock.List[Todo](ctx, c.Data.From("todos").Select("id,title").Eq("done", false))
```

The full reference is [docs/sdk/go.md](../../docs/sdk/go.md).
