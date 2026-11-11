package llm

import "strings"

// A model id may name how the model should think, after a plus sign:
//
//	qwen.qwen3-32b+think      thinking on
//	qwen.qwen3-32b+nothink    thinking off
//	openai.gpt-oss-120b+low   thinking on, at that effort
//
// so a picker can offer one model as several entries and a lane configured with
// one of them gets that thinking without any other setting. The client sends the
// id before the plus sign and asks the catalog about that id only.

const variantSep = "+"

/*
 * SplitVariant reads the thinking a model id names.
 * desc: Only the words below are variants. Any other text after a plus sign is
 *       part of the id, so an id that happens to contain one is sent unchanged.
 * param: model - the id as configured.
 * return: the id to send, and the thinking it names, or nil for none.
 */
func SplitVariant(model string) (string, *Reasoning) {
	i := strings.LastIndex(model, variantSep)
	if i <= 0 {
		return model, nil
	}
	base, word := model[:i], strings.ToLower(model[i+len(variantSep):])
	switch word {
	case "think":
		return base, &Reasoning{Want: WantOn}
	case "nothink":
		return base, &Reasoning{Want: WantOff}
	}
	if e, ok := ParseEffort(word); ok {
		return base, &Reasoning{Want: WantOn, Effort: e}
	}
	return model, nil
}

// BaseModel is the id SplitVariant sends: the model id without its variant.
func BaseModel(model string) string {
	base, _ := SplitVariant(model)
	return base
}

// VariantID is the id that names a model with that thinking: "think",
// "nothink", or an effort word.
func VariantID(model, word string) string {
	return BaseModel(model) + variantSep + word
}

/*
 * takeVariant moves the thinking a model id names onto the request.
 * desc: Run first, before anything looks the model up. What the caller set on
 *       Think wins, field by field: a lane that turns thinking off for a small
 *       forced call keeps it off whatever the id says, and an effort the
 *       caller left unset is taken from the id.
 * param: req - the request, whose Model and Think are set in place.
 */
func (c *Client) takeVariant(req *ChatRequest) {
	if c == nil || req == nil {
		return
	}
	if req.Model == "" {
		req.Model = c.model
	}
	base, v := SplitVariant(req.Model)
	if v == nil {
		return
	}
	req.Model = base
	if req.Think == nil {
		req.Think = v
		return
	}
	merged := *req.Think
	if merged.Want == WantAuto {
		merged.Want = v.Want
	}
	if merged.Effort == EffortUnset && merged.Want == WantOn {
		merged.Effort = v.Effort
	}
	req.Think = &merged
}
