package session

import (
	"bytes"
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
)

// Tool name sets, lower case. Shell tools are the verifier's own list
// (SkillEvaluator eval.py _BEHAVIOR_EXEC_TOOLS) plus common spellings.
var (
	shellTools = set("bash", "execute", "exec_command", "run_code", "run", "shell", "command",
		"local_shell", "terminal", "run_terminal_cmd", "execute_command", "run_shell_command", "shell_command")
	readTools  = set("read", "read_file", "view", "grep", "glob", "ls", "list", "list_files", "list_dir", "find", "search", "notebookread", "codebase_search")
	writeTools = set("write", "write_file", "create_file", "create")
	editTools  = set("edit", "multiedit", "multi_edit", "edit_file", "str_replace_editor", "str_replace_based_edit_tool", "notebookedit", "replace")
	patchTools = set("apply_patch", "applypatch", "patch")
	webTools   = set("webfetch", "websearch", "web_fetch", "web_search", "fetch", "browse", "web")
	skillTools = set("skill")
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

var (
	// networkClient is the verifier's _NETWORK_CLIENT_PATTERN.
	networkClient = regexp.MustCompile(`(?i)\b(?:curl|wget|https?)(?:\.exe)?\b`)
	// packageInstall matches pip, uv, npm, pnpm, yarn, apt-get, apk and cargo installs.
	packageInstall = regexp.MustCompile(`(?i)(?:^|[\s;&|(])(?:sudo\s+)?(?:(?:python3?\s+-m\s+)?pip3?\s+install|uv\s+(?:pip\s+install|add|tool\s+install)|npm\s+(?:install|i|ci|add)|pnpm\s+(?:install|i|add)|yarn\s+(?:install|add)|apt-get\s+install|apt\s+install|apk\s+add|cargo\s+(?:install|add))\b`)
	// patchFile matches the file headers of an apply_patch patch.
	patchFile = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+?)\s*$`)
	// redirect matches a shell redirect target (not 2> or &>, not /dev/*).
	redirect = regexp.MustCompile(`(?:^|[^0-9&>])>>?[ \t]*([^\s;&|<>()'"]+)`)
	// teeTarget matches the file of a tee.
	teeTarget = regexp.MustCompile(`(?:^|[\s|;&])tee[ \t]+(?:-a[ \t]+)?([^\s;&|<>()'"-][^\s;&|<>()'"]*)`)
)

// toolInfo is what the session knows about one tool call.
type toolInfo struct {
	name     string
	kind     string
	category string
	command  string
	path     string
	title    string
	args     map[string]json.RawMessage
	// rawText is the arguments when they are a plain string (a freeform apply_patch).
	rawText string
}

// arg returns the first string argument of the given names.
func (ti *toolInfo) arg(names ...string) string {
	for _, n := range names {
		if s := jsonString(ti.args[n]); s != "" {
			return s
		}
	}
	return ""
}

// classify derives the kind, category, command, path and title of a tool call.
func classify(tc ToolCall) toolInfo {
	ti := toolInfo{name: tc.FunctionName}
	raw := bytes.TrimSpace(tc.Arguments)
	if s := jsonString(raw); s != "" {
		if obj := object(json.RawMessage(s)); obj != nil {
			ti.args = obj
		} else {
			ti.rawText = s
		}
	} else {
		ti.args = object(raw)
	}
	lower := strings.ToLower(tc.FunctionName)
	if i := strings.LastIndex(lower, "__"); i >= 0 && strings.HasPrefix(lower, "mcp__") {
		lower = "mcp:" + lower[i+2:]
	}
	switch {
	case skillTools[lower]:
		ti.kind, ti.category = apiv1.ToolSkill, apiv1.CategorySkill
		name := ti.arg("skill", "name", "command")
		ti.title = strings.TrimSpace(tc.FunctionName + " " + name)
	case shellTools[lower]:
		ti.kind = apiv1.ToolShell
		ti.command = shellCommand(ti.args, ti.rawText)
		ti.title = "$ " + firstLine(ti.command, 200)
		ti.category = shellCategory(ti.command)
	case readTools[lower]:
		ti.kind, ti.category = apiv1.ToolRead, apiv1.CategoryTools
		ti.path = ti.arg("file_path", "filePath", "path", "notebook_path", "target_file", "directory")
		pattern := ti.arg("pattern", "query", "regex")
		switch {
		case ti.path != "" && pattern != "":
			ti.title = tc.FunctionName + " " + pattern + " in " + ti.path
		case ti.path != "":
			ti.title = tc.FunctionName + " " + ti.path
		case pattern != "":
			ti.title = tc.FunctionName + " " + pattern
		default:
			ti.title = tc.FunctionName
		}
		if isSkillFile(ti.path) {
			ti.category = apiv1.CategorySkill
		}
	case writeTools[lower]:
		ti.kind, ti.category = apiv1.ToolWrite, apiv1.CategoryTools
		ti.path = ti.arg("file_path", "filePath", "path", "target_file")
		ti.title = strings.TrimSpace(tc.FunctionName + " " + ti.path)
	case editTools[lower]:
		ti.kind, ti.category = apiv1.ToolEdit, apiv1.CategoryTools
		ti.path = ti.arg("file_path", "filePath", "path", "notebook_path", "target_file")
		switch ti.arg("command") {
		case "create":
			ti.kind = apiv1.ToolWrite
		case "view":
			ti.kind = apiv1.ToolRead
		}
		ti.title = strings.TrimSpace(tc.FunctionName + " " + ti.path)
	case patchTools[lower]:
		ti.kind, ti.category = apiv1.ToolPatch, apiv1.CategoryTools
		files := patchFiles(ti.patchText())
		if len(files) > 0 {
			ti.path = files[0]
		}
		ti.title = strings.TrimSpace(tc.FunctionName + " " + summarizeFiles(files))
	case webTools[lower]:
		ti.kind, ti.category = apiv1.ToolWeb, apiv1.CategoryWeb
		ti.title = strings.TrimSpace(tc.FunctionName + " " + firstLine(ti.arg("url", "query", "q"), 200))
	default:
		ti.kind, ti.category = apiv1.ToolOther, apiv1.CategoryTools
		ti.title = tc.FunctionName
	}
	if ti.title == "" {
		ti.title = "Tool call"
	}
	return ti
}

// patchText returns the patch of an apply_patch call.
func (ti *toolInfo) patchText() string {
	if p := ti.arg("input", "patch", "diff"); p != "" {
		return p
	}
	return ti.rawText
}

// shellCommand returns the command of a shell call: command, cmd or code, as a
// string or an argv list (["bash", "-lc", "..."] gives the script).
func shellCommand(args map[string]json.RawMessage, rawText string) string {
	for _, k := range []string{"command", "cmd", "code", "script"} {
		v, ok := args[k]
		if !ok {
			continue
		}
		if s := jsonString(v); s != "" {
			return s
		}
		var argv []string
		if json.Unmarshal(v, &argv) == nil && len(argv) > 0 {
			if len(argv) >= 3 && (argv[1] == "-lc" || argv[1] == "-c") {
				return argv[len(argv)-1]
			}
			return strings.Join(argv, " ")
		}
	}
	return rawText
}

// shellCategory is skill for a SKILL.md read, pkg for an install, web for a network client, else tools.
func shellCategory(cmd string) string {
	switch {
	case strings.Contains(strings.ToLower(cmd), "skill.md"):
		return apiv1.CategorySkill
	case packageInstall.MatchString(cmd):
		return apiv1.CategoryPkg
	case networkClient.MatchString(cmd):
		return apiv1.CategoryWeb
	}
	return apiv1.CategoryTools
}

func isSkillFile(p string) bool {
	return p != "" && strings.EqualFold(path.Base(p), "SKILL.md")
}

// patchFiles returns the files an apply_patch patch adds, updates or deletes, in order.
func patchFiles(patch string) []string {
	var files []string
	for _, m := range patchFile.FindAllStringSubmatch(patch, -1) {
		files = append(files, m[1])
	}
	return files
}

func summarizeFiles(files []string) string {
	switch len(files) {
	case 0:
		return ""
	case 1:
		return files[0]
	}
	return files[0] + " and " + itoa(len(files)-1) + " more"
}

// redirectTargets returns the files a shell command writes with >, >> or tee.
func redirectTargets(cmd string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || strings.HasPrefix(p, "/dev/") || strings.HasPrefix(p, "&") || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, line := range strings.Split(cmd, "\n") {
		for _, m := range redirect.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
		for _, m := range teeTarget.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
	}
	return out
}

// firstLine returns the first line of s, at most limit bytes, marked when cut.
func firstLine(s string, limit int) string {
	s = strings.TrimSpace(s)
	line, rest, multi := strings.Cut(s, "\n")
	cut := cutBytes(line, limit)
	if multi && strings.TrimSpace(rest) != "" || len(cut) < len(line) {
		return cut + " ..."
	}
	return cut
}

// cutBytes returns the longest prefix of s of at most n bytes that ends on a rune boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
