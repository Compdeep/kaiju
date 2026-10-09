package api

import (
	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/internal/db"
)

// Reading the intent table out of this application's database.
//
// The engine asks for an agent.IntentSource and knows nothing about where the
// ranks are kept, which is what lets an application embedding it keep them
// somewhere else entirely. This is the small piece that says "here they are in
// mine", and it is the only place the two row types meet.
//
// It lives here rather than in the daemon's main package because two callers
// need it: the daemon at startup, and the intent API after every write — a rank
// added or a tool pinned has to reach the engine without a restart, and the API
// cannot reach into package main for the adapter.

// intentSource adapts a database to what the engine reads.
type intentSource struct{ db *db.DB }

// IntentsFrom returns the engine's view of a database's intent table.
func IntentsFrom(d *db.DB) agent.IntentSource { return intentSource{db: d} }

/*
 * ListIntents returns every rank, in the engine's shape.
 * return: the ranks, or the error the database gave.
 */
func (s intentSource) ListIntents() ([]agent.Intent, error) {
	rows, err := s.db.ListIntents()
	if err != nil {
		return nil, err
	}
	out := make([]agent.Intent, 0, len(rows))
	for _, r := range rows {
		out = append(out, agent.Intent{
			Name:              r.Name,
			Rank:              r.Rank,
			PromptDescription: r.PromptDescription,
			IsDefault:         r.IsDefault,
		})
	}
	return out, nil
}

/*
 * ListToolIntents returns the per-tool overrides.
 * return: tool name to intent name, or the error the database gave.
 */
func (s intentSource) ListToolIntents() (map[string]string, error) {
	return s.db.ListToolIntents()
}
