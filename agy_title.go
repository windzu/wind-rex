package main

import (
	"cmp"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorex/internal/rex"
)

// isAgyInfo reports whether a session's foreground program is Antigravity CLI.
// When Antigravity has exited back to an idle shell prompt, the pane is a shell.
func isAgyInfo(in rex.SessionInfo) bool {
	if in.Idle && programOf(in.Program).Shell {
		return false
	}
	prog := strings.ToLower(strings.TrimPrefix(path.Base(in.ProgramName()), "-"))
	switch prog {
	case "agy", "antigravity", "antigravity-cli":
		return true
	}
	return false
}

// paneRawTitle returns the live terminal title when present, falling back to
// the title the session server last sampled.
func paneRawTitle(p *Pane) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(cmp.Or(p.title, p.info.Title))
}

// agyConversationTitle extracts a clean conversation title from an Antigravity
// CLI OSC terminal title. It returns "" when the raw title is absent, is a
// shell wrapper/prompt title, is a bare Antigravity program name, or only names
// a directory path (docs/DSK-012-antigravity-session-naming.md).
func agyConversationTitle(raw string, in rex.SessionInfo) string {
	s := cleanTitleLine(raw)
	if s == "" || isShellWrapperTitle(s, in) || isShellPromptTitle(s) {
		return ""
	}
	s = stripAgyBrandPrefix(s)
	if s == "" {
		return ""
	}
	s = stripTrailingPathSuffix(s, in.Dir)
	if s == "" || isBareAgyName(s) || looksLikeDirPath(s, in.Dir) {
		return ""
	}
	return s
}

func cleanTitleLine(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func isShellWrapperTitle(title string, in rex.SessionInfo) bool {
	l := strings.ToLower(title)
	for _, w := range []string{strings.ToLower(in.Shell), strings.ToLower(in.Program)} {
		if w == "" || isBareAgyName(w) {
			continue
		}
		if l == w || l == w+".exe" ||
			strings.HasPrefix(l, w+" - ") || strings.HasPrefix(l, w+".exe - ") ||
			strings.Contains(l, `\`+w+".exe") {
			return true
		}
	}
	return false
}

func isShellPromptTitle(title string) bool {
	at := strings.IndexByte(title, '@')
	if at <= 0 {
		return false
	}
	colon := strings.IndexByte(title[at:], ':')
	if colon <= 1 {
		return false
	}
	return !strings.ContainsAny(title[:at+colon], " \t")
}

func isBareAgyName(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "agy", "antigravity", "antigravity cli", "antigravity-cli", "google antigravity":
		return true
	}
	return false
}

// stripAgyBrandPrefix removes a redundant leading Antigravity brand prefix
// (optionally headed by a status symbol/emoji) when followed by a separator,
// or returns "" when the entire string is only the brand name.
func stripAgyBrandPrefix(s string) string {
	if isBareAgyName(s) {
		return ""
	}
	trimmed := stripLeadingSymbol(s)
	if isBareAgyName(trimmed) {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"google antigravity", "antigravity cli", "antigravity-cli", "antigravity", "agy"} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		rest := strings.TrimSpace(trimmed[len(prefix):])
		if rest == "" {
			return ""
		}
		r, w := utf8.DecodeRuneInString(rest)
		switch r {
		case '—', '–', '-', ':', '|', '·':
			return strings.TrimSpace(rest[w:])
		}
	}
	return s
}

// stripLeadingSymbol drops a leading non-alphanumeric symbol or emoji run
// followed by whitespace (for example "✨ Antigravity: ...").
func stripLeadingSymbol(s string) string {
	idx := strings.IndexByte(s, ' ')
	if idx <= 0 {
		return s
	}
	head := s[:idx]
	for _, r := range head {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return s
		}
	}
	return strings.TrimSpace(s[idx+1:])
}

// stripTrailingPathSuffix removes trailing " — /path" or " | ~/path" segments
// when the trailing segment is a filesystem path or matches the session dir.
func stripTrailingPathSuffix(s, dir string) string {
	seps := []string{" — ", " – ", " - ", " | ", " · "}
	for range 2 {
		stripped := false
		for _, sep := range seps {
			i := strings.LastIndex(s, sep)
			if i <= 0 {
				continue
			}
			rhs := strings.TrimSpace(s[i+len(sep):])
			if looksLikeDirPath(rhs, dir) {
				s = strings.TrimSpace(s[:i])
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	return s
}

// looksLikeDirPath reports whether s looks like a filesystem path or matches
// the session's directory path.
func looksLikeDirPath(s, dir string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if s == "~" ||
		strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`) ||
		strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "…/") || strings.HasPrefix(s, `…\`) ||
		strings.HasPrefix(s, `\\`) {
		return true
	}
	if len(s) >= 3 && ((s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= 'a' && s[0] <= 'z')) && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	if home, _ := os.UserHomeDir(); home != "" && strings.Contains(s, home) {
		return true
	}
	if dir != "" {
		if samePath(filepath.Clean(s), filepath.Clean(dir)) {
			return true
		}
		if sd := shortDir(dir); sd != "" && samePath(s, sd) {
			return true
		}
		if dd := displayDir(dir); dd != "" && samePath(s, dd) {
			return true
		}
	}
	return false
}

// agyProjectDetail returns concise secondary context for an untitled
// Antigravity session: the WindRex project name when available, else the
// working directory's folder name (omitted for home or root directories).
func agyProjectDetail(projectName, dir string) string {
	if p := strings.TrimSpace(projectName); p != "" {
		return p
	}
	if dir == "" {
		return ""
	}
	if home, _ := os.UserHomeDir(); home != "" && samePath(filepath.Clean(dir), filepath.Clean(home)) {
		return ""
	}
	return folderName(dir)
}

// displayDir formats a full directory path for hover tooltips, shortening the
// user's home directory to ~ without dropping intermediate path components.
func displayDir(d string) string {
	if d == "" {
		return ""
	}
	if home, _ := os.UserHomeDir(); home != "" {
		if samePath(d, home) {
			return "~"
		}
		if len(d) > len(home) && samePath(d[:len(home)], home) && d[len(home)] == filepath.Separator {
			return "~" + d[len(home):]
		}
	}
	return d
}

// paneTooltip returns hover tooltip text for pane p, moving full project and
// working directory paths out of compact inline tab/sidebar labels.
func paneTooltip(p *Pane, tabName, projectName string) string {
	if p == nil || p.note != nil {
		return ""
	}
	rawDir := cmp.Or(p.info.Dir, p.startDir)
	fullDir := displayDir(rawDir)
	if isAgyInfo(p.info) {
		conv := agyConversationTitle(paneRawTitle(p), p.info)
		proj := agyProjectDetail(projectName, rawDir)
		var lines []string
		switch {
		case tabName != "":
			lines = append(lines, tabName)
			if conv != "" && conv != tabName {
				lines = append(lines, conv)
			}
			if fullDir != "" {
				lines = append(lines, fullDir)
			}
		case p.Name != "":
			lines = append(lines, p.Name)
			if conv != "" && conv != p.Name {
				lines = append(lines, conv)
			}
			if fullDir != "" {
				lines = append(lines, fullDir)
			}
		case conv != "":
			lines = append(lines, conv)
			if fullDir != "" {
				if proj != "" && !strings.EqualFold(proj, folderName(rawDir)) {
					lines = append(lines, "Antigravity · "+proj)
					lines = append(lines, fullDir)
				} else {
					lines = append(lines, "Antigravity · "+fullDir)
				}
			} else if proj != "" {
				lines = append(lines, "Antigravity · "+proj)
			}
		default:
			if proj != "" {
				lines = append(lines, "Antigravity · "+proj)
			} else {
				lines = append(lines, "Antigravity")
			}
			if fullDir != "" {
				lines = append(lines, fullDir)
			}
		}
		return strings.Join(lines, "\n")
	}
	if fullDir != "" && fullDir != shortDir(rawDir) {
		name, _ := p.labelIn(projectName)
		if tabName != "" {
			name = tabName
		}
		if name != "" {
			return name + "\n" + fullDir
		}
		return fullDir
	}
	return ""
}

// tabTooltip returns hover tooltip text for a live tab in projectName.
func tabTooltip(t *Tab, projectName string) string {
	if t == nil || t.Focus == nil {
		return ""
	}
	return paneTooltip(t.Focus, t.Name, projectName)
}

// savedTabTooltip returns hover tooltip text for a saved tab of an unshown
// workspace, matching savedTabFaceIn.
func savedTabTooltip(t savedTab, infos map[string]rex.SessionInfo, projectName string) string {
	n := focusedLeaf(t)
	if n.SID == "" || n.URL != "" || n.Kind == "note" {
		return ""
	}
	in, ok := infos[n.SID]
	if !ok || in.Exited {
		return ""
	}
	p := &Pane{SID: n.SID, Name: n.Name, info: in, startDir: n.Dir}
	return paneTooltip(p, t.Name, projectName)
}
