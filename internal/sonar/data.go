package sonar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Config data is JSON text in the database. Some of it, written by hand in
// the installer's migrations, has unquoted property names and trailing
// commas, which Sonar's JSON library (Newtonsoft) accepts. Data is kept as
// an ordered tree so configs go back to the client with their properties in
// the order they were stored, and numbers keep their stored form.

// object is a JSON object that remembers its key order.
type object struct {
	keys []string
	vals map[string]any
}

func newObject() *object { return &object{vals: map[string]any{}} }

func (o *object) get(k string) any { return o.vals[k] }

func (o *object) has(k string) bool {
	_, ok := o.vals[k]
	return ok
}

// set replaces a value in place or appends a new key.
func (o *object) set(k string, v any) {
	if !o.has(k) {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *object) child(k string) *object {
	c, _ := o.vals[k].(*object)
	return c
}

func (o *object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (o *object) UnmarshalJSON(b []byte) error {
	v, err := parseJSON(string(b))
	if err != nil {
		return err
	}
	obj, ok := v.(*object)
	if !ok {
		return fmt.Errorf("expected a JSON object")
	}
	*o = *obj
	return nil
}

// parseJSON parses lenient JSON into *object, []any, json.Number, string,
// bool and nil values.
func parseJSON(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(strictJSON(s)))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		o := newObject()
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k, _ := kt.(string)
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			o.set(k, v)
		}
		_, err = dec.Token()
		return o, err
	case json.Delim('['):
		a := []any{}
		for dec.More() {
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err = dec.Token()
		return a, err
	}
	return t, nil
}

// strictJSON quotes bare property names and drops trailing commas.
func strictJSON(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			b.WriteString(s[i:min(j+1, len(s))])
			i = j
		case c == ',':
			j := i + 1
			for j < len(s) && isSpace(s[j]) {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
			b.WriteByte(c)
		case isIdentStart(c):
			j := i
			for j < len(s) && (isIdentStart(s[j]) || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			k := j
			for k < len(s) && isSpace(s[k]) {
				k++
			}
			if word := s[i:j]; k < len(s) && s[k] == ':' {
				b.WriteString(strconv.Quote(word))
			} else {
				b.WriteString(word)
			}
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// validData reports whether config data is a non-empty object, as Sonar's
// IsDataConfigValid does.
func validData(v any) bool {
	o, ok := v.(*object)
	return ok && len(o.keys) > 0
}

// num builds a JSON number the way Sonar writes floats (0.0, -6.0, 0.7071).
func num(f float64) json.Number {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return json.Number(s)
}

func state(enabled bool, value float64) *object {
	o := newObject()
	o.set("enabled", enabled)
	o.set("value", num(value))
	return o
}
