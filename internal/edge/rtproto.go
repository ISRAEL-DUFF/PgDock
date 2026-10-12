package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Realtime's wire format (V4 §6.1) is the Phoenix channels protocol that
// Supabase's realtime clients speak, so they connect unchanged: vsn 1.0.0
// sends each message as a JSON object, 2.0.0 as an array
// [join_ref, ref, topic, event, payload].

const (
	phxJoin      = "phx_join"
	phxLeave     = "phx_leave"
	phxReply     = "phx_reply"
	phxClose     = "phx_close"
	phxError     = "phx_error"
	phxHeartbeat = "heartbeat"
	phxTopic     = "phoenix"

	evAccessToken     = "access_token"
	evBroadcast       = "broadcast"
	evPresence        = "presence"
	evPresenceState   = "presence_state"
	evPresenceDiff    = "presence_diff"
	evPostgresChanges = "postgres_changes"
	evSystem          = "system"
)

// rtMsg is one message either way.
type rtMsg struct {
	JoinRef *string         `json:"join_ref"`
	Ref     *string         `json:"ref"`
	Topic   string          `json:"topic"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

// decodeMsg reads either encoding; v2 reports which.
func decodeMsg(b []byte) (m rtMsg, v2 bool, err error) {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) > 0 && b[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(b, &arr); err != nil {
			return m, true, err
		}
		if len(arr) != 5 {
			return m, true, errors.New("a message is [join_ref, ref, topic, event, payload]")
		}
		str := func(raw json.RawMessage) (*string, error) {
			if string(raw) == "null" {
				return nil, nil
			}
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, err
			}
			return &s, nil
		}
		if m.JoinRef, err = str(arr[0]); err != nil {
			return m, true, err
		}
		if m.Ref, err = str(arr[1]); err != nil {
			return m, true, err
		}
		if err := json.Unmarshal(arr[2], &m.Topic); err != nil {
			return m, true, err
		}
		if err := json.Unmarshal(arr[3], &m.Event); err != nil {
			return m, true, err
		}
		m.Payload = arr[4]
		return m, true, nil
	}
	err = json.Unmarshal(b, &m)
	return m, false, err
}

// encodeMsg writes m in the connection's encoding.
func encodeMsg(m rtMsg, v2 bool) []byte {
	if len(m.Payload) == 0 {
		m.Payload = json.RawMessage(`{}`)
	}
	if !v2 {
		b, _ := json.Marshal(m)
		return b
	}
	b, _ := json.Marshal([]any{m.JoinRef, m.Ref, m.Topic, m.Event, m.Payload})
	return b
}

func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// rtFilter is a postgres_changes filter: column=op.value (eq, neq, lt, lte,
// gt, gte, in).
type rtFilter struct {
	Column string
	Op     string
	Values []string
}

func parseFilter(s string) (*rtFilter, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	col, rest, ok := strings.Cut(s, "=")
	if !ok {
		return nil, fmt.Errorf("filter %q isn't column=op.value", s)
	}
	op, val, ok := strings.Cut(rest, ".")
	if !ok || col == "" {
		return nil, fmt.Errorf("filter %q isn't column=op.value", s)
	}
	f := &rtFilter{Column: col, Op: op}
	switch op {
	case "eq", "neq", "lt", "lte", "gt", "gte":
		f.Values = []string{val}
	case "in":
		inner, ok := strings.CutPrefix(val, "(")
		if inner, ok2 := strings.CutSuffix(inner, ")"); ok && ok2 {
			for _, v := range strings.Split(inner, ",") {
				f.Values = append(f.Values, strings.Trim(strings.TrimSpace(v), `"`))
			}
		} else {
			return nil, fmt.Errorf("filter %q: in takes (a,b,…)", s)
		}
		if len(f.Values) > 100 {
			return nil, fmt.Errorf("filter %q: in takes at most 100 values", s)
		}
	default:
		return nil, fmt.Errorf("filter %q: the operators are eq, neq, lt, lte, gt, gte and in", s)
	}
	return f, nil
}

// match reports whether record passes f.
func (f *rtFilter) match(record map[string]any) bool {
	if f == nil {
		return true
	}
	v, ok := record[f.Column]
	if !ok {
		return false
	}
	if v == nil {
		return f.Op == "neq" && f.Values[0] != "null" || f.Op == "eq" && f.Values[0] == "null"
	}
	s := jsonText(v)
	switch f.Op {
	case "eq":
		return s == f.Values[0]
	case "neq":
		return s != f.Values[0]
	case "in":
		for _, x := range f.Values {
			if s == x {
				return true
			}
		}
		return false
	}
	c := compare(s, f.Values[0])
	switch f.Op {
	case "lt":
		return c < 0
	case "lte":
		return c <= 0
	case "gt":
		return c > 0
	default:
		return c >= 0
	}
}

// jsonText is a JSON value as filters compare it.
func jsonText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// compare orders numbers as numbers and anything else as text.
func compare(a, b string) int {
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	if ea == nil && eb == nil {
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	}
	return strings.Compare(a, b)
}
