package lightsoff

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// configText is config.toml held as text, so that an edit changes only the
// lines it means to and the person's comments, blank lines and layout survive.
type configText struct {
	dir    string
	text   string
	exists bool
	mode   fs.FileMode
	cfg    config
}

// readConfigText reads and parses config.toml before anything edits it. A
// missing file is an empty config, so that the first opt-in has somewhere to
// start.
func readConfigText(dir string) (configText, error) {
	c := configText{dir: dir, mode: 0o600}
	path := configPath(dir)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		c.cfg = config{repos: map[string]repoConfig{}}
		return c, nil
	}
	if err != nil {
		return configText{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return configText{}, err
	}
	cfg, line, err := parseConfig(string(data))
	if err != nil {
		return configText{}, fmt.Errorf("%s:%d: %w", path, line, err)
	}
	c.text = string(data)
	c.exists = true
	c.mode = info.Mode().Perm()
	c.cfg = cfg
	return c, nil
}

// withRepo returns the text with a new repository table appended after a blank
// line. Everything already in the file is left byte-for-byte alone.
func (c configText) withRepo(repo string, allowlist []string) string {
	nl := "\n"
	if strings.Contains(c.text, "\r\n") {
		nl = "\r\n"
	}
	addition := fmt.Sprintf("[repos.%q]%sallowlist = %s%s", repo, nl, allowlistValue(allowlist), nl)
	if c.text == "" {
		return addition
	}
	switch {
	case strings.HasSuffix(c.text, nl+nl):
		return c.text + addition
	case strings.HasSuffix(c.text, nl):
		return c.text + nl + addition
	default:
		return c.text + nl + nl + addition
	}
}

// addingUsers returns the text with users appended to the repository's
// Allowlist, and which names were added or were already there.
func (c configText) addingUsers(repo string, users []string) (string, []string, []string, error) {
	merged, added, already := mergeUsers(c.cfg.repos[repo].allowlist, users)
	edited, err := c.withAllowlist(repo, merged)
	return edited, added, already, err
}

// withAllowlist replaces only an allowlist line's value, keeping everything
// before and after it, including a trailing comment. A table without that line
// gets one right after its header. The file was parsed first, so the line
// structure is known; an absent table is refused rather than guessed at.
func (c configText) withAllowlist(repo string, users []string) (string, error) {
	lines := splitLines(c.text)
	header := -1
	for i, line := range lines {
		if r, secrets, ok := tableHeader(line); ok && r == repo && !secrets {
			header = i
			break
		}
	}
	if header < 0 {
		return "", fmt.Errorf("config.toml has no [repos.%q] table", repo)
	}

	end := len(lines)
	for i := header + 1; i < len(lines); i++ {
		body, _ := splitLineEnding(lines[i])
		if strings.HasPrefix(strings.TrimSpace(body), "[") {
			end = i
			break
		}
	}

	value := allowlistValue(users)
	for i := header + 1; i < end; i++ {
		body, ending := splitLineEnding(lines[i])
		eq := strings.Index(body, "=")
		if eq < 0 || strings.TrimSpace(body[:eq]) != "allowlist" {
			continue
		}
		open := strings.Index(body[eq:], "[")
		if open < 0 {
			continue
		}
		open += eq
		close := closingBracket(body, open)
		if close < 0 {
			continue
		}
		lines[i] = body[:open] + value + body[close+1:] + ending
		return strings.Join(lines, ""), nil
	}

	body, ending := splitLineEnding(lines[header])
	if ending == "" {
		nl := "\n"
		if strings.Contains(c.text, "\r\n") {
			nl = "\r\n"
		}
		lines[header] = body + nl + "allowlist = " + value + nl
	} else {
		lines[header] = body + ending + "allowlist = " + value + ending
	}
	return strings.Join(lines, ""), nil
}

// writeChecked parses the edited text with the same parser the next run will use
// before writing it, so a config lights-off wrote is one it can read back.
func (c configText) writeChecked(text string) error {
	if _, line, err := parseConfig(text); err != nil {
		return fmt.Errorf("%s:%d: edited config.toml is invalid: %w", configPath(c.dir), line, err)
	}
	return c.write(text)
}

// allowlistValue writes users as a one-line array of plain strings. The parser
// never accepts a quote or a backslash in a string, so no escaping is needed.
func allowlistValue(users []string) string {
	quoted := make([]string, len(users))
	for i, user := range users {
		quoted[i] = `"` + user + `"`
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// splitLines splits text into lines that keep their own endings, so joining them
// again reproduces the file exactly.
func splitLines(text string) []string {
	return strings.SplitAfter(text, "\n")
}

// splitLineEnding separates one line's text from its ending.
func splitLineEnding(line string) (body, ending string) {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return line[:len(line)-2], "\r\n"
	case strings.HasSuffix(line, "\n"):
		return line[:len(line)-1], "\n"
	default:
		return line, ""
	}
}

// tableHeader reads a line as a repository table header, if it is one.
func tableHeader(line string) (repo string, secrets, ok bool) {
	body, _ := splitLineEnding(line)
	p := &lineParser{rest: strings.TrimSpace(body)}
	if !p.accept("[") {
		return "", false, false
	}
	repo, secrets, err := p.table()
	return repo, secrets, err == nil
}

// closingBracket finds the "]" that ends the array starting at open, ignoring
// brackets inside a quoted string.
func closingBracket(line string, open int) int {
	quoted := false
	for i := open; i < len(line); i++ {
		switch line[i] {
		case '"':
			quoted = !quoted
		case ']':
			if !quoted {
				return i
			}
		}
	}
	return -1
}

// write puts edited text in a temporary file in the same directory and renames
// it over config.toml, so a crash can't leave a half-written config behind. An
// existing file keeps its mode; a new directory is private to its owner and a
// new file is readable only by them.
func (c configText) write(text string) error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, ".config.toml.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpPath)
	}()
	if _, err := tmp.WriteString(text); err != nil {
		return err
	}
	mode := c.mode
	if !c.exists {
		mode = 0o600
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, configPath(c.dir))
}
