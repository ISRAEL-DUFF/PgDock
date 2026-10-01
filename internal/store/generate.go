// Package store is the metadata DB access layer: embedded goose migrations
// and sqlc-generated queries. Do not edit the *.sql.go, db.go, or models.go
// files by hand; edit queries/ and migrations/, then run `make generate`.
package store

//go:generate go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate
