package edge

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// openAPI serves the project's own OpenAPI document (V4 §3.7), generated
// from its catalog. Secret key only: it describes every exposed table.
func (e *Edge) openAPI(c *call, req Request) {
	if req.Role != "service" {
		c.fail(http.StatusForbidden, "secret_key_required", "the OpenAPI document needs the secret key")
		return
	}
	ctx, cancel := context.WithTimeout(c.r.Context(), req.Timeout+2*time.Second)
	defer cancel()
	var cat *Catalog
	err := e.WithRequest(ctx, c.p, req, func(tx pgx.Tx) error {
		var err error
		cat, _, err = e.catalog(ctx, c.p, tx)
		return err
	})
	if err != nil {
		e.dataDBError(c, err)
		return
	}
	c.json(http.StatusOK, openAPIDoc(c.p, cat))
}

func jsonType(col *Column) map[string]any {
	s := map[string]any{}
	switch {
	case col.TypName == "int2" || col.TypName == "int4" || col.TypName == "int8":
		s["type"] = "integer"
	case col.Category == 'N':
		s["type"] = "number"
	case col.Category == 'B':
		s["type"] = "boolean"
	case col.Category == 'A':
		s["type"] = "array"
		s["items"] = map[string]any{}
	case col.JSON():
		s["description"] = "JSON"
	case col.TypName == "uuid":
		s["type"], s["format"] = "string", "uuid"
	case col.TypName == "timestamptz" || col.TypName == "timestamp":
		s["type"], s["format"] = "string", "date-time"
	case col.TypName == "date":
		s["type"], s["format"] = "string", "date"
	default:
		s["type"] = "string"
	}
	if col.Nullable {
		s["nullable"] = true
	}
	s["x-pg-type"] = col.Type
	return s
}

func openAPIDoc(p *project, cat *Catalog) map[string]any {
	paths := map[string]any{}
	schemas := map[string]any{}
	listParams := []any{
		map[string]any{"name": "select", "in": "query", "schema": map[string]any{"type": "string"}, "description": "Columns and embedded relations: id,title,author(name)"},
		map[string]any{"name": "where", "in": "query", "schema": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"description": "column:operator:value; repeatable (AND)", "style": "form", "explode": true},
		map[string]any{"name": "or", "in": "query", "schema": map[string]any{"type": "string"}, "description": "a,b OR group of conditions"},
		map[string]any{"name": "order", "in": "query", "schema": map[string]any{"type": "string"}, "description": "column:asc|desc,…"},
		map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "maximum": maxLimit}},
		map[string]any{"name": "offset", "in": "query", "schema": map[string]any{"type": "integer", "maximum": maxOffset}},
		map[string]any{"name": "cursor", "in": "query", "schema": map[string]any{"type": "string"}},
		map[string]any{"name": "count", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"exact", "estimated"}}},
	}
	for _, t := range cat.Tables() {
		name := t.Name
		if t.Schema != cat.Schemas[0] {
			name = t.Schema + "." + t.Name
		}
		props := map[string]any{}
		var required []string
		for _, col := range t.Columns {
			props[col.Name] = jsonType(col)
			if !col.Nullable {
				required = append(required, col.Name)
			}
		}
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		schemas[name] = schema
		ref := map[string]any{"$ref": "#/components/schemas/" + name}
		list := map[string]any{"type": "object", "properties": map[string]any{
			"data": map[string]any{"type": "array", "items": ref}, "next_cursor": map[string]any{"type": "string"},
			"count": map[string]any{"type": "integer"}}}
		ops := map[string]any{"get": map[string]any{
			"summary": "Read " + name, "parameters": listParams,
			"responses": map[string]any{"200": map[string]any{"description": "Rows", "content": map[string]any{"application/json": map[string]any{"schema": list}}}},
		}}
		if t.Kind == kindTable || t.Kind == kindPart || (t.Kind == kindView && t.Invoker) {
			body := map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{
				"oneOf": []any{ref, map[string]any{"type": "array", "items": ref, "maxItems": maxWriteRows}}}}}}
			written := map[string]any{"description": "The rows written", "content": map[string]any{"application/json": map[string]any{
				"schema": map[string]any{"type": "object", "properties": map[string]any{"affected": map[string]any{"type": "integer"},
					"data": map[string]any{"type": "array", "items": ref}}}}}}
			filtered := []any{
				map[string]any{"name": "where", "in": "query", "required": true, "schema": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
				map[string]any{"name": "max_affected", "in": "query", "schema": map[string]any{"type": "integer", "default": defaultMaxAffected}},
				map[string]any{"name": "return", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"representation", "minimal"}}},
			}
			ops["post"] = map[string]any{"summary": "Insert into " + name + " (upsert with on_conflict)", "requestBody": body,
				"parameters": []any{map[string]any{"name": "on_conflict", "in": "query", "schema": map[string]any{"type": "string"}},
					map[string]any{"name": "resolution", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"merge", "ignore"}}},
					map[string]any{"name": "return", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"representation", "minimal"}}}},
				"responses": map[string]any{"201": written}}
			ops["patch"] = map[string]any{"summary": "Update " + name + " rows matching a filter", "parameters": filtered,
				"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": ref}}}, "responses": map[string]any{"200": written}}
			ops["delete"] = map[string]any{"summary": "Delete " + name + " rows matching a filter", "parameters": filtered, "responses": map[string]any{"200": written}}
		}
		paths["/data/v1/"+name] = ops
		paths["/data/v1/"+name+"/query"] = map[string]any{"post": map[string]any{
			"summary":   "Read " + name + " with a JSON query",
			"responses": map[string]any{"200": map[string]any{"description": "Rows", "content": map[string]any{"application/json": map[string]any{"schema": list}}}},
		}}
		if len(t.PK) == 1 {
			paths["/data/v1/"+name+"/{"+t.PK[0]+"}"] = map[string]any{"get": map[string]any{
				"summary": "One " + name + " by " + t.PK[0],
				"parameters": []any{map[string]any{"name": t.PK[0], "in": "path", "required": true, "schema": jsonType(t.Col(t.PK[0]))},
					map[string]any{"name": "select", "in": "query", "schema": map[string]any{"type": "string"}}},
				"responses": map[string]any{"200": map[string]any{"description": "The row", "content": map[string]any{"application/json": map[string]any{
					"schema": map[string]any{"type": "object", "properties": map[string]any{"data": ref}}}}}},
			}}
		}
	}
	for key, fs := range cat.Functions {
		f := fs[0]
		name := f.Name
		if f.Schema != cat.Schemas[0] {
			name = key
		}
		props := map[string]any{}
		for _, a := range f.Args {
			if a.Name != "" {
				props[a.Name] = jsonType(&Column{TypName: a.TypName, Category: a.Category, Type: a.Type})
			}
		}
		op := map[string]any{"summary": "Call " + name, "requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{
			"schema": map[string]any{"type": "object", "properties": props}}}},
			"responses": map[string]any{"200": map[string]any{"description": "The result as data"}}}
		item := map[string]any{"post": op}
		if f.Volatile != 'v' {
			item["get"] = map[string]any{"summary": "Call " + name + " (stable)", "responses": map[string]any{"200": map[string]any{"description": "The result as data"}}}
		}
		paths["/data/v1/rpc/"+name] = item
	}
	doc := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": p.cfg.Ref + " data API", "version": "v1"},
		"servers": []any{map[string]any{"url": "/"}},
		"paths":   paths,
		"components": map[string]any{
			"schemas": schemas,
			"securitySchemes": map[string]any{
				"apikey": map[string]any{"type": "apiKey", "in": "header", "name": "apikey"},
				"user":   map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "JWT"},
			},
		},
		"security": []any{map[string]any{"apikey": []string{}}},
	}
	// Round-trip so the map renders deterministically sorted.
	b, _ := json.Marshal(doc)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
