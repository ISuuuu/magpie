package sessions

import (
	"path/filepath"
	"testing"
)

func TestAgySessionsList(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	copyTree(t, "testdata/agy", filepath.Join(home, ".gemini", "antigravity-cli"))
	copyTree(t, "testdata/agy", filepath.Join(home, ".gemini", "antigravity"))

	Reset()
	ss := List(0)

	var agySessions []Session
	for _, s := range ss {
		if s.Agent == "agy" {
			agySessions = append(agySessions, s)
		}
	}

	// 2 non-empty conversations in testdata/agy (deduped across cli and desktop;
	// the third in testdata has no content and no tokens, dropped as an empty session)
	if len(agySessions) != 2 {
		t.Fatalf("want 2 non-empty agy sessions, got %d", len(agySessions))
	}

	var found *Session
	for i := range agySessions {
		if agySessions[i].ID == "8ac63d53-9162-4a29-af29-bda3428931ae" {
			found = &agySessions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("session 8ac63d53-9162-4a29-af29-bda3428931ae not found")
	}

	if found.Title != "say hi" {
		t.Errorf("title = %q, want %q", found.Title, "say hi")
	}
	if !found.Transcript {
		t.Error("expected session to support Transcript")
	}
	if found.Input != 2104 || found.Output != 65 {
		t.Errorf("unexpected tokens: input=%d output=%d", found.Input, found.Output)
	}
}

func TestAgyTranscript(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	copyTree(t, "testdata/agy", filepath.Join(home, ".gemini", "antigravity"))

	Reset()
	s, ok := Find("agy", "8ac63d53-9162-4a29-af29-bda3428931ae")
	if !ok {
		t.Fatal("Find agy session failed")
	}

	tr, err := TranscriptOf(s)
	if err != nil {
		t.Fatalf("TranscriptOf failed: %v", err)
	}

	if len(tr.Parts) == 0 {
		t.Fatal("expected transcript parts, got 0")
	}

	hasUser := false
	hasAssistant := false
	for _, p := range tr.Parts {
		if p.Role == "user" && p.Text == "say hi" {
			hasUser = true
		}
		if p.Role == "assistant" && p.Text == "Hello from the fake." {
			hasAssistant = true
		}
	}

	if !hasUser {
		t.Error("transcript missing user part 'say hi'")
	}
	if !hasAssistant {
		t.Error("transcript missing assistant part 'Hello from the fake.'")
	}
}

// Conversations from older Antigravity versions (before token counting was added)
// have user prompt text and answers but no input_tokens or output_tokens fields.
// They must still appear in the session list with their title, and their conversation
// can be viewed via TranscriptOf.
func TestAgyLegacySessionWithoutTokens(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	dir := filepath.Join(home, ".gemini", "antigravity", "brain", "legacy-august-session", ".system_generated", "logs")
	writeFile(t, filepath.Join(dir, "transcript_full.jsonl"),
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-08-04T11:28:38Z","content":"<USER_REQUEST>\nFix floating window double click\n</USER_REQUEST>"}`+"\n"+
			`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-08-04T11:28:40Z","content":"I will inspect the floating window component."}`+"\n",
	)

	Reset()
	s, ok := Find("agy", "legacy-august-session")
	if !ok {
		t.Fatal("legacy agy session not found in List/Find")
	}

	if s.Title != "Fix floating window double click" {
		t.Errorf("title = %q, want %q", s.Title, "Fix floating window double click")
	}
	if s.Input != 0 || s.Output != 0 {
		t.Errorf("expected 0 tokens for legacy session, got input=%d output=%d", s.Input, s.Output)
	}

	tr, err := TranscriptOf(s)
	if err != nil {
		t.Fatalf("TranscriptOf failed: %v", err)
	}
	if len(tr.Parts) != 2 {
		t.Fatalf("want 2 parts, got %d", len(tr.Parts))
	}
	if tr.Parts[0].Text != "Fix floating window double click" || tr.Parts[0].Role != "user" {
		t.Errorf("part 0: %+v", tr.Parts[0])
	}
	if tr.Parts[1].Text != "I will inspect the floating window component." || tr.Parts[1].Role != "assistant" {
		t.Errorf("part 1: %+v", tr.Parts[1])
	}
}

// TestAgyStepDeduplicationAndFolding verifies:
// 1. A step rewritten in transcript_full.jsonl (e.g. IN_PROGRESS then DONE) is counted once.
// 2. Steps sharing a response_id (e.g. tool loop before and after) fold to the last one for tokens and replies,
// while preserving both tool call and assistant response in the transcript.
func TestAgyStepDeduplicationAndFolding(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	dir := filepath.Join(home, ".gemini", "antigravity", "brain", "folding-session", ".system_generated", "logs")
	writeFile(t, filepath.Join(dir, "transcript_full.jsonl"),
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-15T10:00:00Z","content":"<USER_REQUEST>\ncheck files\n</USER_REQUEST>"}`+"\n"+
			// Step 1 written first as IN_PROGRESS
			`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"IN_PROGRESS","created_at":"2026-10-15T10:00:02Z","content":"inspecting"}`+"\n"+
			// Step 1 updated to DONE with tool call and response_id "resp-101"
			`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-15T10:00:05Z","response_id":"resp-101","input_tokens":500,"output_tokens":20,"tool_calls":[{"name":"run_command","args":{"Cwd":"/tmp/repo","CommandLine":"ls"}}],"content":""}`+"\n"+
			// Step 2 in the same response (tool loop result consumed) sharing response_id "resp-101" with final tokens
			`{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-15T10:00:10Z","response_id":"resp-101","input_tokens":600,"output_tokens":50,"content":"Found 3 files."}`+"\n",
	)

	Reset()
	s, ok := Find("agy", "folding-session")
	if !ok {
		t.Fatal("folding session not found")
	}

	// Token usage should be from the folded step (600 in, 50 out), NOT 500+600=1100
	if s.Input != 600 || s.Output != 50 {
		t.Fatalf("tokens = in:%d out:%d, want in:600 out:50", s.Input, s.Output)
	}
	if s.Cwd != "/tmp/repo" {
		t.Errorf("cwd = %q, want /tmp/repo", s.Cwd)
	}

	// Transcript must include user prompt, tool call from step 1, and assistant text from step 2
	tr, err := TranscriptOf(s)
	if err != nil {
		t.Fatalf("TranscriptOf failed: %v", err)
	}
	if len(tr.Parts) != 3 {
		t.Fatalf("want 3 parts, got %d: %+v", len(tr.Parts), tr.Parts)
	}
	if tr.Parts[0].Role != "user" || tr.Parts[0].Text != "check files" {
		t.Errorf("part 0: %+v", tr.Parts[0])
	}
	if tr.Parts[1].Role != "assistant" || tr.Parts[1].Kind != "tool_use" || tr.Parts[1].Name != "run_command" {
		t.Errorf("part 1: %+v", tr.Parts[1])
	}
	if tr.Parts[2].Role != "assistant" || tr.Parts[2].Text != "Found 3 files." {
		t.Errorf("part 2: %+v", tr.Parts[2])
	}
}

// TestAgyModelDetection tests model recognition from:
// 1. Model Selection setting with human readable name (e.g. Gemini 3.6 Flash (High)).
// 2. Line JSON model field.
// 3. Fallback to settings.json in the parent Antigravity folder.
func TestAgyModelDetection(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")

	// Case 1: Model Selection with human-readable name in USER_SETTINGS_CHANGE
	dir1 := filepath.Join(home, ".gemini", "antigravity", "brain", "model-setting-session", ".system_generated", "logs")
	writeFile(t, filepath.Join(dir1, "transcript_full.jsonl"),
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-15T11:00:00Z","content":"<USER_REQUEST>\nhello\n</USER_REQUEST>\n<USER_SETTINGS_CHANGE>\nThe user changed setting `+"`Model Selection`"+` from None to Gemini 3.6 Flash (High).\n</USER_SETTINGS_CHANGE>"}`+"\n"+
			`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-15T11:00:02Z","input_tokens":100,"output_tokens":20,"content":"hi"}`+"\n",
	)

	// Case 2: Line JSON model field
	dir2 := filepath.Join(home, ".gemini", "antigravity", "brain", "line-model-session", ".system_generated", "logs")
	writeFile(t, filepath.Join(dir2, "transcript_full.jsonl"),
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-15T11:00:00Z","content":"<USER_REQUEST>\nhello line\n</USER_REQUEST>"}`+"\n"+
			`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-15T11:00:02Z","model":"gemini-3.8-flash-n","input_tokens":100,"output_tokens":20,"content":"hi"}`+"\n",
	)

	// Case 3: Fallback to settings.json in root folder
	cliRoot := filepath.Join(home, ".gemini", "antigravity-cli")
	writeFile(t, filepath.Join(cliRoot, "settings.json"), `{"model":"deepseek/deepseek-v4-pro"}`)
	dir3 := filepath.Join(cliRoot, "brain", "fallback-settings-session", ".system_generated", "logs")
	writeFile(t, filepath.Join(dir3, "transcript_full.jsonl"),
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-15T11:00:00Z","content":"<USER_REQUEST>\nhello fallback\n</USER_REQUEST>"}`+"\n"+
			`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-15T11:00:02Z","input_tokens":100,"output_tokens":20,"content":"hi"}`+"\n",
	)

	Reset()

	s1, ok := Find("agy", "model-setting-session")
	if !ok {
		t.Fatal("model-setting-session not found")
	}
	f1 := file{agent: "agy", key: "agy:" + s1.ID, path: filepath.Join(dir1, "transcript_full.jsonl"), main: true}
	stat(&f1)
	st1 := parseAgy(f1, nil)
	if st1.Agy.Model != "gemini-3.6-flash-high" {
		t.Errorf("case 1 model = %q, want gemini-3.6-flash-high", st1.Agy.Model)
	}

	s2, ok := Find("agy", "line-model-session")
	if !ok {
		t.Fatal("line-model-session not found")
	}
	f2 := file{agent: "agy", key: "agy:" + s2.ID, path: filepath.Join(dir2, "transcript_full.jsonl"), main: true}
	stat(&f2)
	st2 := parseAgy(f2, nil)
	if st2.Agy.Model != "gemini-3.8-flash-n" {
		t.Errorf("case 2 model = %q, want gemini-3.8-flash-n", st2.Agy.Model)
	}

	s3, ok := Find("agy", "fallback-settings-session")
	if !ok {
		t.Fatal("fallback-settings-session not found")
	}
	f3 := file{agent: "agy", key: "agy:" + s3.ID, path: filepath.Join(dir3, "transcript_full.jsonl"), main: true}
	stat(&f3)
	st3 := parseAgy(f3, nil)
	if st3.Agy.Model != "deepseek/deepseek-v4-pro" {
		t.Errorf("case 3 model = %q, want deepseek/deepseek-v4-pro", st3.Agy.Model)
	}
}
