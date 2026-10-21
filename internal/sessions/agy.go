package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/dirent"
)

// Antigravity (CLI and Desktop) stores conversations under
// ~/.gemini/antigravity-cli, ~/.gemini/antigravity, and the older
// ~/.gemini/antigravity-ide, in brain/<conversation id>/.system_generated/logs/transcript_full.jsonl.
// Each line is a JSON object with step_index, source (USER_EXPLICIT, MODEL, SYSTEM),
// type, created_at, content, and optional input_tokens, output_tokens, cache_read_tokens.

// agyCarry is what a read of an Antigravity transcript keeps to know whether
// it can stand for the next read without re-parsing unchanged files.
type agyCarry struct {
	Size  int64  `json:"size"`
	Mod   int64  `json:"mod"`
	Title string `json:"title,omitempty"`
	Model string `json:"model,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
}

// agyDirs are the data folders that hold Antigravity conversations:
// the CLI's (~/.gemini/antigravity-cli), and the desktop app's
// (~/.gemini/antigravity and the older ~/.gemini/antigravity-ide).
func agyDirs() []string {
	home, _ := os.UserHomeDir()
	g := filepath.Join(home, ".gemini")
	return []string{filepath.Join(g, "antigravity-cli"), filepath.Join(g, "antigravity"), filepath.Join(g, "antigravity-ide")}
}

// agyFiles returns the transcript_full.jsonl file for each conversation found
// across the Antigravity data directories, deduping by conversation id.
func agyFiles() []file {
	var out []file
	seen := map[string]int{}
	for _, dir := range agyDirs() {
		brain := filepath.Join(dir, "brain")
		dirs, _ := os.ReadDir(brain)
		for _, d := range dirs {
			if !dirent.IsFolder(brain, d) {
				continue
			}
			p := filepath.Join(brain, d.Name(), ".system_generated", "logs", "transcript_full.jsonl")
			f := file{agent: "agy", key: "agy:" + d.Name(), path: p, main: true}
			if !stat(&f) {
				continue
			}
			if idx, ok := seen[f.key]; ok {
				if f.mod.After(out[idx].mod) || (f.mod.Equal(out[idx].mod) && f.size > out[idx].size) {
					out[idx] = f
				}
				continue
			}
			seen[f.key] = len(out)
			out = append(out, f)
		}
	}
	return out
}

var agyModelSetting = regexp.MustCompile("`Model(?: Selection)?` (?:from [^`\\n\\r]+? )?to ([^`\\n\\r]+?)(?:\\.(?:\\s|<)|$)")
var agyModelDirect = regexp.MustCompile(`(?i)(?:selected model|using model)[:\s]+([a-zA-Z0-9_.-]+)`)

func normalizeAgyModel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "none") {
		return ""
	}
	s := strings.ToLower(raw)
	s = strings.ReplaceAll(s, "(", " ")
	s = strings.ReplaceAll(s, ")", " ")
	s = strings.ReplaceAll(s, "[", " ")
	s = strings.ReplaceAll(s, "]", " ")
	s = strings.Join(strings.Fields(s), "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

// agySettingsModel reads the configured "model" from settings.json in the
// root Antigravity folder (e.g. ~/.gemini/antigravity-cli/settings.json).
func agySettingsModel(path string) string {
	cur := filepath.Dir(path)
	for i := 0; i < 5; i++ {
		cur = filepath.Dir(cur)
		settingsPath := filepath.Join(cur, "settings.json")
		b, err := os.ReadFile(settingsPath)
		if err == nil {
			var cfg struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(b, &cfg) == nil && cfg.Model != "" {
				return normalizeAgyModel(cfg.Model)
			}
		}
	}
	return ""
}

type agyTranscriptLine struct {
	StepIndex    int               `json:"step_index"`
	Source       string            `json:"source"`
	Type         string            `json:"type"`
	Status       string            `json:"status"`
	CreatedAt    string            `json:"created_at"`
	Content      string            `json:"content"`
	Input        *int              `json:"input_tokens"`
	CacheRead    *int              `json:"cache_read_tokens"`
	Output       *int              `json:"output_tokens"`
	ToolCalls    []agyToolCallItem `json:"tool_calls"`
	Error        string            `json:"error"`
	Model        string            `json:"model,omitempty"`
	ModelVersion string            `json:"model_version,omitempty"`
	ModelName    string            `json:"model_name,omitempty"`
	ResponseID   string            `json:"response_id,omitempty"`
	MessageID    string            `json:"message_id,omitempty"`
}

type agyToolCallItem struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// agyReadSteps reads all lines from transcript_full.jsonl, keeping the latest
// entry for each step_index so rewritten or streaming steps are deduplicated.
func agyReadSteps(r io.Reader) []agyTranscriptLine {
	type stepSlot struct {
		line agyTranscriptLine
	}
	byIndex := map[int]*stepSlot{}
	var order []*stepSlot

	br := bufio.NewReaderSize(r, scanBuffer(r, 0))
	for {
		b, err := br.ReadBytes('\n')
		if err != nil && (!errors.Is(err, io.EOF) || len(bytes.TrimSpace(b)) == 0) {
			break
		}
		b = bytes.TrimSpace(b)
		if len(b) > 0 && len(b) <= maxLine {
			var line agyTranscriptLine
			if json.Unmarshal(b, &line) == nil {
				if slot, ok := byIndex[line.StepIndex]; ok {
					slot.line = line
				} else {
					slot := &stepSlot{line: line}
					byIndex[line.StepIndex] = slot
					order = append(order, slot)
				}
			}
		}
		if err != nil {
			break
		}
	}
	out := make([]agyTranscriptLine, len(order))
	for i, slot := range order {
		out[i] = slot.line
	}
	return out
}

// agyFoldKey returns the fold key for a step: response_id, else message_id,
// else step index.
func agyFoldKey(line agyTranscriptLine) string {
	if line.ResponseID != "" {
		return "resp:" + line.ResponseID
	}
	if line.MessageID != "" {
		return "msg:" + line.MessageID
	}
	return "step:" + strconv.Itoa(line.StepIndex)
}

// agyPrompt extracts user text from a USER_EXPLICIT step's content.
func agyPrompt(content string) string {
	if start := strings.Index(content, "<USER_REQUEST>"); start >= 0 {
		content = content[start+len("<USER_REQUEST>"):]
		if end := strings.Index(content, "</USER_REQUEST>"); end >= 0 {
			content = content[:end]
		}
	}
	if meta := strings.Index(content, "<ADDITIONAL_METADATA>"); meta >= 0 {
		content = content[:meta]
	}
	return strings.TrimSpace(content)
}

// parseAgy reads an Antigravity conversation transcript to compute session
// metadata, token usage, timestamps and prompt/title.
func parseAgy(f file, old *state) *state {
	if old != nil && old.Agy != nil && old.Agy.Size == f.size && old.Agy.Mod == f.mod.UnixNano() {
		return old.clone()
	}
	r, err := os.Open(f.path)
	if err != nil {
		return &state{ID: strings.TrimPrefix(f.key, "agy:"), Size: f.size, Mod: f.mod.UnixNano()}
	}
	defer r.Close()

	s := &state{ID: strings.TrimPrefix(f.key, "agy:")}
	s.Size, s.Mod = f.size, f.mod.UnixNano()
	c := &agyCarry{Size: f.size, Mod: f.mod.UnixNano()}

	steps := agyReadSteps(r)

	// Step folding: when multiple MODEL steps belong to the same response
	// (sharing response_id, message_id, or step_index), only the last of them
	// counts its reply and tokens, preventing double-counting across tool loops.
	lastForFoldKey := map[string]int{}
	for i, line := range steps {
		if line.Source == "MODEL" {
			lastForFoldKey[agyFoldKey(line)] = i
		}
	}

	for i, line := range steps {
		isLastOfResponse := (line.Source != "MODEL" || lastForFoldKey[agyFoldKey(line)] == i)
		agyStepOf(s, c, line, f.main, isLastOfResponse)
	}

	if c.Model == "" {
		if m := agySettingsModel(f.path); m != "" {
			c.Model = m
		}
	}
	if c.Title != "" {
		s.Title = c.Title
	}
	if c.Cwd != "" {
		s.Cwd = c.Cwd
	}
	s.Agy = c
	return s
}

func agyStepOf(s *state, c *agyCarry, line agyTranscriptLine, main bool, isLastOfResponse bool) {
	at, _ := time.Parse(time.RFC3339, line.CreatedAt)
	if !at.IsZero() {
		s.saw(at, main)
	}

	// Extract model if present on the line
	if line.Model != "" {
		if norm := normalizeAgyModel(line.Model); norm != "" {
			c.Model = norm
		}
	} else if line.ModelVersion != "" {
		if norm := normalizeAgyModel(line.ModelVersion); norm != "" {
			c.Model = norm
		}
	} else if line.ModelName != "" {
		if norm := normalizeAgyModel(line.ModelName); norm != "" {
			c.Model = norm
		}
	}

	if line.Source == "USER_EXPLICIT" {
		if m := agyModelSetting.FindStringSubmatch(line.Content); m != nil {
			if norm := normalizeAgyModel(m[1]); norm != "" {
				c.Model = norm
			}
		} else if m := agyModelDirect.FindStringSubmatch(line.Content); m != nil {
			if norm := normalizeAgyModel(m[1]); norm != "" {
				c.Model = norm
			}
		}
		prompt := agyPrompt(line.Content)
		if prompt != "" {
			if c.Title == "" && main {
				c.Title = title(prompt)
				if c.Title == "" {
					s.First = prompt
				}
			}
			if !at.IsZero() {
				s.day(dateOf(at)).Prompts++
			}
		}
	}

	if line.Source == "MODEL" {
		for _, tc := range line.ToolCalls {
			if !at.IsZero() {
				s.tool(at, tc.Name, "")
			}
			if c.Cwd == "" && tc.Name == "run_command" {
				var args struct {
					Cwd string `json:"Cwd"`
				}
				if json.Unmarshal(tc.Args, &args) == nil && args.Cwd != "" {
					c.Cwd = args.Cwd
				}
			}
		}

		if isLastOfResponse {
			if !at.IsZero() {
				s.day(dateOf(at)).Replies++
			}
			if line.Input != nil && line.Output != nil && !at.IsZero() {
				tokens := Tokens{Input: *line.Input, Output: *line.Output}
				if line.CacheRead != nil {
					tokens.CacheRead = *line.CacheRead
				}
				model := c.Model
				if model == "" {
					model = "gemini"
				}
				s.use(dateOf(at), model, tokens)
			}
		}
	}
}

// agyTranscript reads a session's conversation as parts for TranscriptOf.
func agyTranscript(path string, add func(bool, Part) bool) error {
	r, err := os.Open(path)
	if err != nil {
		return err
	}
	defer r.Close()

	steps := agyReadSteps(r)
	for _, line := range steps {
		if !agyAddTranscriptLine(line, add) {
			return nil
		}
	}
	return nil
}

func agyAddTranscriptLine(line agyTranscriptLine, add func(bool, Part) bool) bool {
	switch line.Source {
	case "USER_EXPLICIT":
		prompt := agyPrompt(line.Content)
		if prompt != "" {
			return add(true, Part{Role: "user", Kind: "text", Text: prompt})
		}
	case "MODEL":
		if line.Content != "" {
			if !add(false, Part{Role: "assistant", Kind: "text", Text: line.Content}) {
				return false
			}
		}
		for _, tc := range line.ToolCalls {
			part := Part{Role: "assistant", Kind: "tool_use", Name: tc.Name, Text: string(tc.Args)}
			if !add(false, part) {
				return false
			}
		}
	case "SYSTEM":
		if line.Type == "ERROR_MESSAGE" && line.Error != "" {
			return add(false, Part{Role: "system", Kind: "text", Text: line.Error})
		}
	}
	return true
}
