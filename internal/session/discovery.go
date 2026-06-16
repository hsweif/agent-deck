package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

const botmuxSessionPrefix = "bmx-"

type botmuxSessionMetadata struct {
	SessionID  string `json:"sessionId"`
	Title      string `json:"title"`
	WorkingDir string `json:"workingDir"`
	CLIID      string `json:"cliId"`
	LarkAppID  string `json:"larkAppId"`
	Status     string `json:"status"`
}

// DiscoverExistingTmuxSessions finds all tmux sessions and converts them to instances
func DiscoverExistingTmuxSessions(existingInstances []*Instance) ([]*Instance, error) {
	// Get all tmux sessions
	tmuxSessions, err := tmux.DiscoverAllTmuxSessions()
	if err != nil {
		return nil, err
	}

	// Build a map of existing sessions by tmux name
	existingMap := make(map[string]bool)
	for _, inst := range existingInstances {
		if inst.GetTmuxSession() != nil {
			existingMap[inst.GetTmuxSession().Name] = true
		}
		// Also track by title
		existingMap[inst.Title] = true
	}

	botmuxMetadata := loadBotmuxSessionMetadata()
	var discovered []*Instance
	for _, sess := range tmuxSessions {
		// Skip if already tracked
		if existingMap[sess.Name] || existingMap[sess.DisplayName] {
			continue
		}

		// For orphaned agent-deck sessions, extract the original title from the tmux name
		// Format: agentdeck_<title>_<hash> -> extract <title>
		title := sess.DisplayName
		groupPath := ""
		isOrphaned := false
		if strings.HasPrefix(sess.Name, tmux.SessionPrefix) {
			isOrphaned = true
			// Extract title from session name: agentdeck_<title>_<8-char-hash>
			namePart := strings.TrimPrefix(sess.Name, tmux.SessionPrefix)
			if lastUnderscore := strings.LastIndex(namePart, "_"); lastUnderscore > 0 {
				title = namePart[:lastUnderscore]
			} else {
				title = namePart
			}
			// Put orphaned sessions in a "Recovered" group so user knows they were recovered
			groupPath = "recovered"
		}

		// Create instance for discovered session
		projectPath := sess.WorkDir
		if projectPath == "" {
			projectPath = "~"
		}

		// Enable mouse mode for proper scrolling in imported sessions
		// Ignore errors - non-fatal, older tmux versions may not support all options
		_ = sess.EnableMouseMode()

		// Determine tool type - for orphaned agent-deck sessions, assume claude (most common)
		tool := detectToolFromName(title)
		if isOrphaned && tool == "shell" {
			tool = "claude" // Most agent-deck sessions are Claude sessions
		}
		if strings.HasPrefix(sess.Name, botmuxSessionPrefix) {
			title, projectPath, tool, groupPath = botmuxImportFields(sess, botmuxMetadata)
			if existingMap[title] {
				continue
			}
		}

		inst := &Instance{
			ID:             GenerateID(),
			Title:          title,
			ProjectPath:    projectPath,
			GroupPath:      groupPath,
			Status:         StatusIdle,
			Tool:           tool,
			TmuxSocketName: sess.SocketName, // Inherit from the tmux session we discovered (#687)
			tmuxSession:    sess,
		}
		_ = inst.UpdateStatus()
		discovered = append(discovered, inst)
	}

	return discovered, nil
}

// DiscoverBotmuxTmuxSessions imports live botmux tmux sessions (`bmx-*`) as
// Agent Deck instances. It intentionally ignores every other tmux session so
// startup auto-import does not unexpectedly register a user's unrelated panes.
func DiscoverBotmuxTmuxSessions(existingInstances []*Instance) ([]*Instance, error) {
	tmuxSessions, err := tmux.DiscoverAllTmuxSessions()
	if err != nil {
		return nil, err
	}
	return discoverBotmuxSessionsFromTmux(tmuxSessions, existingInstances, loadBotmuxSessionMetadata())
}

func discoverBotmuxSessionsFromTmux(tmuxSessions []*tmux.Session, existingInstances []*Instance, metadata map[string]botmuxSessionMetadata) ([]*Instance, error) {
	existingMap := make(map[string]bool)
	for _, inst := range existingInstances {
		if inst.GetTmuxSession() != nil {
			existingMap[inst.GetTmuxSession().Name] = true
		}
		existingMap[inst.Title] = true
	}

	var discovered []*Instance
	for _, sess := range tmuxSessions {
		if !strings.HasPrefix(sess.Name, botmuxSessionPrefix) {
			continue
		}
		if existingMap[sess.Name] || existingMap[sess.DisplayName] {
			continue
		}

		title, projectPath, tool, groupPath := botmuxImportFields(sess, metadata)
		if existingMap[title] {
			continue
		}

		_ = sess.EnableMouseMode()
		inst := &Instance{
			ID:             GenerateID(),
			Title:          title,
			ProjectPath:    projectPath,
			GroupPath:      groupPath,
			Status:         StatusIdle,
			Tool:           tool,
			TmuxSocketName: sess.SocketName,
			tmuxSession:    sess,
		}
		_ = inst.UpdateStatus()
		discovered = append(discovered, inst)
		existingMap[sess.Name] = true
		existingMap[title] = true
	}

	return discovered, nil
}

func botmuxImportFields(sess *tmux.Session, metadata map[string]botmuxSessionMetadata) (title, projectPath, tool, groupPath string) {
	title = sess.DisplayName
	projectPath = sess.WorkDir
	tool = detectToolFromName(title)
	groupPath = "botmux"
	if meta, ok := findBotmuxMetadataForTmuxName(sess.Name, metadata); ok {
		if strings.TrimSpace(meta.Title) != "" {
			title = strings.TrimSpace(meta.Title)
		}
		if strings.TrimSpace(meta.WorkingDir) != "" {
			projectPath = strings.TrimSpace(meta.WorkingDir)
		}
		if cliID := normalizeBotmuxCLIID(meta.CLIID); cliID != "" {
			tool = cliID
			groupPath = "botmux/" + cliID
		}
	}
	if projectPath == "" {
		projectPath = "~"
	}
	if tool == "shell" {
		tool = detectToolFromName(sess.Name)
	}
	return title, projectPath, tool, groupPath
}

func findBotmuxMetadataForTmuxName(tmuxName string, metadata map[string]botmuxSessionMetadata) (botmuxSessionMetadata, bool) {
	prefix := strings.TrimPrefix(tmuxName, botmuxSessionPrefix)
	for sessionID, meta := range metadata {
		if strings.HasPrefix(sessionID, prefix) {
			return meta, true
		}
	}
	return botmuxSessionMetadata{}, false
}

func loadBotmuxSessionMetadata() map[string]botmuxSessionMetadata {
	result := make(map[string]botmuxSessionMetadata)
	for _, dir := range botmuxDataDirCandidates() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasPrefix(name, "sessions") || !strings.HasSuffix(name, ".json") {
				continue
			}
			mergeBotmuxSessionFile(filepath.Join(dir, name), result)
		}
		if len(result) > 0 {
			return result
		}
	}
	return result
}

func mergeBotmuxSessionFile(path string, result map[string]botmuxSessionMetadata) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var data map[string]botmuxSessionMetadata
	if err := json.Unmarshal(raw, &data); err != nil {
		return
	}
	for id, meta := range data {
		if meta.SessionID == "" {
			meta.SessionID = id
		}
		result[id] = meta
	}
}

func botmuxDataDirCandidates() []string {
	seen := make(map[string]bool)
	var dirs []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}

	add(os.Getenv("SESSION_DATA_DIR"))
	if home, err := os.UserHomeDir(); err == nil {
		if raw, err := os.ReadFile(filepath.Join(home, ".botmux", ".data-dir")); err == nil {
			add(strings.TrimSpace(string(raw)))
		}
		add(filepath.Join(home, ".botmux", "data"))
	}
	return dirs
}

func normalizeBotmuxCLIID(cliID string) string {
	cliID = strings.ToLower(strings.TrimSpace(cliID))
	switch cliID {
	case "claude-code", "claude_code", "claude":
		return "claude"
	case "open-code", "opencode":
		return "opencode"
	case "codex", "gemini", "cursor", "cursor-agent", "coco", "agy", "antigravity":
		return cliID
	default:
		return ""
	}
}

// GroupByProject groups sessions by their parent project directory
func GroupByProject(instances []*Instance) map[string][]*Instance {
	groups := make(map[string][]*Instance)

	for _, inst := range instances {
		// Extract parent directory name
		projectName := extractProjectName(inst.ProjectPath)
		groups[projectName] = append(groups[projectName], inst)
	}

	return groups
}

// FilterByQuery filters sessions by title, project path, tool, or status
// Supports status filters: "waiting", "running", "idle", "error"
func FilterByQuery(instances []*Instance, query string) []*Instance {
	if query == "" {
		return instances
	}

	query = strings.ToLower(strings.TrimSpace(query))

	// Check for status filters
	statusFilters := map[string]Status{
		"waiting": StatusWaiting,
		"running": StatusRunning,
		"idle":    StatusIdle,
		"error":   StatusError,
		"stopped": StatusStopped,
	}

	// If query matches a status filter exactly, filter by status
	if status, ok := statusFilters[query]; ok {
		return filterByStatus(instances, status)
	}

	// Regular fuzzy search on title, path, tool
	filtered := make([]*Instance, 0)

	for _, inst := range instances {
		if strings.Contains(strings.ToLower(inst.Title), query) ||
			strings.Contains(strings.ToLower(inst.ProjectPath), query) ||
			strings.Contains(strings.ToLower(inst.Tool), query) {
			filtered = append(filtered, inst)
		}
	}

	return filtered
}

// filterByStatus returns only instances with the specified status
func filterByStatus(instances []*Instance, status Status) []*Instance {
	filtered := make([]*Instance, 0)
	for _, inst := range instances {
		if inst.Status == status {
			filtered = append(filtered, inst)
		}
	}
	return filtered
}

// detectToolFromName tries to detect tool type from session name
func detectToolFromName(name string) string {
	nameLower := strings.ToLower(name)

	if strings.Contains(nameLower, "claude") {
		return "claude"
	}
	if strings.Contains(nameLower, "gemini") {
		return "gemini"
	}
	if strings.Contains(nameLower, "opencode") || strings.Contains(nameLower, "open-code") {
		return "opencode"
	}
	if strings.Contains(nameLower, "codex") {
		return "codex"
	}

	return "shell"
}

// extractProjectName extracts the parent directory name from a path
func extractProjectName(projectPath string) string {
	// Clean the path and split into parts
	cleanPath := filepath.Clean(projectPath)
	parts := strings.Split(cleanPath, string(filepath.Separator))

	// Filter out empty parts
	var filteredParts []string
	for _, part := range parts {
		if part != "" {
			filteredParts = append(filteredParts, part)
		}
	}

	// For /home/user/projects/devops, we want "projects" (second-to-last)
	if len(filteredParts) >= 2 {
		return filteredParts[len(filteredParts)-2]
	}

	// Fallback to the last part
	if len(filteredParts) > 0 {
		return filteredParts[len(filteredParts)-1]
	}

	return "unknown"
}
