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
	nl := c.lineEnding()
	addition := fmt.Sprintf("[repos.%q]%sallowlist = %s%s", repo, nl, formatAllowlist(allowlist), nl)
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

// withoutRepo returns the text with the repository's table and its .secrets
// table removed, and the lines that were removed. Each table goes from its
// header up to the next table header, with the blank lines that separated it
// from what follows. Comment lines right above the next table's header stay,
// because they describe that table. Everything before the removed header is
// left alone, except that removing the last table also drops the blank lines
// that separated it from the one above, so the file doesn't end in them.
func (c configText) withoutRepo(repo string) (string, string) {
	lines := strings.SplitAfter(c.text, "\n")
	var kept []string
	var removed strings.Builder
	for i := 0; i < len(lines); {
		header, _, ok := configTableHeader(lines[i])
		if !ok || header != repo {
			kept = append(kept, lines[i])
			i++
			continue
		}
		next := i + 1
		for next < len(lines) {
			if _, _, ok := configTableHeader(lines[next]); ok {
				break
			}
			next++
		}
		end := next
		if next < len(lines) {
			if r, _, _ := configTableHeader(lines[next]); r != repo {
				for end > i+1 && isCommentLine(lines[end-1]) {
					end--
				}
			}
		}
		for _, line := range lines[i:end] {
			removed.WriteString(line)
		}
		if next == len(lines) {
			for len(kept) > 0 && isBlankLine(kept[len(kept)-1]) {
				kept = kept[:len(kept)-1]
			}
		}
		i = end
	}
	return strings.Join(kept, ""), removed.String()
}

func isBlankLine(line string) bool { return strings.TrimSpace(line) == "" }

func isCommentLine(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

// withAllowlist returns the text with a repository's allowlist line set to
// users. It changes only that line's value, keeping its indentation, spacing
// and trailing comment, or inserts a line right after the table header when the
// repository has none yet.
func (c configText) withAllowlist(repo string, users []string) (string, error) {
	lines := strings.SplitAfter(c.text, "\n")
	header := -1
	for i, line := range lines {
		if r, secrets, ok := configTableHeader(line); ok && r == repo && !secrets {
			header = i
			break
		}
	}
	if header < 0 {
		return "", fmt.Errorf("config.toml has no [repos.%q] table", repo)
	}
	for i := header + 1; i < len(lines); i++ {
		if _, _, ok := configTableHeader(lines[i]); ok {
			break
		}
		if start, end, ok := allowlistValue(lines[i]); ok {
			lines[i] = lines[i][:start] + formatAllowlist(users) + lines[i][end:]
			return strings.Join(lines, ""), nil
		}
	}
	line, ending := trimLineEnding(lines[header])
	inserted := "allowlist = " + formatAllowlist(users)
	if ending == "" {
		lines[header] = line + c.lineEnding() + inserted
	} else {
		lines[header] = lines[header] + inserted + ending
	}
	return strings.Join(lines, ""), nil
}

// configTableHeader returns the repository and kind of a table header line.
func configTableHeader(line string) (repo string, secrets bool, ok bool) {
	content, _ := trimLineEnding(line)
	p := &lineParser{rest: strings.TrimSpace(content)}
	if !p.accept("[") {
		return "", false, false
	}
	repo, secrets, err := p.table()
	if err != nil {
		return "", false, false
	}
	return repo, secrets, true
}

// allowlistValue returns the span of the array value on an allowlist line,
// after any indentation and the "=". It handles "]" inside quoted strings.
func allowlistValue(line string) (start, end int, ok bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == len(line) || line[i] == '#' || line[i] == '[' || line[i] == '\r' || line[i] == '\n' {
		return 0, 0, false
	}
	eq := strings.IndexByte(line[i:], '=')
	if eq < 0 {
		return 0, 0, false
	}
	eq += i
	if strings.TrimSpace(line[i:eq]) != "allowlist" {
		return 0, 0, false
	}
	start = eq + 1
	for start < len(line) && (line[start] == ' ' || line[start] == '\t') {
		start++
	}
	if start == len(line) || line[start] != '[' {
		return 0, 0, false
	}
	quoted := false
	for j := start; j < len(line); j++ {
		switch line[j] {
		case '"':
			quoted = !quoted
		case ']':
			if !quoted {
				return start, j + 1, true
			}
		}
	}
	return 0, 0, false
}

func formatAllowlist(users []string) string {
	quoted := make([]string, len(users))
	for i, user := range users {
		quoted[i] = `"` + user + `"`
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func trimLineEnding(line string) (content, ending string) {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return line[:len(line)-2], "\r\n"
	case strings.HasSuffix(line, "\n"):
		return line[:len(line)-1], "\n"
	default:
		return line, ""
	}
}

// lineEnding follows the file's own line endings, so an edit on a Windows file
// doesn't mix them.
func (c configText) lineEnding() string {
	if i := strings.IndexByte(c.text, '\n'); i > 0 && c.text[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// writeChecked parses an edit before writing it, so a config lights-off writes
// is always one it can read back.
func (c configText) writeChecked(text string) error {
	if _, line, err := parseConfig(text); err != nil {
		return fmt.Errorf("%s:%d: edited config.toml is invalid: %w", configPath(c.dir), line, err)
	}
	return c.write(text)
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
