package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestDiscoverExistingTmuxSessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Should not error even with no existing instances
	discovered, err := DiscoverExistingTmuxSessions([]*Instance{})
	if err != nil {
		t.Logf("DiscoverExistingTmuxSessions error (may be expected): %v", err)
	}
	_ = discovered
}

func TestDiscoverSkipsAgentDeckSessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Create a mock existing instance
	existing := []*Instance{
		{
			ID:          "test-123",
			Title:       "existing-session",
			ProjectPath: "/tmp",
		},
	}

	discovered, err := DiscoverExistingTmuxSessions(existing)
	if err != nil {
		t.Logf("Error (may be expected): %v", err)
	}

	// Should not include sessions that are already tracked
	for _, d := range discovered {
		if d.Title == "existing-session" {
			t.Error("Should not discover already tracked sessions")
		}
	}
}

func TestGroupByProjectDeep(t *testing.T) {
	instances := []*Instance{
		{Title: "s1", ProjectPath: "/home/user/projects/devops"},
		{Title: "s2", ProjectPath: "/home/user/projects/frontend"},
		{Title: "s3", ProjectPath: "/home/user/personal/blog"},
		{Title: "s4", ProjectPath: "/tmp"},
	}

	groups := GroupByProject(instances)

	// Check grouping
	if _, ok := groups["projects"]; !ok {
		t.Error("Expected 'projects' group")
	}
	if _, ok := groups["personal"]; !ok {
		t.Error("Expected 'personal' group")
	}
}

func TestFilterByQueryCaseInsensitive(t *testing.T) {
	instances := []*Instance{
		{Title: "DevOps-Claude", ProjectPath: "/tmp", Tool: "claude"},
		{Title: "frontend-shell", ProjectPath: "/tmp", Tool: "shell"},
	}

	// Should match case-insensitively
	result := FilterByQuery(instances, "DEVOPS")
	if len(result) != 1 {
		t.Errorf("Expected 1 result for 'DEVOPS', got %d", len(result))
	}

	result = FilterByQuery(instances, "Claude")
	if len(result) != 1 {
		t.Errorf("Expected 1 result for 'Claude', got %d", len(result))
	}
}

func TestDetectToolFromName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"Claude uppercase", "CLAUDE-session", "claude"},
		{"claude lowercase", "my-claude-session", "claude"},
		{"Gemini mixed case", "Gemini-AI", "gemini"},
		{"OpenCode", "opencode-session", "opencode"},
		{"Codex", "codex-test", "codex"},
		{"Unknown", "random-session", "shell"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectToolFromName(tt.input)
			if result != tt.expected {
				t.Errorf("detectToolFromName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestDiscoverBotmuxSessionsFromTmuxUsesMetadata(t *testing.T) {
	tmuxSessions := []*tmux.Session{
		{Name: "bmx-12345678", DisplayName: "bmx-12345678", WorkDir: "/fallback"},
		{Name: "unrelated", DisplayName: "unrelated", WorkDir: "/tmp"},
	}
	metadata := map[string]botmuxSessionMetadata{
		"12345678-aaaa-bbbb-cccc-123456789abc": {
			SessionID:  "12345678-aaaa-bbbb-cccc-123456789abc",
			Title:      "Fix login",
			WorkingDir: "/repo/app",
			CLIID:      "claude-code",
		},
	}

	discovered, err := discoverBotmuxSessionsFromTmux(tmuxSessions, nil, metadata)
	if err != nil {
		t.Fatalf("discoverBotmuxSessionsFromTmux returned error: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("discovered %d sessions, want 1", len(discovered))
	}
	got := discovered[0]
	if got.Title != "Fix login" {
		t.Fatalf("Title = %q, want %q", got.Title, "Fix login")
	}
	if got.ProjectPath != "/repo/app" {
		t.Fatalf("ProjectPath = %q, want /repo/app", got.ProjectPath)
	}
	if got.Tool != "claude" {
		t.Fatalf("Tool = %q, want claude", got.Tool)
	}
	if got.GroupPath != "botmux/claude" {
		t.Fatalf("GroupPath = %q, want botmux/claude", got.GroupPath)
	}
}

func TestDiscoverBotmuxSessionsFromTmuxFallsBackWithoutMetadata(t *testing.T) {
	discovered, err := discoverBotmuxSessionsFromTmux(
		[]*tmux.Session{{Name: "bmx-deadbeef", DisplayName: "bmx-deadbeef", WorkDir: "/repo"}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("discoverBotmuxSessionsFromTmux returned error: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("discovered %d sessions, want 1", len(discovered))
	}
	if discovered[0].Title != "bmx-deadbeef" {
		t.Fatalf("Title = %q, want bmx-deadbeef", discovered[0].Title)
	}
	if discovered[0].GroupPath != "botmux" {
		t.Fatalf("GroupPath = %q, want botmux", discovered[0].GroupPath)
	}
}

func TestDiscoverBotmuxSessionsFromTmuxSkipsExisting(t *testing.T) {
	existingTmux := tmux.NewSession("existing", "/repo")
	existingTmux.Name = "bmx-12345678"
	existing := []*Instance{{Title: "existing", tmuxSession: existingTmux}}

	discovered, err := discoverBotmuxSessionsFromTmux(
		[]*tmux.Session{{Name: "bmx-12345678", DisplayName: "bmx-12345678", WorkDir: "/repo"}},
		existing,
		nil,
	)
	if err != nil {
		t.Fatalf("discoverBotmuxSessionsFromTmux returned error: %v", err)
	}
	if len(discovered) != 0 {
		t.Fatalf("discovered %d sessions, want 0", len(discovered))
	}
}

func TestLoadBotmuxSessionMetadataUsesSessionDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_DATA_DIR", dir)
	raw := `{
		"abcdef12-0000-4000-8000-000000000000": {
			"title": "Review MR",
			"workingDir": "/repo/review",
			"cliId": "codex"
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, "sessions-lark-app.json"), []byte(raw), 0600); err != nil {
		t.Fatalf("write botmux sessions file: %v", err)
	}

	metadata := loadBotmuxSessionMetadata()
	got, ok := metadata["abcdef12-0000-4000-8000-000000000000"]
	if !ok {
		t.Fatalf("metadata missing botmux session")
	}
	if got.SessionID != "abcdef12-0000-4000-8000-000000000000" {
		t.Fatalf("SessionID = %q, want map key", got.SessionID)
	}
	if got.Title != "Review MR" || got.WorkingDir != "/repo/review" || got.CLIID != "codex" {
		t.Fatalf("metadata = %+v, want title/workingDir/cliId from file", got)
	}
}

func TestExtractProjectName(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{"Deep path", "/home/user/projects/devops", "projects"},
		{"Home path", "/home/user/personal/blog", "personal"},
		{"Root level", "/tmp", "tmp"},
		{"Single level", "/home", "home"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractProjectName(tt.path)
			if result != tt.expected {
				t.Errorf("extractProjectName(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}
