package llm

import "log"

// Sizing a reply against the model that will answer it.
//
// Every caller picks a max_tokens for its own reply — 16 for a routing
// classifier, 16384 for a code generator. The model never sees that number; it
// is where the provider stops generating. So a number chosen without reference
// to the model is wrong in both directions: too high and the provider rejects
// the request, too low and a reply that had to be parsed arrives cut in half.
//
// capReply keeps the caller's choice and only lowers it where the model says it
// cannot be met. It runs on every send, so a caller cannot forget it.

// replyFloor is the smallest cap capReply will settle on. A prompt that nearly
// fills the window would otherwise compute a cap of a few tokens, which fails
// in a way that looks like the model refusing to answer.
const replyFloor = 256

// promptHeadroom covers what the estimate cannot see — tool schemas, the
// provider's own framing — so a reply sized against the remaining window does
// not run into the end of it.
const promptHeadroom = 2000

/*
 * capReply lowers a request's reply cap to what the model can actually produce.
 * desc: Two ceilings apply. The model's published maximum reply is the first.
 *       The room left in its context window after the prompt is the second,
 *       estimated at four characters per token. The lower wins, and the result
 *       is never raised above what the caller asked for — a caller that wants a
 *       short reply keeps getting one.
 *
 *       Nothing happens without Limits, or for a model it does not carry, so a
 *       client given no catalog behaves exactly as it did before this existed.
 * param: req - the request, whose MaxTokens is lowered in place.
 */
func (c *Client) capReply(req *ChatRequest) {
	if c == nil || c.limits == nil || req == nil || req.MaxTokens <= 0 {
		return
	}
	model := req.Model
	if model == "" {
		model = c.model
	}
	if model == "" {
		return
	}
	contextTokens, maxOutput := c.limits(model)
	if contextTokens == 0 && maxOutput == 0 {
		return
	}

	ceiling := req.MaxTokens
	if maxOutput > 0 && maxOutput < ceiling {
		ceiling = maxOutput
	}
	if contextTokens > 0 {
		if room := contextTokens - promptTokens(req.Messages) - promptHeadroom; room < ceiling {
			ceiling = room
		}
	}
	if ceiling < replyFloor {
		ceiling = replyFloor
	}
	if ceiling >= req.MaxTokens {
		return
	}
	log.Printf("[llm] %s: reply cap %d → %d (window %d, max reply %d, prompt ~%d)",
		model, req.MaxTokens, ceiling, contextTokens, maxOutput, promptTokens(req.Messages))
	req.MaxTokens = ceiling
}

// promptTokens estimates the size of a message list at four characters per
// token. Tool schemas are not counted; promptHeadroom stands in for them.
func promptTokens(messages []Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	return chars / 4
}
