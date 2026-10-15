# Xenibase Go SDK

The Go client for servers and tools. It covers the data API, auth
(including verifying users' tokens against the JWKS), storage and
realtime.

    go get github.com/israel-duff/xenibase/sdk/go

```go
c, _ := xenibase.New("https://<ref>.api.xenibase.ng", os.Getenv("XENIBASE_SECRET_KEY"))
todos, _, err := xenibase.List[Todo](ctx, c.Data.From("todos").Select("id,title").Eq("done", false))
```

The full reference is [docs/sdk/go.md](../../docs/sdk/go.md).
