package transform

import (
	"encoding/json"
	"strings"
)

// ApplyMappings applies rules, in order, to a JSON object body. Bodies that are not JSON
// objects are returned unchanged, with ok false.
func ApplyMappings(body []byte, rules []MappingRule) (out []byte, ok bool) {
	if len(rules) == 0 {
		return body, false
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(body, &doc); err != nil || doc == nil {
		return body, false
	}

	for _, r := range rules {
		from := splitPath(r.From)
		to := splitPath(r.To)
		if len(to) == 0 {
			continue
		}
		value, found := getPath(doc, from)
		if found {
			deletePath(doc, from)
		} else if r.Default != "" {
			value = parseDefault(r.Default)
		} else {
			continue
		}
		setPath(doc, to, value)
	}

	data, err := json.Marshal(doc)
	if err != nil {
		return body, false
	}
	return data, true
}

func splitPath(p string) []string {
	p = strings.TrimPrefix(strings.TrimPrefix(p, "$"), ".")
	if p == "" {
		return nil
	}
	return strings.Split(p, ".")
}

func parseDefault(s string) interface{} {
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}

func getPath(doc map[string]interface{}, path []string) (interface{}, bool) {
	if len(path) == 0 {
		return nil, false
	}
	cur := doc
	for i, key := range path {
		v, ok := cur[key]
		if !ok {
			return nil, false
		}
		if i == len(path)-1 {
			return v, true
		}
		if cur, ok = v.(map[string]interface{}); !ok {
			return nil, false
		}
	}
	return nil, false
}

func deletePath(doc map[string]interface{}, path []string) {
	cur := doc
	for i, key := range path {
		if i == len(path)-1 {
			delete(cur, key)
			return
		}
		next, ok := cur[key].(map[string]interface{})
		if !ok {
			return
		}
		cur = next
	}
}

func setPath(doc map[string]interface{}, path []string, value interface{}) {
	cur := doc
	for i, key := range path {
		if i == len(path)-1 {
			cur[key] = value
			return
		}
		next, ok := cur[key].(map[string]interface{})
		if !ok {
			next = make(map[string]interface{})
			cur[key] = next
		}
		cur = next
	}
}
