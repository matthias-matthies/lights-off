package lightsoff_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// summary returns what follows the summary heading in stdout.
func (w *world) summary() string {
	w.t.Helper()
	_, after, found := strings.Cut(w.stdout.String(), "lights-off: summary\n")
	if !found {
		w.t.Fatalf("no summary in stdout:\n%s", w.stdout.String())
	}
	return after
}

func (w *world) assertSummaryHas(parts ...string) {
	w.t.Helper()
	summary := w.summary()
	for _, part := range parts {
		if !strings.Contains(summary, part) {
			w.t.Errorf("summary lacks %q:\n%s", part, summary)
		}
	}
}

func TestSummaryShowsWhatMergedAndWhatNeedsAPerson(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(0)
	w.addTicket(43, day(1)).verdict = oneWayVerdict

	if code := w.run("owner/repo", "--n=2"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	var kept []string
	for _, name := range w.sandboxes {
		if sandboxTicket(name) == 43 {
			kept = append(kept, name)
		}
	}
	if len(kept) != 2 {
		t.Fatalf("want #43's two sandboxes, got %v", w.sandboxes)
	}
	w.assertSummaryHas(
		"merged: #42, pull request #7",
		"needs a person: #43, pull request #143 open: drops a table",
		"sandboxes kept: "+kept[0]+", "+kept[1],
	)
	if strings.Contains(w.summary(), "#42, pull request #7\n    sandboxes kept") {
		t.Errorf("summary names sandboxes for a merged ticket:\n%s", w.summary())
	}
}

// runLogs returns every file of the one run directory under the state
// directory, by name.
func (w *world) runLogs() (string, map[string]string) {
	w.t.Helper()
	runs, err := filepath.Glob(filepath.Join(w.stateDir, "runs", "*"))
	if err != nil || len(runs) != 1 {
		w.t.Fatalf("want one run directory, got %v (%v)", runs, err)
	}
	files := map[string]string{}
	entries, err := os.ReadDir(runs[0])
	if err != nil {
		w.t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(runs[0], e.Name()))
		if err != nil {
			w.t.Fatal(err)
		}
		files[e.Name()] = string(data)
	}
	return runs[0], files
}

func TestFullAgentOutputIsWrittenToThePerRunDirectory(t *testing.T) {
	w := newWorld(t)
	w.implementerPasses = []agentPass{
		{output: "Opening a PR.\n" + finishedResult, stderr: "reasoning about sum\nran go test"},
	}
	w.reviewerPasses = []agentPass{{output: "```json\n" + mergeVerdict + "\n```", stderr: "reviewing the diff"}}

	if code := w.run("owner/repo", "--n=1"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	dir, files := w.runLogs()
	if !strings.HasPrefix(dir, w.stateDir) {
		t.Errorf("run directory %q is not under the state directory %q", dir, w.stateDir)
	}
	implementer := files["42-implementer-1.log"]
	for _, want := range []string{"Opening a PR.", finishedResult, "reasoning about sum\nran go test", "Make sum add numbers."} {
		if !strings.Contains(implementer, want) {
			t.Errorf("implementer log lacks %q:\n%s", want, implementer)
		}
	}
	if reviewer := files["42-reviewer-1.log"]; !strings.Contains(reviewer, mergeVerdict) || !strings.Contains(reviewer, "reviewing the diff") {
		t.Errorf("reviewer log lacks its output:\n%s", reviewer)
	}
	if run := files["run.log"]; !strings.Contains(run, "#42 merged pull request #7") || !strings.Contains(run, "lights-off: summary") {
		t.Errorf("run.log lacks the status lines and summary:\n%s", run)
	}
	w.assertSummaryHas("logs: " + dir)
	for name, text := range files {
		for _, secret := range []string{deepseekKey, implementerGH, reviewerGH} {
			if strings.Contains(text, secret) {
				t.Errorf("%s contains a secret value", name)
			}
		}
	}
}

func TestEachIterationKeepsItsOwnAgentLog(t *testing.T) {
	w := newWorld(t)
	w.checks = []string{redChecks, greenChecks}
	w.implementerPasses = []agentPass{{output: "first try\n" + finishedResult}, {output: "second try\n" + finishedResult}}

	if code := w.run("owner/repo", "--n=1"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	_, files := w.runLogs()
	if !strings.Contains(files["42-implementer-1.log"], "first try") || !strings.Contains(files["42-implementer-2.log"], "second try") {
		t.Errorf("want one implementer log per iteration, got files %v", keys(files))
	}
}

func keys(m map[string]string) []string {
	var names []string
	for name := range m {
		names = append(names, name)
	}
	return names
}
