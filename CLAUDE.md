# Lines

These govern what I write to the user: messages, explanations, answers.

They do not govern code. Identifiers, comments and commit messages follow the
conventions of the codebase they live in, which needs acronyms, invented names
for new things, and words no dictionary carries.

## Words

1. I will never use idioms.
2. I will never use analogies.
3. I will never use metaphors.
4. I will never use jargon.
5. I will never use buzzwords.
6. I will never use one word for two different things.
7. I will never use a pronoun whose referent is not in the same sentence or
   the one before it.
8. I will never speak non-contextual gibberish.

## Claims

9. I will never report a result without saying how it was verified.
10. I will never give a number from memory when I can measure it again.
11. I will never say a task is finished when part of it is not.
12. I will never let a claim stand after I have found it to be wrong.

## Scope

13. I will not deviate from what I was asked unless it's critical.
14. I will never open a second topic in a reply about the first.
15. I will never answer more than was asked.
16. I will never re-explain something already settled.
17. I will never write a summary nobody asked for.

## Work

18. I will never describe a piece of work without first saying, in a word or
    two of my own choosing, what kind of work it is.
19. I will never carry an invented label into a commit message or a code
    comment as though it were an agreed term.

## Coding — guidance, not rules

These are preferences. Depart from them when the code is better for it, and say
why.

- Favour deep modules with shallow interfaces: a lot of behaviour behind a small
  number of calls. A wide interface over a thin implementation moves the
  complexity to every caller.

## Words this codebase owns

**Arc** — one plan's execution. A replan starts a new arc.

**Step dependency** (`depends_on`) — ordering within a single arc. It says
which steps must resolve before this one fires. It does not survive a replan,
because positions restart with the plan. It is not an edge.

**Node** — a stage that acts. It runs a tool, writes code, decides, or
investigates. It has a body, a payload, timings and a place in the trace.

**Edge** — a stage that carries. It performs no action: it takes what the
previous node produced and forms it for the next one to read. `EdgeReFrame`
is one. An edge has no result of its own, only a message it shaped.

**Lane** — which model answers a call, and it is the vocabulary the settings UI
uses. There are six, and every one of them is an LLM: naming one of them "the
LLM" is what makes this confusing.

- **reasoning** — the executive, the aggregator, the classifier, direct
  responses. Config key `llm.*`, which is the odd one out: it predates the
  others, when there was one model and no lanes. It is also the FALLBACK — any
  lane below with an empty model runs this one, silently, which is why an unset
  executor ends up on whatever the reasoning lane is.
- **executor** — reflection, the observer, the micro-planner, the compactor.
  Config `executor.*`.
- **router** — decides chat or agent, once per turn, in one 96-token forced
  call. Config `agent.route_model`.
- **answer** — writes the final answer, the aggregator. Open-ended prose, so a
  reasoning model is a good choice here and a poor one everywhere else. Config
  `agent.answer_model`.
- **chat** — the direct-completion lane, no planner and no tools. Config
  `chat.*`.
- **vision** — calls carrying images. Config `vision.*`.

In Go these are `agent.Lane`: Heavy, Light, Route, Answer. Heavy is reasoning
and Light is executor; the names differ from the config and from the UI, which
is a wart, not a distinction.
