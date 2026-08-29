package agent

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// Config.Environment must actually reach the prompts. It was possible to set it
// and have nothing happen — no error, no effect — which is the worst kind of
// broken for a configuration field.
func TestEnvironmentReachesPrompts(t *testing.T) {
	a := &Agent{environment: func() string { return "machines: alpha, beta" }}
	got := a.environmentSection()
	if !strings.Contains(got, "alpha") {
		t.Fatalf("Config.Environment did not reach the prompt section: %q", got)
	}
	if !strings.HasPrefix(got, "\n\n") {
		t.Errorf("expected a leading blank line so it appends cleanly: %q", got)
	}
}

// The application's half is optional. The date is not: a stage that writes a
// parameter needs it whether or not the application had anything to say.
func TestEmptyEnvironmentStillCarriesTheDate(t *testing.T) {
	for _, a := range []*Agent{
		{environment: func() string { return "" }},
		{},
		{environment: func() string { panic("no") }}, // a crash costs a paragraph, not the run
	} {
		got := a.environmentSection()
		if !strings.Contains(got, "Current time:") {
			t.Errorf("the date did not survive an absent description: %q", got)
		}
		if strings.Contains(got, "machines:") {
			t.Errorf("an absent description contributed text: %q", got)
		}
	}
}

// The regression this exists for.
//
// llmTimeFormat was "Jan 02 15:04:05" — no year, under a comment saying it
// included the date. A planner asked for a body's position "right now" was told
// "Aug 29 11:16:55", supplied the year from memory, and sent
// START_TIME='2025-08-29' to nine API calls in a run that happened in 2026. Six
// returned real data for the wrong year and nothing in the run could tell.
func TestTheDateGivenToAStageCarriesTheYear(t *testing.T) {
	got := (&Agent{}).environmentSection()
	year := strconv.Itoa(time.Now().UTC().Year())
	if !strings.Contains(got, year) {
		t.Fatalf("the year is missing, so a stage writing a date has to guess it: %q", got)
	}
	// And a stage must be able to copy the date rather than compose it.
	day := time.Now().UTC().Format("2006-01-02")
	if !strings.Contains(got, day) {
		t.Errorf("the date is not written the way it goes into a parameter (%s): %q", day, got)
	}
}

// The rule travels with the date. Stating the day without saying it governs
// parameters is what the gate already did, and the planner still wrote a
// remembered year.
func TestTheDateSaysItGovernsParameters(t *testing.T) {
	got := (&Agent{}).environmentSection()
	if !strings.Contains(got, "parameter") {
		t.Errorf("the date is stated without saying what it is for: %q", got)
	}
}
