package llm

import (
	"bytes"
	"encoding/json"
	"slices"
)

/*
 * Keeping a schema's field order through closedSchema.
 *
 * closedSchema edits the schema as a map[string]any, and encoding/json writes a
 * map with its keys sorted. A strict decoder writes a reply's fields in the
 * order its schema lists them, so sorting changed what every strict stage was
 * asked to write first.
 *
 * Measured on the reflector (run c77f2d9d, 2026-11-12, qwen3.6 on OpenRouter):
 * sorted, the required `summary` came last. After `progress` the model went to
 * close the object, the grammar refused `}` because `summary` was missing, and
 * the next token it allowed was whitespace, then more of it, until the 8192-token
 * cap; the run ended on a failed reflection. Replayed: 2 of 8 sent as sorted
 * failed, and so did 2 of 8 at Qwen's own non-thinking sampling, so temperature
 * was not the cause; 24 of 24 with `summary` second were fine.
 *
 * The fix is here, not in each stage's schema: every stage is written in the
 * order its author chose, and that is the order sent.
 */

// orderedObject is a JSON object with its keys in document order.
type orderedObject struct {
	keys []string
	vals map[string]any
}

/*
 * keepAuthorsOrder rewrites closed so every object lists its keys in the order
 * the same object has them in authored.
 * desc: Objects are matched by position: the same key under the same parent,
 *       the same index in an array. Keys closedSchema added (additionalProperties,
 *       a required list it wrote) have no place in authored and follow the
 *       authored ones. A required list beside properties is put in the order of
 *       those properties.
 * param: authored - the schema as the stage wrote it.
 * param: closed - closedSchema's output.
 * return: closed in the authored order, or closed unchanged when either fails
 *         to parse.
 */
func keepAuthorsOrder(authored, closed json.RawMessage) json.RawMessage {
	a, err := decodeOrdered(authored)
	if err != nil {
		return closed
	}
	c, err := decodeOrdered(closed)
	if err != nil {
		return closed
	}
	reorder(c, a)
	var buf bytes.Buffer
	if err := encodeOrdered(&buf, c); err != nil {
		return closed
	}
	return buf.Bytes()
}

func reorder(node, authored any) {
	switch n := node.(type) {
	case *orderedObject:
		ao, _ := authored.(*orderedObject)
		if ao != nil {
			rank := func(k string) int {
				if i := slices.Index(ao.keys, k); i >= 0 {
					return i
				}
				return len(ao.keys)
			}
			slices.SortStableFunc(n.keys, func(x, y string) int { return rank(x) - rank(y) })
		}
		for _, k := range n.keys {
			var av any
			if ao != nil {
				av = ao.vals[k]
			}
			reorder(n.vals[k], av)
		}
		if props, ok := n.vals["properties"].(*orderedObject); ok {
			if req, ok := n.vals["required"].([]any); ok {
				slices.SortStableFunc(req, func(x, y any) int {
					xs, _ := x.(string)
					ys, _ := y.(string)
					return indexOr(props.keys, xs) - indexOr(props.keys, ys)
				})
			}
		}
	case []any:
		aa, _ := authored.([]any)
		for i := range n {
			var av any
			if i < len(aa) {
				av = aa[i]
			}
			reorder(n[i], av)
		}
	}
}

func indexOr(keys []string, k string) int {
	if i := slices.Index(keys, k); i >= 0 {
		return i
	}
	return len(keys)
}

func decodeOrdered(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return decodeValue(dec)
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &orderedObject{vals: map[string]any{}}
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
				if _, dup := o.vals[k]; !dup {
					o.keys = append(o.keys, k)
				}
				o.vals[k] = v
			}
			_, err := dec.Token() // '}'
			return o, err
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, err := dec.Token() // ']'
			return arr, err
		}
	}
	return tok, nil
}

func encodeOrdered(buf *bytes.Buffer, v any) error {
	switch n := v.(type) {
	case *orderedObject:
		buf.WriteByte('{')
		for i, k := range n.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			buf.Write(kb)
			buf.WriteByte(':')
			if err := encodeOrdered(buf, n.vals[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range n {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeOrdered(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		b, err := json.Marshal(n)
		if err != nil {
			return err
		}
		buf.Write(b)
	}
	return nil
}
