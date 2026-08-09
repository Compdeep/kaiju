package toolapi

import "encoding/json"

// Reading a tool parameter, whatever Go type it arrived as.
//
// Two paths reach a tool and they do not agree on shape. A plan is decoded from
// the planner's JSON, so a number arrives as float64 and an array as []any. An
// application also builds tool nodes in Go, where the natural value is an int
// or a []string — and at those call sites it writes float64(port) by hand to
// stay compatible, a convention that holds only while someone remembers it.
//
// A bare params["count"].(float64) fails on an int without saying so: the tool
// runs on its default and answers a question nobody asked. These read every
// shape, so the convention is no longer load-bearing.

// ParamNum returns a numeric parameter and whether it was present as a number.
func ParamNum(params map[string]any, key string) (float64, bool) {
	switch v := params[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

// ParamInt is ParamNum truncated, for the parameters that count things.
func ParamInt(params map[string]any, key string) (int, bool) {
	f, ok := ParamNum(params, key)
	return int(f), ok
}

// ParamStrings returns a string-array parameter, from either the decoded-JSON
// shape or the Go one. Non-string entries and empty strings are dropped, so a
// caller gets the values it can use and nothing else.
func ParamStrings(params map[string]any, key string) []string {
	switch v := params[key].(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
