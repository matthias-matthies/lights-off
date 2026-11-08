package lightsoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// implementerEnding is the agent contract, the same on every pass.
const implementerEnding = `Do not merge the pull request. Lights-off decides on the merge.

End your final answer with only a JSON object:
{"status": "finished" or "blocked", "pr": <pull request number, or 0 if none>, "reason": "<one sentence>"}`

func implementerPrompt(repo string, t ticket) string {
	return fmt.Sprintf(`You are the Implementer for ticket #%[1]d of %[2]s, working unattended in a sandbox. Nobody can answer questions.

Use the implement skill on the ticket below. Its text is all you need: do not fetch the ticket or its comments.

These instructions override the skill:
- Do not run the code-review skill. An independent Reviewer judges the work later.
- Create a new branch for the work instead of committing to the current branch.

When the work is committed, push the branch to origin and open a pull request with gh. Write its body with the pr skill, and include the line "Closes #%[1]d".

%[3]s

<ticket>
%[4]s

%[5]s
</ticket>
`, t.Number, repo, implementerEnding, t.Title, t.Body)
}

// handBackPrompt continues the work in the sandbox that already holds it. The
// reason sits apart from the instructions because it can quote the Reviewer.
func handBackPrompt(repo string, t ticket, pr int, reason string) string {
	publish := fmt.Sprintf(`push to the branch of pull request #%d, and do not open another pull request.`, pr)
	if pr == 0 {
		publish = fmt.Sprintf(`push the branch to origin. If it has no open pull request yet, open one with gh, writing its body with the pr skill and including the line "Closes #%d".`, t.Number)
	}
	return fmt.Sprintf(`You are the Implementer for ticket #%[1]d of %[2]s, working unattended in a sandbox. Nobody can answer questions.

Your earlier work on this ticket is in this sandbox: stay on its branch and continue from it, and do not start over. It came back to you for the reason below. Fix what the reason describes, or finish the ticket if your earlier work never completed, using the implement skill if work remains. If the reason needs no change to the code, change nothing. Its text is all you need: do not fetch the ticket or its comments.

<hand-back-reason>
%[3]s
</hand-back-reason>

These instructions override the skill:
- Do not run the code-review skill. An independent Reviewer judges the work later.
- Create a new branch for the work if you are not on one yet, instead of committing to the default branch.

When the work is committed, %[4]s

%[5]s

<ticket>
%[6]s

%[7]s
</ticket>
`, t.Number, repo, reason, publish, implementerEnding, t.Title, t.Body)
}

func reviewerPrompt(repo string, t ticket, pr pullRequest, fixedPoint string) string {
	return fmt.Sprintf(`You are the Reviewer of pull request #%[1]d of %[2]s, working unattended in a sandbox. Nobody can answer questions. Do not ask for anything: if something is missing, say so in your reasons and judge with what you have.

The pull request is checked out at HEAD. Use the code-review skill with these inputs:
- Fixed point: %[3]s
- Spec: the ticket below

Do not merge, push or comment. Lights-off decides on the merge from your answer. Judge the code yourself: do not rely on the pull request's description or its Merge Danger section.

End your final answer with only a JSON object:
{"mergeable": true or false, "risk": "low" or "medium" or "high", "update": "two-way" or "one-way", "reason": "<one sentence saying whether it should merge and why you chose that risk>"}

- mergeable: the change does what the ticket asks and has no problem that should block it.
- risk: how much harm a mistake in this change could do before it is reverted, judged from what it touches and what the ticket says about how that is used, not from how easily it reverts. "low" means a mistake would be minor and contained. "medium" means it would break something people rely on, and anything affecting sign-in, permissions, payments or production data is at least medium. "high" means it could cause a wide outage, expose data or do lasting damage.
- update: "two-way" if reverting the merge fully undoes its effects, otherwise "one-way", for example a destructive data migration or a published release.

<ticket>
%[4]s

%[5]s
</ticket>
`, pr.Number, repo, fixedPoint, t.Title, t.Body)
}

// jsonObjects returns the top-level JSON objects in an agent's answer. The model
// may wrap its JSON in prose or a code fence, so braces that aren't JSON are
// skipped, whole: an object quoted inside them isn't the answer. An answer that
// ends on such braces, or never closes one, may have lost its real conclusion, so
// it has no objects at all.
func jsonObjects(answer string) []json.RawMessage {
	var found []json.RawMessage
	endsUnreadable := false
	for i := 0; i < len(answer); {
		start := strings.IndexByte(answer[i:], '{')
		if start < 0 {
			break
		}
		i += start
		dec := json.NewDecoder(strings.NewReader(answer[i:]))
		var object json.RawMessage
		if err := dec.Decode(&object); err != nil {
			length := bracedLength(answer[i:])
			if length < 0 {
				return nil
			}
			i += length
			endsUnreadable = true
			continue
		}
		found = append(found, object)
		endsUnreadable = false
		i += int(dec.InputOffset())
	}
	if endsUnreadable {
		return nil
	}
	return found
}

// bracedLength returns the length of the braces text opens with, ignoring braces
// in double-quoted strings, or -1 if they never close.
func bracedLength(text string) int {
	depth, inString := 0, false
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case inString && c == '\\':
			i++
		case c == '"':
			inString = !inString
		case inString:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

type implementerResult struct {
	Status string `json:"status"`
	PR     int    `json:"pr"`
	Reason string `json:"reason"`
}

// parseImplementerResult reads the answer's last object. A finished result must
// name its pull request; anything else is an error, which counts as a crash.
func parseImplementerResult(answer string) (implementerResult, error) {
	objects := jsonObjects(answer)
	if len(objects) == 0 {
		return implementerResult{}, errors.New("no JSON object in the implementer's answer")
	}
	var r implementerResult
	if err := json.Unmarshal(objects[len(objects)-1], &r); err != nil {
		return r, fmt.Errorf("invalid implementer result: %w", err)
	}
	switch {
	case r.Status == "finished" && r.PR <= 0:
		return r, errors.New("implementer result names no pull request")
	case r.Status == "blocked" && strings.TrimSpace(r.Reason) == "":
		// A person is about to act on it, and it is all they'd have to go on.
		return r, errors.New("blocked implementer result gives no reason")
	case r.Status != "finished" && r.Status != "blocked":
		return r, fmt.Errorf("unknown implementer status %q", r.Status)
	}
	return r, nil
}

// Verdict is the Reviewer's structured judgement of a pull request. Its JSON
// field names are both the Reviewer's answer and the Dashboard's event shape.
type Verdict struct {
	Mergeable bool   `json:"mergeable"`
	Risk      string `json:"risk"`
	Update    string `json:"update"`
	Reason    string `json:"reason"`
}

// parseVerdict reads the answer's last object. Every field must be present and
// exactly as specified, and an answer holding verdicts that disagree is invalid,
// because a review can quote an approving verdict from the code or ticket under
// review. Any error counts as a crash, so malformed output can never merge.
func parseVerdict(answer string) (Verdict, error) {
	objects := jsonObjects(answer)
	if len(objects) == 0 {
		return Verdict{}, errors.New("no JSON object in the reviewer's answer")
	}
	v, err := decodeVerdict(objects[len(objects)-1])
	if err != nil {
		return Verdict{}, err
	}
	// Checked here and not in decodeVerdict, so that an earlier verdict with no
	// reason still counts as a disagreement below.
	if strings.TrimSpace(v.Reason) == "" {
		return Verdict{}, errors.New("invalid verdict: reason is missing")
	}
	for _, object := range objects[:len(objects)-1] {
		other, err := decodeVerdict(object)
		if err != nil {
			continue // prose can contain unrelated objects
		}
		if other.Mergeable != v.Mergeable || other.Risk != v.Risk || other.Update != v.Update {
			return Verdict{}, errors.New("reviewer's answer holds verdicts that disagree")
		}
	}
	return v, nil
}

func decodeVerdict(object json.RawMessage) (Verdict, error) {
	var raw struct {
		Mergeable *bool  `json:"mergeable"`
		Risk      string `json:"risk"`
		Update    string `json:"update"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(object, &raw); err != nil {
		return Verdict{}, fmt.Errorf("invalid verdict: %w", err)
	}
	if raw.Mergeable == nil {
		return Verdict{}, errors.New("invalid verdict: mergeable is missing")
	}
	if raw.Risk != "low" && raw.Risk != "medium" && raw.Risk != "high" {
		return Verdict{}, fmt.Errorf("invalid verdict: unknown risk %q", raw.Risk)
	}
	if raw.Update != "two-way" && raw.Update != "one-way" {
		return Verdict{}, fmt.Errorf("invalid verdict: unknown update %q", raw.Update)
	}
	return Verdict{Mergeable: *raw.Mergeable, Risk: raw.Risk, Update: raw.Update, Reason: raw.Reason}, nil
}

func (v Verdict) describe() string {
	state := "not mergeable"
	if v.Mergeable {
		state = "mergeable"
	}
	return strings.Join([]string{state, v.Risk + " risk", v.Update}, ", ")
}
