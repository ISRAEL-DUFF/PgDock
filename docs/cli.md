# The pgdock CLI and API tokens

`pgdock` works with your PGDock from a terminal, a script or a CI job. It
talks to the same API as the web UI, with an **API token** in place of a
browser session.

## Install

Download the binary for your system from the release
(`pgdock-cli-<version>-<os>-<arch>`, for linux, darwin and windows on
amd64 and arm64), check it against `SHA256SUMS`, and put it on your
`PATH` as `pgdock`. Or build it from source with `go build -o pgdock ./cmd/cli`.

## Log in

```sh
pgdock login --server https://pgdock.example.com
```

The CLI prints a code and opens your browser. Sign in if you aren't
already, check the code matches, choose the **organisation** the CLI
will act in and its **scopes**, and approve. The CLI saves the token in
`~/.config/pgdock/config.toml` (readable only by you) as a **context**.
Logging in to another organisation, or another PGDock, adds another
context:

```sh
pgdock context list
pgdock context use pgdock.example.com/acme
```

Without a browser on the machine, use `--no-browser` and open the link
elsewhere, or paste a token made under **Account → API tokens**:
`pgdock login --server … --token -` (reads it from stdin). For a server
whose certificate isn't publicly trusted, add `--ca-file ca.pem`.

`pgdock logout` revokes the context's token and forgets it.

## Tokens

A token belongs to you and acts in **one organisation**:

| Scope | Allows |
| --- | --- |
| `read` | Viewing everything you can see in the organisation; read-only SQL |
| `write` | Creating and changing: SQL that writes, backups, restoring into a new project, creating projects |
| `admin` | Destructive and settings actions: deleting, promoting, restoring in place, members, settings (needs `write`) |

A token can be **restricted to some projects**: it then can't see or
touch anything else in the organisation, even what you can. Every token
expires (90 days by default, at most a year, or less if the platform
admin set a lower maximum). It never does more than you can: when your
role changes, it changes; when you leave the organisation, or it is
suspended, the token stops working.

Destructive actions with a token need the `admin` scope **and** a typed
confirmation (`--confirm <name>` in the CLI, a `confirm` field in the
API), in place of the browser's password-and-code step.

Manage your tokens under **Account → API tokens** or with
`pgdock tokens list | create | revoke`. Organisation owners and admins
see and can revoke every token scoped to their organisation
(**Organisation → API tokens**). You get an email when a token is
created and a week before it expires.

## In CI

Make a token for the job: `write` scope, restricted to the project it
deploys, expiring when you'd want to rotate it.

```sh
pgdock tokens create --name "GitHub Actions — blog" --scopes read,write --project blog --expires 90d
```

Store it as a secret and set `PGDOCK_SERVER` and `PGDOCK_TOKEN`; they
take priority over any saved context:

```yaml
# .github/workflows/migrate.yml
jobs:
  migrate:
    runs-on: ubuntu-latest
    env:
      PGDOCK_SERVER: https://pgdock.example.com
      PGDOCK_TOKEN: ${{ secrets.PGDOCK_TOKEN }}
    steps:
      - uses: actions/checkout@v4
      - run: curl -fsSL -o pgdock https://github.com/…/releases/download/v1.1.0/pgdock-cli-v1.1.0-linux-amd64 && chmod +x pgdock
      - run: ./pgdock sql blog -f migrations/latest.sql
      - run: ./pgdock backup create blog
```

## Commands

```
pgdock login | logout | context list | context use <name> | whoami
pgdock orgs list | create <name>
pgdock org members | invite <email> --role <r> | remove <email> | usage | quotas

pgdock projects list | info <p> | create <name> [--tier] | delete <p> --confirm <name>
pgdock connect <p> [--pooled|--session] [--psql]
pgdock creds <p> [--rotate]
pgdock sql <p> -c "select …" | -f file.sql [--json|--csv]

pgdock backup list <p> | create <p> | restore <p> --backup <id> [--into <name>]
pgdock promote <p> [--node <id>] [--profile <size>]

pgdock members list <p> | invite <p> <email> --role <r> | remove <p> <email>
pgdock tokens list | create --name … --scopes … [--project <p>] [--expires 90d] | revoke <id>
pgdock operations get <id> [--follow]
```

`<p>` is a project's name or id. `connect` prints a URL with your
**personal** database login for the project (issuing one the first
time, and remembering it in `~/.config/pgdock/credentials.toml`); `creds
--rotate` issues a new password.

Every command takes `--json` for scripts. Long operations (create,
backup, restore, promote, delete) stream their progress; `--no-wait`
returns the operation at once.

Exit codes: `0` success, `1` error (including "not found"), `2` usage
error, `3` the operation failed, `4` permission denied.
