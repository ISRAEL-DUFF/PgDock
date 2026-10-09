package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/store"
)

// The RLS sample apps (V4.1 §12.1): three small apps, each checked for
// what anon, two users and the service role can do through the data API,
// storage and realtime.

// rlsUser creates and signs in a user of sp's project.
func rlsUser(t *testing.T, sp *storageProject, email string) (string, uuid.UUID) {
	t.Helper()
	api := authAPI{t: t, ed: sp.ed, ref: sp.ref, key: sp.admin.key}
	if r := api.post("/auth/v1/admin/users", fmt.Sprintf(`{"email":%q,"password":"password-123456","email_confirm":true}`, email)); r.Code != 201 {
		t.Fatalf("create %s: %d %s", email, r.Code, r.Body)
	}
	r := authAPI{t: t, ed: sp.ed, ref: sp.ref, key: sp.anon.key}.post("/auth/v1/signin/password",
		fmt.Sprintf(`{"email":%q,"password":"password-123456"}`, email))
	if r.Code != 200 {
		t.Fatalf("sign in %s: %d %s", email, r.Code, r.Body)
	}
	return r.Session.AccessToken, r.Session.User.ID
}

// rlsSQL runs the app's schema as the project owner.
func rlsSQL(t *testing.T, sp *storageProject, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	app := sp.e.MustConnect(sp.owner)
	defer app.Close(ctx)
	for _, st := range stmts {
		if _, err := app.Exec(ctx, st); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
}

// rows reads a data API list as a field of each row, sorted.
func rows(t *testing.T, f files, path, token, field string) []string {
	t.Helper()
	r := f.do("GET", path, nil, token)
	if r.Code != http.StatusOK {
		t.Fatalf("GET %s: %s", path, r)
	}
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	var vals []string
	for _, row := range out.Data {
		vals = append(vals, fmt.Sprint(row[field]))
	}
	sort.Strings(vals)
	return vals
}

func same(got []string, want ...string) bool {
	sort.Strings(want)
	return strings.Join(got, ",") == strings.Join(want, ",")
}

// subscribe joins topic for changes to table, as token.
func subscribe(t *testing.T, sp *storageProject, topic, table, token string) *rtClient {
	t.Helper()
	c := connectRT(t, sp.ed, sp.ref, sp.anon.key)
	cfg := map[string]any{"postgres_changes": []map[string]any{{"event": "*", "schema": "public", "table": table}}}
	if st, resp := c.join(topic, cfg, token); st != "ok" {
		t.Fatalf("subscribe to %s: %s %s", table, st, resp)
	}
	c.next(5*time.Second, "subscribed", func(m rtMessage) bool {
		return m.Event == "system" && strings.Contains(string(m.Payload), "Subscribed")
	})
	return c
}

func changeWith(field, value string) func(rtMessage) bool {
	return func(m rtMessage) bool {
		if m.Event != "postgres_changes" {
			return false
		}
		var c rtChange
		_ = json.Unmarshal(m.Payload, &c)
		return fmt.Sprint(c.Data.Record[field]) == value
	}
}

// TestRLSTodoApp: each user's todos, their attachments in a private bucket,
// and their changes in realtime, are theirs alone.
func TestRLSTodoApp(t *testing.T) {
	sp := newStorageProject(t, "todo-app", false)
	user := store.UserRole(sp.db)
	rlsSQL(t, sp,
		`CREATE TABLE todos (id serial PRIMARY KEY, owner uuid NOT NULL DEFAULT pgd_auth.uid(), body text NOT NULL)`,
		`ALTER TABLE todos ENABLE ROW LEVEL SECURITY`,
		fmt.Sprintf(`CREATE POLICY own ON todos FOR ALL TO %q USING (owner = pgd_auth.uid()) WITH CHECK (owner = pgd_auth.uid())`, user),
		`SELECT pgd_realtime.enable('todos')`,
		// Attachments live under <todo id>/: whoever owns the todo.
		fmt.Sprintf(`CREATE POLICY todo_files ON pgd_storage.objects FOR ALL TO %q
		   USING (bucket = 'attachments' AND EXISTS (SELECT 1 FROM todos WHERE id::text = pgd_storage.folder(path, 1)))
		   WITH CHECK (bucket = 'attachments' AND EXISTS (SELECT 1 FROM todos WHERE id::text = pgd_storage.folder(path, 1)))`, user),
	)
	if r := sp.admin.json("POST", "/storage/v1/bucket", `{"id":"attachments"}`, ""); r.Code != 200 && r.Code != 201 {
		t.Fatalf("bucket: %s", r)
	}
	aliceRT := subscribe(t, sp, "realtime:todos", "todos", sp.alice)
	bobRT := subscribe(t, sp, "realtime:todos", "todos", sp.bob)

	insert := func(token, body string) string {
		t.Helper()
		r := sp.anon.json("POST", "/data/v1/todos", fmt.Sprintf(`{"body":%q}`, body), token)
		var out struct {
			Data []struct {
				ID int `json:"id"`
			} `json:"data"`
		}
		if r.Code != http.StatusCreated || json.Unmarshal(r.Body, &out) != nil || len(out.Data) != 1 {
			t.Fatalf("insert %s: %s", body, r)
		}
		return fmt.Sprint(out.Data[0].ID)
	}
	aliceTodo, bobTodo := insert(sp.alice, "alice's"), insert(sp.bob, "bob's")

	// Data: each sees their own; anon none; the service role all.
	if got := rows(t, sp.anon, "/data/v1/todos", sp.alice, "body"); !same(got, "alice's") {
		t.Fatalf("alice's todos: %v", got)
	}
	if got := rows(t, sp.anon, "/data/v1/todos", sp.bob, "body"); !same(got, "bob's") {
		t.Fatalf("bob's todos: %v", got)
	}
	if r := sp.anon.do("GET", "/data/v1/todos", nil, ""); r.Code == http.StatusOK && strings.Contains(string(r.Body), "alice") {
		t.Fatalf("anon read todos: %s", r)
	}
	if got := rows(t, sp.admin, "/data/v1/todos", "", "body"); !same(got, "alice's", "bob's") {
		t.Fatalf("service's todos: %v", got)
	}
	// Bob can't write a todo as alice.
	if r := sp.anon.json("POST", "/data/v1/todos", fmt.Sprintf(`{"body":"forged","owner":%q}`, sp.aliceID), sp.bob); r.Code < 400 {
		t.Fatalf("bob inserted alice's todo: %s", r)
	}

	// Storage: attachments under one's own todos only.
	put := func(todo, token string) fileResult {
		return sp.anon.do("POST", "/storage/v1/object/attachments/"+todo+"/note.txt", strings.NewReader("for "+todo), token, "Content-Type", "text/plain")
	}
	if r := put(aliceTodo, sp.alice); r.Code != 200 {
		t.Fatalf("alice attaches: %s", r)
	}
	if r := put(aliceTodo, sp.bob); r.Code == 200 {
		t.Fatalf("bob attached to alice's todo: %s", r)
	}
	if r := put(bobTodo, sp.bob); r.Code != 200 {
		t.Fatalf("bob attaches: %s", r)
	}
	get := func(todo, token string) int {
		return sp.anon.do("GET", "/storage/v1/object/attachments/"+todo+"/note.txt", nil, token).Code
	}
	if get(aliceTodo, sp.alice) != 200 || get(aliceTodo, sp.bob) == 200 || get(aliceTodo, "") == 200 || get(bobTodo, sp.alice) == 200 {
		t.Fatalf("attachment reads: alice %d, bob %d, anon %d, alice→bob's %d",
			get(aliceTodo, sp.alice), get(aliceTodo, sp.bob), get(aliceTodo, ""), get(bobTodo, sp.alice))
	}
	if r := sp.admin.do("GET", "/storage/v1/object/attachments/"+bobTodo+"/note.txt", nil, ""); r.Code != 200 {
		t.Fatalf("the service reads bob's attachment: %s", r)
	}

	// Realtime: own rows only.
	aliceRT.next(10*time.Second, "alice's insert", changeWith("body", "alice's"))
	bobRT.next(10*time.Second, "bob's insert", changeWith("body", "bob's"))
	aliceRT.quiet(time.Second, "bob's todo at alice", changeWith("body", "bob's"))
	bobRT.quiet(100*time.Millisecond, "alice's todo at bob", changeWith("body", "alice's"))
}

// TestRLSMarketplace: sellers list publicly, buyers order privately, and
// invoices and new-order notifications reach only the two parties.
func TestRLSMarketplace(t *testing.T) {
	sp := newStorageProject(t, "market", false)
	user, anon := store.UserRole(sp.db), store.AnonRole(sp.db)
	seller, sellerID := sp.alice, sp.aliceID // alice sells
	buyer, buyerID := sp.bob, sp.bobID       // bob buys
	other, otherID := rlsUser(t, sp, "carol@example.com")
	rival, rivalID := rlsUser(t, sp, "dave@example.com") // another seller
	rlsSQL(t, sp,
		`CREATE TABLE sellers (user_id uuid PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE listings (id serial PRIMARY KEY, seller uuid NOT NULL REFERENCES sellers, title text NOT NULL, price int NOT NULL)`,
		`CREATE TABLE orders (id serial PRIMARY KEY, listing_id int NOT NULL REFERENCES listings, buyer uuid NOT NULL DEFAULT pgd_auth.uid(), qty int NOT NULL)`,
		`ALTER TABLE sellers ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE listings ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE orders ENABLE ROW LEVEL SECURITY`,
		fmt.Sprintf(`CREATE POLICY read ON sellers FOR SELECT TO %q, %q USING (true)`, anon, user),
		fmt.Sprintf(`CREATE POLICY read ON listings FOR SELECT TO %q, %q USING (true)`, anon, user),
		fmt.Sprintf(`CREATE POLICY sell ON listings FOR INSERT TO %q WITH CHECK (seller = pgd_auth.uid())`, user),
		fmt.Sprintf(`CREATE POLICY edit ON listings FOR UPDATE TO %q USING (seller = pgd_auth.uid()) WITH CHECK (seller = pgd_auth.uid())`, user),
		fmt.Sprintf(`CREATE POLICY parties ON orders FOR SELECT TO %q
		   USING (buyer = pgd_auth.uid() OR EXISTS (SELECT 1 FROM listings l WHERE l.id = listing_id AND l.seller = pgd_auth.uid()))`, user),
		fmt.Sprintf(`CREATE POLICY buy ON orders FOR INSERT TO %q WITH CHECK (buyer = pgd_auth.uid())`, user),
		fmt.Sprintf(`INSERT INTO sellers VALUES ('%s', 'Alice''s Fabrics'), ('%s', 'Dave''s Shoes')`, sellerID, rivalID),
		`SELECT pgd_realtime.enable('orders')`,
		// Invoices under <order id>/: the order's buyer and seller read them.
		fmt.Sprintf(`CREATE POLICY invoice_parties ON pgd_storage.objects FOR SELECT TO %q
		   USING (bucket = 'invoices' AND EXISTS (SELECT 1 FROM orders o WHERE o.id::text = pgd_storage.folder(path, 1)))`, user),
	)
	for _, b := range []string{`{"id":"listing-images","public":true}`, `{"id":"invoices"}`} {
		if r := sp.admin.json("POST", "/storage/v1/bucket", b, ""); r.Code != 200 && r.Code != 201 {
			t.Fatalf("bucket %s: %s", b, r)
		}
	}

	// Sellers list; a buyer can't list as a seller or edit another's listing.
	list := func(token, title string) string {
		t.Helper()
		r := sp.anon.json("POST", "/data/v1/listings", fmt.Sprintf(`{"seller":%q,"title":%q,"price":5000}`, map[string]uuid.UUID{seller: sellerID, rival: rivalID}[token], title), token)
		var out struct {
			Data []struct {
				ID int `json:"id"`
			} `json:"data"`
		}
		if r.Code != http.StatusCreated || json.Unmarshal(r.Body, &out) != nil {
			t.Fatalf("list %s: %s", title, r)
		}
		return fmt.Sprint(out.Data[0].ID)
	}
	fabric, shoes := list(seller, "Ankara fabric"), list(rival, "Leather sandals")
	if r := sp.anon.json("POST", "/data/v1/listings", fmt.Sprintf(`{"seller":%q,"title":"fake","price":1}`, sellerID), buyer); r.Code < 400 {
		t.Fatalf("a buyer listed as the seller: %s", r)
	}
	if r := sp.anon.json("PATCH", "/data/v1/listings/"+fabric, `{"price":1}`, rival); r.Code == 200 && strings.Contains(string(r.Body), `"affected":1`) {
		t.Fatalf("a rival repriced alice's listing: %s", r)
	}
	if r := sp.admin.do("POST", "/storage/v1/object/listing-images/"+fabric+".png", strings.NewReader(string(pngOf(t, 8, 8))), "", "Content-Type", "image/png"); r.Code != 200 {
		t.Fatalf("listing image: %s", r)
	}

	// Anon browses listings and their images, never orders.
	if got := rows(t, sp.anon, "/data/v1/listings", "", "title"); !same(got, "Ankara fabric", "Leather sandals") {
		t.Fatalf("anon's listings: %v", got)
	}
	keyless := files{t: t, ed: sp.ed, ref: sp.ref}
	if r := keyless.do("GET", "/storage/v1/object/public/listing-images/"+fabric+".png", nil, ""); r.Code != 200 {
		t.Fatalf("anon's listing image: %s", r)
	}

	// Realtime: each seller hears of orders on their own listings only.
	sellerRT := subscribe(t, sp, "realtime:orders", "orders", seller)
	rivalRT := subscribe(t, sp, "realtime:orders", "orders", rival)

	// Buyers order; one can't order in another's name.
	order := func(token, listing string, qty int) string {
		t.Helper()
		r := sp.anon.json("POST", "/data/v1/orders", fmt.Sprintf(`{"listing_id":%s,"qty":%d}`, listing, qty), token)
		var out struct {
			Data []struct {
				ID int `json:"id"`
			} `json:"data"`
		}
		if r.Code != http.StatusCreated || json.Unmarshal(r.Body, &out) != nil {
			t.Fatalf("order: %s", r)
		}
		return fmt.Sprint(out.Data[0].ID)
	}
	bobOrder, carolOrder := order(buyer, fabric, 2), order(other, fabric, 3)
	daveSale := order(other, shoes, 1)
	if r := sp.anon.json("POST", "/data/v1/orders", fmt.Sprintf(`{"listing_id":%s,"qty":1,"buyer":%q}`, fabric, otherID), buyer); r.Code < 400 {
		t.Fatalf("bob ordered as carol: %s", r)
	}
	if r := sp.anon.json("POST", "/data/v1/orders", fmt.Sprintf(`{"listing_id":%s,"qty":1}`, fabric), ""); r.Code < 400 {
		t.Fatalf("anon ordered: %s", r)
	}

	// Who sees which orders.
	if got := rows(t, sp.anon, "/data/v1/orders", buyer, "id"); !same(got, bobOrder) {
		t.Fatalf("bob's orders: %v", got)
	}
	if got := rows(t, sp.anon, "/data/v1/orders", other, "id"); !same(got, carolOrder, daveSale) {
		t.Fatalf("carol's orders: %v", got)
	}
	if got := rows(t, sp.anon, "/data/v1/orders", seller, "id"); !same(got, bobOrder, carolOrder) {
		t.Fatalf("alice's sales: %v", got)
	}
	if got := rows(t, sp.anon, "/data/v1/orders", rival, "id"); !same(got, daveSale) {
		t.Fatalf("dave's sales: %v", got)
	}
	if r := sp.anon.do("GET", "/data/v1/orders", nil, ""); r.Code == http.StatusOK && strings.Contains(string(r.Body), `"qty"`) {
		t.Fatalf("anon saw orders: %s", r)
	}
	if got := rows(t, sp.admin, "/data/v1/orders", "", "id"); len(got) != 3 {
		t.Fatalf("the service's orders: %v", got)
	}

	// Invoices: written by the service, read by the parties to the order.
	for _, o := range []string{bobOrder, carolOrder} {
		if r := sp.admin.do("POST", "/storage/v1/object/invoices/"+o+"/invoice.pdf", strings.NewReader("%PDF-1.4 invoice "+o), "", "Content-Type", "application/pdf"); r.Code != 200 {
			t.Fatalf("invoice %s: %s", o, r)
		}
	}
	invoice := func(o, token string) int {
		return sp.anon.do("GET", "/storage/v1/object/invoices/"+o+"/invoice.pdf", nil, token).Code
	}
	for name, c := range map[string]struct {
		order, token string
		ok           bool
	}{
		"bob his own": {bobOrder, buyer, true}, "bob carol's": {carolOrder, buyer, false},
		"alice her sale": {carolOrder, seller, true}, "dave alice's sale": {bobOrder, rival, false}, "anon": {bobOrder, "", false},
	} {
		if got := invoice(c.order, c.token); (got == 200) != c.ok {
			t.Errorf("invoice, %s: %d", name, got)
		}
	}
	// Buyers can't write invoices.
	if r := sp.anon.do("POST", "/storage/v1/object/invoices/"+bobOrder+"/forged.pdf", strings.NewReader("%PDF"), buyer, "Content-Type", "application/pdf"); r.Code == 200 {
		t.Fatalf("a buyer wrote an invoice: %s", r)
	}

	sellerRT.next(10*time.Second, "bob's order at alice", changeWith("id", bobOrder))
	sellerRT.next(10*time.Second, "carol's order at alice", changeWith("id", carolOrder))
	rivalRT.next(10*time.Second, "dave's sale at dave", changeWith("id", daveSale))
	sellerRT.quiet(time.Second, "dave's sale at alice", changeWith("id", daveSale))
	rivalRT.quiet(100*time.Millisecond, "alice's sales at dave", func(m rtMessage) bool {
		return changeWith("id", bobOrder)(m) || changeWith("id", carolOrder)(m)
	})
	_ = buyerID
}

// TestRLSChat: a room's messages, private channel, broadcasts and presence
// are its members' only, and a removed member stops hearing it.
func TestRLSChat(t *testing.T) {
	sp := newStorageProject(t, "chat", false)
	user := store.UserRole(sp.db)
	carol, carolID := rlsUser(t, sp, "carol@example.com")
	rlsSQL(t, sp,
		`CREATE TABLE rooms (id text PRIMARY KEY)`,
		`CREATE TABLE members (room text NOT NULL REFERENCES rooms, user_id uuid NOT NULL, PRIMARY KEY (room, user_id))`,
		`CREATE TABLE messages (id serial PRIMARY KEY, room text NOT NULL REFERENCES rooms, author uuid NOT NULL DEFAULT pgd_auth.uid(), body text NOT NULL)`,
		`ALTER TABLE members ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE messages ENABLE ROW LEVEL SECURITY`,
		// A security-definer check keeps the members policy from recursing.
		`CREATE FUNCTION public.is_member(r text) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public
		   AS $$ SELECT EXISTS (SELECT 1 FROM members WHERE room = r AND user_id = pgd_auth.uid()) $$`,
		fmt.Sprintf(`CREATE POLICY see_room ON members FOR SELECT TO %q USING (public.is_member(room))`, user),
		fmt.Sprintf(`CREATE POLICY read ON messages FOR SELECT TO %q USING (public.is_member(room))`, user),
		fmt.Sprintf(`CREATE POLICY post ON messages FOR INSERT TO %q WITH CHECK (author = pgd_auth.uid() AND public.is_member(room))`, user),
		fmt.Sprintf(`CREATE POLICY join_room ON pgd_realtime.channel_access FOR SELECT TO %q USING (public.is_member(topic))`, user),
		fmt.Sprintf(`CREATE POLICY speak ON pgd_realtime.channel_access FOR INSERT TO %q WITH CHECK (public.is_member(topic))`, user),
		`INSERT INTO rooms VALUES ('general'), ('ops')`,
		fmt.Sprintf(`INSERT INTO members VALUES ('general', '%s'), ('general', '%s'), ('ops', '%s')`, sp.aliceID, sp.bobID, carolID),
		`SELECT pgd_realtime.enable('messages')`,
	)

	// Data: members read and post; others neither.
	post := func(token, room, body string) int {
		return sp.anon.json("POST", "/data/v1/messages", fmt.Sprintf(`{"room":%q,"body":%q}`, room, body), token).Code
	}
	aliceRT := subscribe(t, sp, "realtime:messages", "messages", sp.alice)
	bobRT := subscribe(t, sp, "realtime:messages", "messages", sp.bob)
	carolRT := subscribe(t, sp, "realtime:messages", "messages", carol)
	if c := post(sp.alice, "general", "hello general"); c != http.StatusCreated {
		t.Fatalf("alice posts: %d", c)
	}
	if c := post(carol, "general", "gatecrash"); c < 400 {
		t.Fatalf("carol posted to a room she isn't in: %d", c)
	}
	if got := rows(t, sp.anon, "/data/v1/messages", sp.bob, "body"); !same(got, "hello general") {
		t.Fatalf("bob's messages: %v", got)
	}
	if got := rows(t, sp.anon, "/data/v1/messages", carol, "body"); len(got) != 0 {
		t.Fatalf("carol read general: %v", got)
	}
	if got := rows(t, sp.anon, "/data/v1/members", carol, "room"); !same(got, "ops") {
		t.Fatalf("carol's rooms: %v", got)
	}

	// Changes reach members only.
	aliceRT.next(10*time.Second, "the message at alice", changeWith("body", "hello general"))
	bobRT.next(10*time.Second, "the message at bob", changeWith("body", "hello general"))
	carolRT.quiet(time.Second, "general at carol", changeWith("body", "hello general"))

	// The room's private channel: members join, broadcast and see each
	// other's presence; carol can't join, and a public channel of the same
	// name hears nothing of it.
	private := map[string]any{"private": true, "broadcast": map[string]any{"self": false}, "presence": map[string]any{}}
	alice := connectRT(t, sp.ed, sp.ref, sp.anon.key)
	bob := connectRT(t, sp.ed, sp.ref, sp.anon.key)
	gate := connectRT(t, sp.ed, sp.ref, sp.anon.key)
	for name, c := range map[string]struct {
		cl    *rtClient
		token string
	}{"alice": {alice, sp.alice}, "bob": {bob, sp.bob}} {
		if st, resp := c.cl.join("realtime:general", map[string]any{"private": true, "broadcast": map[string]any{"self": false},
			"presence": map[string]any{"key": name}}, c.token); st != "ok" {
			t.Fatalf("%s joins general: %s %s", name, st, resp)
		}
	}
	if st, resp := gate.join("realtime:general", private, carol); st != "error" || !strings.Contains(string(resp), "not allowed") {
		t.Fatalf("carol joins general's private channel: %s %s", st, resp)
	}
	if st, _ := gate.join("realtime:general", map[string]any{"presence": map[string]any{"key": "carol"}}, carol); st != "ok" {
		t.Fatal("carol joins the public channel")
	}
	alice.send("realtime:general", "presence", map[string]any{"type": "presence", "event": "track", "payload": map[string]any{"status": "typing"}})
	bob.next(10*time.Second, "alice's presence at bob", func(m rtMessage) bool {
		return m.Event == "presence_diff" && strings.Contains(string(m.Payload), `"alice"`)
	})
	alice.send("realtime:general", "broadcast", map[string]any{"type": "broadcast", "event": "typing", "payload": map[string]any{"who": "alice"}})
	bob.next(10*time.Second, "alice's broadcast at bob", isBroadcast("typing"))
	gate.quiet(time.Second, "general's private traffic on the public channel", func(m rtMessage) bool {
		return isBroadcast("typing")(m) || (m.Event == "presence_diff" && strings.Contains(string(m.Payload), `"alice"`))
	})

	// Bob leaves the room: he stops getting its messages.
	rlsSQL(t, sp, fmt.Sprintf(`DELETE FROM members WHERE room = 'general' AND user_id = '%s'`, sp.bobID))
	if c := post(sp.alice, "general", "after bob left"); c != http.StatusCreated {
		t.Fatalf("alice posts again: %d", c)
	}
	aliceRT.next(10*time.Second, "the second message at alice", changeWith("body", "after bob left"))
	bobRT.quiet(2*time.Second, "a message after bob left", changeWith("body", "after bob left"))
	if got := rows(t, sp.anon, "/data/v1/messages", sp.bob, "body"); len(got) != 0 {
		t.Fatalf("bob still reads general: %v", got)
	}
	// Nor can he join its private channel again.
	again := connectRT(t, sp.ed, sp.ref, sp.anon.key)
	if st, _ := again.join("realtime:general", private, sp.bob); st != "error" {
		t.Fatal("bob rejoined general's private channel after leaving")
	}
}
