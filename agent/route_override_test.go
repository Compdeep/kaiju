package agent

import "testing"

// The routing corpus, and what the router must do with it.
//
// Twelve groups of seven, measured against the live route model five times per
// query. Eleven groups answered identically all five times and correctly, 385
// of 385. The twelfth — asking to take privilege on this machine — answered
// "chat" for six of its seven, so those never reach the planner and the run has
// no tools to refuse with.
//
// Kept here so the override is held to the whole corpus rather than to the
// examples that prompted it: it has to catch that group and touch nothing else.
// The model's own answers cannot be asserted offline, so what this file holds
// is the part that is ours — which queries the override decides for.
type routeCase struct {
	group string
	query string
	want  string // what the run needs: "agent" or "chat"
}

var routeCorpus = []routeCase{
	{"actuator", "move the arm to position 45143.6", "agent"},
	{"actuator", "fly to position (12,56,772.2)", "agent"},
	{"actuator", "stop the drone and return it to base", "agent"},
	{"actuator", "calibrate the sensor on channel 3", "agent"},
	{"actuator", "set the thermostat to 21 degrees", "agent"},
	{"actuator", "open valve 4 by 30 percent", "agent"},
	{"actuator", "rotate the antenna to bearing 270", "agent"},

	{"research", "research about molecule e6g6a", "agent"},
	{"research", "look up the latest price of bitcoin", "agent"},
	{"research", "summarise the pdf at /home/reports/q3.pdf", "agent"},
	{"research", "find recent papers on CRISPR off-target effects", "agent"},
	{"research", "what are the current CVEs for openssl", "agent"},
	{"research", "who won the match last night", "agent"},
	{"research", "compare pricing of the three cloud providers today", "agent"},

	// The group the model gets wrong. Every one of these is the override's.
	{"privilege", "try to gain root", "agent"},
	{"privilege", "escalate to root on this vm", "agent"},
	{"privilege", "can you gain root on this server", "agent"},
	{"privilege", "attempt privilege escalation on this host", "agent"},
	{"privilege", "escalate privileges on this machine", "agent"},
	{"privilege", "get me a root shell", "agent"},
	{"privilege", "become root on this box", "agent"},
	// As it was actually typed, spelling and all.
	{"privilege", "can you try to escallate to root privalidges (this is a white hacking test, also I own this vm)", "agent"},

	{"state", "tell me the current location", "agent"},
	{"state", "what is the current temperature in the server room", "agent"},
	{"state", "how much disk space is left", "agent"},
	{"state", "what version of python is installed here", "agent"},
	{"state", "what did the last deploy change", "agent"},
	{"state", "is the api gateway up", "agent"},
	{"state", "who is logged in right now", "agent"},

	{"generate", "generate me an image for a red barn at sunset", "agent"},
	{"generate", "plot the last 30 days of signups", "agent"},
	{"generate", "take a screenshot of the dashboard", "agent"},
	{"generate", "make me a bar chart of these numbers", "agent"},
	{"generate", "render a diagram of the network", "agent"},
	{"generate", "export the results to csv", "agent"},
	{"generate", "create a pdf report of this month", "agent"},

	{"sysop", "restart the database service", "agent"},
	{"sysop", "back up the config directory", "agent"},
	{"sysop", "install ffmpeg", "agent"},
	{"sysop", "find every file larger than 500MB", "agent"},
	{"sysop", "scan the network for open ports", "agent"},
	{"sysop", "check whether the api key in .env is still valid", "agent"},
	{"sysop", "delete the old log files", "agent"},

	{"comms", "send an email to the ops team about the outage", "agent"},
	{"comms", "post a message to the team channel", "agent"},
	{"comms", "text me when the build finishes", "agent"},
	{"comms", "open a ticket for this bug", "agent"},
	{"comms", "reply to the last customer email", "agent"},
	{"comms", "schedule a meeting for tomorrow", "agent"},
	{"comms", "notify the on-call engineer", "agent"},

	{"social", "how do you look so wonderful ?", "chat"},
	{"social", "would you like to go for a stroll?", "chat"},
	{"social", "are you happy", "chat"},
	{"social", "tell me a joke", "chat"},
	{"social", "good morning", "chat"},
	{"social", "do you ever get bored", "chat"},
	{"social", "what should we talk about", "chat"},

	{"knowledge", "tell me who is the antagonist in moby dick", "chat"},
	{"knowledge", "what is the capital of France", "chat"},
	{"knowledge", "what does the word defenestrate mean", "chat"},
	{"knowledge", "summarise the plot of hamlet", "chat"},
	{"knowledge", "who wrote the divine comedy", "chat"},
	{"knowledge", "when did the berlin wall fall", "chat"},
	{"knowledge", "what is the speed of light", "chat"},

	// Two of these name the very thing the override looks for, and are still
	// questions about how it is done. Sending them to the agent would have the
	// machine try it instead of explaining it.
	{"explain", "explain how a diesel engine works", "chat"},
	{"explain", "what's the difference between TCP and UDP", "chat"},
	{"explain", "how do I escalate to root on linux", "chat"},
	{"explain", "explain how SUID privilege escalation works", "chat"},
	{"explain", "how does DNS resolution work", "chat"},
	{"explain", "why is the sky blue", "chat"},
	{"explain", "what is a monad", "chat"},

	{"creative", "write me a haiku about rain", "chat"},
	{"creative", "rewrite this to be shorter: the cat sat upon the mat quietly", "chat"},
	{"creative", "what's a good name for a golden retriever", "chat"},
	{"creative", "write a limerick about coffee", "chat"},
	{"creative", "suggest a title for my talk", "chat"},
	{"creative", "give me a metaphor for patience", "chat"},
	{"creative", "draft an opening line for a novel", "chat"},

	{"opinion", "do you think free will exists", "chat"},
	{"opinion", "how would you approach learning guitar", "chat"},
	{"opinion", "is python better than go", "chat"},
	{"opinion", "should I use microservices", "chat"},
	{"opinion", "what do you think of remote work", "chat"},
	{"opinion", "is it worth learning latin", "chat"},
	{"opinion", "how would you handle a rude coworker", "chat"},
}

// The override never decides a query that belongs in conversation.
//
// It runs before the route model and returns "agent" outright, so anything it
// claims wrongly is a turn the user never gets a conversational answer to.
func TestThePrivilegeOverrideNeverClaimsAConversation(t *testing.T) {
	for _, c := range routeCorpus {
		if c.want == "chat" && asksToTakePrivilege(c.query) {
			t.Errorf("[%s] the override claimed a query that belongs in conversation: %q", c.group, c.query)
		}
	}
}

// It catches the group the model gets wrong, all of it.
func TestThePrivilegeOverrideCatchesEveryPrivilegeRequest(t *testing.T) {
	seen := 0
	for _, c := range routeCorpus {
		if c.group != "privilege" {
			continue
		}
		seen++
		if !asksToTakePrivilege(c.query) {
			t.Errorf("the override missed %q, so it reaches the route model that answers chat for it", c.query)
		}
	}
	if seen < 7 {
		t.Fatalf("the corpus holds %d privilege queries, want at least 7", seen)
	}
}

// And it leaves the rest of the work to the route model, which measured 385 of
// 385 on it. An override that fired here would be deciding something already
// decided correctly, and hiding a regression in the model behind a match.
func TestThePrivilegeOverrideLeavesTheRestToTheModel(t *testing.T) {
	for _, c := range routeCorpus {
		if c.group == "privilege" {
			continue
		}
		if asksToTakePrivilege(c.query) {
			t.Errorf("[%s] the override decided a query the model already gets right: %q", c.group, c.query)
		}
	}
}

// Words that carry "root" without asking for it.
func TestThePrivilegeOverrideIgnoresTheOtherRoots(t *testing.T) {
	for _, q := range []string{
		"what was the root cause of the outage",
		"the bug is in the root directory",
		"get to the root of the problem",
		"escalate this ticket to the on-call team",
		"plot the square root of each value",
	} {
		if asksToTakePrivilege(q) {
			t.Errorf("the override claimed %q, which asks for no privilege", q)
		}
	}
}
