package lightsoff

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// binding approves a kit's use of a service's stored API key on some domains.
// Without one, sbx starts a local kit's sandbox with the credential withheld.
type binding struct {
	service string
	domains []string
}

// requiredBindings covers both roles, because the Reviewer's token is stored
// under the same github service as the Implementer's. The github domains are the
// ones sbx documents for that service, so that sbx has nothing left to ask about
// in a run with nobody to answer.
var requiredBindings = []binding{
	{"deepseek", []string{"api.deepseek.com"}},
	{"github", []string{"api.github.com", "github.com", "raw.githubusercontent.com", "gist.github.com",
		"copilot.github.com", "api.githubcopilot.com"}},
}

const bindingsIndent = 2

// mergeBindings adds what's missing to credentials.yaml. sbx owns the file and
// lights-off has no YAML library, so it edits lines and keeps everything else
// byte for byte. A shape it doesn't recognise is an error, not a guess.
func mergeBindings(dir string) error {
	path := filepath.Join(dir, "credentials.yaml")
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // renaming over a symlink would replace the link
	}
	mode := fs.FileMode(0o600)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(data) == 0 {
		lines = nil
	}
	for _, b := range requiredBindings {
		if lines, err = addBinding(lines, b); err != nil {
			return fmt.Errorf("can't merge %s: %w. Add these lines by hand:\n\n%s", path, err, bindingsToAdd())
		}
	}
	merged := strings.Join(lines, "\n") + "\n"
	if merged == string(data) {
		return nil
	}
	return replaceFile(path, merged, mode)
}

func bindingsToAdd() string {
	var lines []string
	lines = append(lines, "bindings:")
	for _, b := range requiredBindings {
		lines = append(lines, bindingLines(b, bindingsIndent, bindingsIndent)...)
	}
	return strings.Join(lines, "\n")
}

// bindingLines renders a whole entry for a service at indent.
func bindingLines(b binding, indent, step int) []string {
	return append([]string{pad(indent) + b.service + ":"}, apiKeyLines(b, indent+step, step)...)
}

func apiKeyLines(b binding, indent, step int) []string {
	return []string{pad(indent) + "apiKey:", domainsLine(indent+step, b.domains)}
}

func domainsLine(indent int, domains []string) string {
	return pad(indent) + "domains: [" + strings.Join(domains, ", ") + "]"
}

func pad(n int) string { return strings.Repeat(" ", n) }

// replaceFile writes atomically, so that a crash can't truncate sbx's approvals.
func replaceFile(path, content string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(content)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// addBinding returns lines with b approved, editing as little as it can.
func addBinding(lines []string, b binding) ([]string, error) {
	if err := checkPlainMapping(lines); err != nil {
		return nil, err
	}
	root := findKey(lines, 0, len(lines), 0, "bindings")
	if root < 0 {
		return append(append(lines, "bindings:"), bindingLines(b, bindingsIndent, bindingsIndent)...), nil
	}
	_, value := parseKey(lines[root])
	if value == "{}" {
		lines[root] = "bindings:" + trailingComment(lines[root])
	} else if value != "" {
		return nil, errors.New("its bindings are not a block mapping")
	}

	if next := nextContent(lines, root); next >= 0 && isListItem(lines[next]) {
		return nil, errors.New("its bindings are not a block mapping")
	}
	end := blockEnd(lines, root, 0)
	indent := childIndent(lines, root, end, bindingsIndent)
	service := findKey(lines, root+1, end, indent, b.service)
	if service < 0 {
		return slices.Insert(lines, end, bindingLines(b, indent, indent)...), nil
	}
	if _, value := parseKey(lines[service]); value != "" {
		return nil, fmt.Errorf("its %s entry is not a block mapping", b.service)
	}

	end = blockEnd(lines, service, indent)
	inner := childIndent(lines, service, end, indent*2)
	step := inner - indent
	apiKey := findKey(lines, service+1, end, inner, "apiKey")
	if apiKey < 0 {
		return slices.Insert(lines, service+1, apiKeyLines(b, inner, step)...), nil
	}
	if _, value := parseKey(lines[apiKey]); value != "" {
		return nil, fmt.Errorf("its %s apiKey is not a block mapping", b.service)
	}

	end = blockEnd(lines, apiKey, inner)
	leaf := childIndent(lines, apiKey, end, inner+step)
	domains := findKey(lines, apiKey+1, end, leaf, "domains")
	if domains < 0 {
		return slices.Insert(lines, apiKey+1, domainsLine(leaf, b.domains)), nil
	}
	return mergeDomains(lines, domains, leaf, b)
}

func mergeDomains(lines []string, at, indent int, b binding) ([]string, error) {
	_, value := parseKey(lines[at])
	if value == "" {
		return mergeDomainBlock(lines, at, indent, b)
	}
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil, fmt.Errorf("its %s domains are not a list", b.service)
	}
	var existing []string
	for item := range strings.SplitSeq(value[1:len(value)-1], ",") {
		if item = strings.TrimSpace(item); item != "" {
			existing = append(existing, item)
		}
	}
	missing := missingDomains(existing, b)
	if len(missing) > 0 {
		lines[at] = domainsLine(indent, append(existing, missing...)) + trailingComment(lines[at])
	}
	return lines, nil
}

// mergeDomainBlock handles `- domain` items, which YAML allows at the same
// indent as their key.
func mergeDomainBlock(lines []string, at, indent int, b binding) ([]string, error) {
	var existing []string
	end := at + 1
	itemIndent := indent + bindingsIndent
	for i := at + 1; i < len(lines); i++ {
		if !isContent(lines[i]) {
			continue
		}
		if !isListItem(lines[i]) || indentOf(lines[i]) < indent {
			break
		}
		itemIndent = indentOf(lines[i])
		item, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(lines[i]), "- "), " #")
		existing = append(existing, strings.TrimSpace(item))
		end = i + 1
	}
	if len(existing) > 0 {
		var items []string
		for _, domain := range missingDomains(existing, b) {
			items = append(items, pad(itemIndent)+"- "+domain)
		}
		return slices.Insert(lines, end, items...), nil
	}
	if next := nextContent(lines, at); next >= 0 && indentOf(lines[next]) > indent {
		return nil, fmt.Errorf("its %s domains are not a list", b.service)
	}
	lines[at] = domainsLine(indent, b.domains) + trailingComment(lines[at])
	return lines, nil
}

func missingDomains(existing []string, b binding) []string {
	var missing []string
	for _, domain := range b.domains {
		covered := slices.ContainsFunc(existing, func(item string) bool {
			return strings.Trim(item, `"'`) == domain
		})
		if !covered {
			missing = append(missing, domain)
		}
	}
	return missing
}

// checkPlainMapping refuses what the line editing below can't safely work in: a
// file that isn't a single block mapping at its top level.
func checkPlainMapping(lines []string) error {
	for _, line := range lines {
		if strings.Contains(line[:len(line)-len(strings.TrimLeft(line, " \t"))], "\t") {
			return errors.New("it is indented with tabs")
		}
		if !isContent(line) || indentOf(line) > 0 {
			continue
		}
		key, _ := parseKey(line)
		if key == "" || strings.ContainsAny(line[:1], "-{[.%") {
			return errors.New("it is not a single YAML mapping")
		}
	}
	return nil
}

func isListItem(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "- ") }

// nextContent returns the index of the first content line after start, or -1.
func nextContent(lines []string, start int) int {
	for i := start + 1; i < len(lines); i++ {
		if isContent(lines[i]) {
			return i
		}
	}
	return -1
}

// trailingComment returns a line's comment with the space before it, or "".
func trailingComment(line string) string {
	if i := strings.Index(line, " #"); i >= 0 {
		return line[i:]
	}
	return ""
}

func isContent(line string) bool {
	text := strings.TrimSpace(line)
	return text != "" && text[0] != '#'
}

func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

// parseKey splits `key: value # comment` into its unquoted key and its value.
func parseKey(line string) (key, value string) {
	key, value, found := strings.Cut(strings.TrimSpace(line), ":")
	if !found {
		return "", ""
	}
	if i := strings.Index(value, " #"); i >= 0 {
		value = value[:i]
	}
	return strings.Trim(strings.TrimSpace(key), `"'`), strings.TrimSpace(value)
}

func findKey(lines []string, from, to, indent int, key string) int {
	for i := from; i < to; i++ {
		if !isContent(lines[i]) || indentOf(lines[i]) != indent {
			continue
		}
		if name, _ := parseKey(lines[i]); name == key {
			return i
		}
	}
	return -1
}

// blockEnd returns the index just past the last content line nested under the
// key at start, so that insertions land before any blank line or comment that
// leads into what follows.
func blockEnd(lines []string, start, indent int) int {
	end := start + 1
	for i := start + 1; i < len(lines); i++ {
		if !isContent(lines[i]) {
			continue
		}
		if indentOf(lines[i]) <= indent {
			break
		}
		end = i + 1
	}
	return end
}

// childIndent returns the indent of the block's first content line.
func childIndent(lines []string, start, end, fallback int) int {
	for i := start + 1; i < end; i++ {
		if isContent(lines[i]) {
			return indentOf(lines[i])
		}
	}
	return fallback
}
