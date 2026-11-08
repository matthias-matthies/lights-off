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
func (c configText) withRepo(repo string) string {
	nl := "\n"
	if strings.HasSuffix(c.text, "\r\n") {
		nl = "\r\n"
	}
	addition := fmt.Sprintf("[repos.%q]%sallowlist = []%s", repo, nl, nl)
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
// table removed, and the lines that were removed. Each table is removed from
// its header up to the next table header, so the blank lines that separated it
// from what follows go with it; everything before its header is left alone.
func (c configText) withoutRepo(repo string) (string, string) {
	lines := strings.SplitAfter(c.text, "\n")
	var kept, removed strings.Builder
	for i := 0; i < len(lines); {
		header, _, ok := tableHeader(lines[i])
		if !ok || header != repo {
			kept.WriteString(lines[i])
			i++
			continue
		}
		j := i + 1
		for j < len(lines) {
			if _, _, next := tableHeader(lines[j]); next {
				break
			}
			j++
		}
		for _, line := range lines[i:j] {
			removed.WriteString(line)
		}
		i = j
	}
	return kept.String(), removed.String()
}

// tableHeader reads a table header line, telling the repository it belongs to
// and whether it is its .secrets table.
func tableHeader(line string) (repo string, secrets, ok bool) {
	p := &lineParser{rest: strings.TrimSpace(line)}
	if !p.accept("[") {
		return "", false, false
	}
	repo, secrets, err := p.table()
	return repo, secrets, err == nil
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
