package tools

// Writing a tool once, for both callers.
//
// A tool's real output is a ToolMessage: the kind of thing it produced, whether
// it produced anything, the readable text, and its own payload. The DAG wants
// that message — it reads the status to build the coverage statement, resolves
// ${step.N.field} against the payload, and shows the model the readable text
// alone. Callers outside the DAG want a string.
//
// So a tool implements ExecuteTyped and satisfies Tool.Execute with the two
// lines below. Go has no virtual dispatch, so an embedded base type cannot call
// the embedder's ExecuteTyped for it — a plain function is the equivalent, and
// it cannot be got wrong the way a base type with an unset field can.
//
//	func (t *Thing) ExecuteTyped(ctx context.Context, p map[string]any) (tools.ToolMessage, error) {
//	        …
//	        return tools.ToolOK("listing", text, payload), nil
//	}
//
//	func (t *Thing) Execute(ctx context.Context, p map[string]any) (string, error) {
//	        return tools.StringResult(t.ExecuteTyped(ctx, p))
//	}

/*
 * StringResult adapts a typed result to the string Tool.Execute returns.
 * desc: Serialises the whole envelope, which is what a caller outside the DAG
 *       has always received. The DAG never reaches this — the dispatcher takes
 *       the ToolMessage directly, so nothing is marshalled and unmarshalled on
 *       the path that matters.
 *
 *       Takes the error too, so a call site is one line: the error passes
 *       through and the empty string goes with it.
 * param: msg - the tool's typed result.
 * param: err - the tool's error, returned unchanged.
 * return: the serialised envelope, or the error.
 */
func StringResult(msg ToolMessage, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return msg.JSON(), nil
}
