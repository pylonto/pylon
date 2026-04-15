package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/cron"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/pylonto/pylon/internal/store"
)

// detailModel shows a single pylon's config and recent jobs.
type detailModel struct {
	name           string
	pylon          *config.PylonConfig
	global         *config.GlobalConfig
	jobs           []*store.Job
	cursor         int
	focused        bool // true when the detail pane has keyboard focus
	showFullPrompt bool
	showJobs       bool // toggle jobs section visibility
	confirmKill    bool // when true, waiting for y/n to confirm kill
	confirmDismiss bool // when true, waiting for y/n to confirm dismiss
	confirmRetry   bool // when true, waiting for y/n to confirm retry
	confirmFire    bool // when true, waiting for y/n to confirm manual trigger
	copyFlash      copyFlashModel
	err            error
	warning        string // non-blocking config validation warning

	// Alert template builder
	alertBuilder    bool            // true when in builder mode
	alertGroups     []alertGroup    // grouped payload fields
	alertRootFields []alertField    // ungrouped root-level fields
	alertExpanded   map[string]bool // which groups are expanded
	alertCursor     int
	alertChecked    map[string]bool // path -> checked
}

// maxGroupDepth is the number of levels of collapsible sub-groups.
// Fields deeper than this show relative paths instead of getting their own header.
const maxGroupDepth = 3

// alertGroup holds a category of payload fields with recursive sub-groups.
type alertGroup struct {
	key       string       // local key (e.g. "values")
	fullKey   string       // full dot path (e.g. "event.exception.values") for expand state
	fields    []alertField // leaf fields at this level
	subgroups []alertGroup // child groups (up to maxGroupDepth)
}

// alertField holds a single payload field path and its sample value.
type alertField struct {
	path        string // full original path (e.g. "event.exception.values.type")
	displayName string // shown in UI (e.g. "type" or "meta.source" for deep fields)
	value       string
}

// alertItem represents one visible row in the builder UI.
type alertItem struct {
	isHeader     bool
	isRoot       bool   // true for ungrouped root-level fields
	groupKey     string // local key for display (headers only)
	groupFullKey string // full path for expand/collapse (headers only)
	path         string // full dot path (fields only)
	displayName  string // display name (fields only)
	value        string // sample value (fields only)
	childCount   int    // total descendant count (collapsed headers)
	depth        int    // nesting depth for indentation
}

// alertVisibleItems returns the flattened list of currently visible rows,
// accounting for collapsed/expanded group state.
func (m detailModel) alertVisibleItems() []alertItem {
	var items []alertItem
	m.appendGroupItems(&items, m.alertGroups, 0)
	for _, f := range m.alertRootFields {
		items = append(items, alertItem{
			path: f.path, displayName: f.displayName,
			value: f.value, isRoot: true, depth: 0,
		})
	}
	return items
}

func (m detailModel) appendGroupItems(items *[]alertItem, groups []alertGroup, depth int) {
	for _, g := range groups {
		expanded := m.alertExpanded[g.fullKey]
		*items = append(*items, alertItem{
			isHeader: true, groupKey: g.key, groupFullKey: g.fullKey,
			childCount: countDescendants(g), depth: depth,
		})
		if expanded {
			m.appendGroupItems(items, g.subgroups, depth+1)
			for _, f := range g.fields {
				*items = append(*items, alertItem{
					path: f.path, displayName: f.displayName,
					value: f.value, depth: depth + 1,
				})
			}
		}
	}
}

func countDescendants(g alertGroup) int {
	count := len(g.fields)
	for _, sg := range g.subgroups {
		count += countDescendants(sg)
	}
	return count
}

// buildAlertGroups organizes flat paths into a recursive tree of groups.
// Paths are grouped by dot-segment up to maxGroupDepth levels; deeper
// fields show their remaining path as the display name.
func buildAlertGroups(paths []string, values map[string]string) ([]alertGroup, []alertField) {
	root := &pathNode{children: make(map[string]*pathNode)}
	for _, p := range paths {
		segments := strings.Split(p, ".")
		node := root
		for i, seg := range segments {
			if i == len(segments)-1 {
				node.leafPaths = append(node.leafPaths, p)
			} else {
				if node.children[seg] == nil {
					node.children[seg] = &pathNode{children: make(map[string]*pathNode)}
					node.childOrder = append(node.childOrder, seg)
				}
				node = node.children[seg]
			}
		}
	}
	return convertNode(root, values, 0, "")
}

// pathNode is an intermediate tree used while building alert groups.
type pathNode struct {
	children   map[string]*pathNode
	childOrder []string
	leafPaths  []string // full original paths of leaves at this node
}

func convertNode(node *pathNode, values map[string]string, depth int, prefix string) ([]alertGroup, []alertField) {
	var groups []alertGroup
	var fields []alertField

	for _, p := range node.leafPaths {
		segments := strings.Split(p, ".")
		fields = append(fields, alertField{
			path: p, displayName: segments[len(segments)-1], value: values[p],
		})
	}

	for _, childKey := range node.childOrder {
		child := node.children[childKey]
		childPrefix := childKey
		if prefix != "" {
			childPrefix = prefix + "." + childKey
		}
		if depth >= maxGroupDepth-1 {
			// At max depth: flatten all descendants as fields with relative paths
			var descPaths []string
			collectDescendantPaths(child, &descPaths)
			for _, p := range descPaths {
				segments := strings.Split(p, ".")
				// Display name is the relative path from this point
				relParts := segments[depth+1:]
				fields = append(fields, alertField{
					path: p, displayName: strings.Join(relParts, "."), value: values[p],
				})
			}
		} else {
			subgroups, subfields := convertNode(child, values, depth+1, childPrefix)
			groups = append(groups, alertGroup{
				key: childKey, fullKey: childPrefix,
				subgroups: subgroups, fields: subfields,
			})
		}
	}

	return groups, fields
}

func collectDescendantPaths(node *pathNode, out *[]string) {
	*out = append(*out, node.leafPaths...)
	for _, key := range node.childOrder {
		collectDescendantPaths(node.children[key], out)
	}
}

// collectGroupKeys returns all fullKeys in the group tree (for auto-expanding).
func collectGroupKeys(groups []alertGroup) []string {
	var keys []string
	for _, g := range groups {
		keys = append(keys, g.fullKey)
		keys = append(keys, collectGroupKeys(g.subgroups)...)
	}
	return keys
}

// collectCheckedFields gathers all checked fields from the recursive group tree and root fields.
func collectCheckedFields(groups []alertGroup, rootFields []alertField, checked map[string]bool) []string {
	var lines []string
	for _, g := range groups {
		lines = append(lines, collectCheckedFields(g.subgroups, nil, checked)...)
		for _, f := range g.fields {
			if checked[f.path] {
				lines = append(lines, deriveLabel(f.path)+": {{ .body."+f.path+" }}")
			}
		}
	}
	for _, f := range rootFields {
		if checked[f.path] {
			lines = append(lines, deriveLabel(f.path)+": {{ .body."+f.path+" }}")
		}
	}
	return lines
}

// preCheckFields marks fields as checked if they appear in an existing message template.
func preCheckFields(groups []alertGroup, rootFields []alertField, message string, checked map[string]bool) {
	for _, g := range groups {
		preCheckFields(g.subgroups, nil, message, checked)
		for _, f := range g.fields {
			if strings.Contains(message, ".body."+f.path) {
				checked[f.path] = true
			}
		}
	}
	for _, f := range rootFields {
		if strings.Contains(message, ".body."+f.path) {
			checked[f.path] = true
		}
	}
}

// deriveLabel converts a dot path's last segment to a human-readable label.
// e.g. "issue.title" -> "Title", "repository.full_name" -> "Full Name"
func deriveLabel(path string) string {
	parts := strings.Split(path, ".")
	last := parts[len(parts)-1]
	words := strings.Split(last, "_")
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func newDetailModel(name string) detailModel {
	return detailModel{name: name, showJobs: true}
}

type detailLoadedMsg struct {
	pylon   *config.PylonConfig
	global  *config.GlobalConfig
	jobs    []*store.Job
	err     error
	warning string // validation warning (non-blocking)
}

// jobsRefreshedMsg carries only updated job data (no config reload).
type jobsRefreshedMsg struct {
	jobs []*store.Job
}

func (m detailModel) Init() tea.Cmd {
	name := m.name
	return func() tea.Msg {
		pyl, err := config.LoadPylonRaw(name)
		if err != nil {
			return detailLoadedMsg{err: err}
		}
		global, _ := config.LoadGlobal()

		// Validate separately -- config errors become non-blocking warnings.
		var warning string
		if verr := pyl.Validate(config.PylonPath(name)); verr != nil {
			warning = verr.Error()
		}

		var jobs []*store.Job
		dbPath := config.PylonDBPath(name)
		if _, statErr := os.Stat(dbPath); statErr == nil {
			if s, openErr := store.Open(dbPath); openErr == nil {
				jobs, _ = s.RecentJobs(name, 20)
				_ = s.Close()
			}
		}

		// Check running/active jobs against live containers.
		// If the container is gone, mark as stale (or bootstrapping if very new).
		for _, j := range jobs {
			if !isRunningStatus(j.Status) {
				continue
			}
			out, cerr := exec.Command("docker", "ps", "-a", "--filter",
				fmt.Sprintf("label=pylon.job=%s", j.ID), "--format", "{{.ID}}").Output()
			if cerr != nil || strings.TrimSpace(string(out)) == "" {
				j.Status = orphanJobStatus(j.CreatedAt)
			}
		}

		return detailLoadedMsg{pylon: pyl, global: global, jobs: jobs, warning: warning}
	}
}

// refreshJobs re-queries only the job list without reloading config.
func (m detailModel) refreshJobs() tea.Cmd {
	name := m.name
	return func() tea.Msg {
		var jobs []*store.Job
		dbPath := config.PylonDBPath(name)
		if _, statErr := os.Stat(dbPath); statErr == nil {
			if s, openErr := store.Open(dbPath); openErr == nil {
				jobs, _ = s.RecentJobs(name, 20)
				_ = s.Close()
			}
		}
		for _, j := range jobs {
			if !isRunningStatus(j.Status) {
				continue
			}
			out, cerr := exec.Command("docker", "ps", "-a", "--filter",
				fmt.Sprintf("label=pylon.job=%s", j.ID), "--format", "{{.ID}}").Output()
			if cerr != nil || strings.TrimSpace(string(out)) == "" {
				j.Status = orphanJobStatus(j.CreatedAt)
			}
		}
		return jobsRefreshedMsg{jobs: jobs}
	}
}

func (m detailModel) Update(msg tea.Msg) (detailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case detailLoadedMsg:
		m.pylon = msg.pylon
		m.global = msg.global
		m.jobs = msg.jobs
		m.err = msg.err
		m.warning = msg.warning

	case jobsRefreshedMsg:
		m.jobs = msg.jobs

	case detailEditorDoneMsg:
		// Reload config after editing
		return m, m.Init()

	case copiedMsg:
		var cmd tea.Cmd
		m.copyFlash, cmd = m.copyFlash.show(msg.label)
		return m, cmd

	case copyFlashClearMsg:
		m.copyFlash = m.copyFlash.Update(msg)

	case containerFoundMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		switch msg.action {
		case "logs":
			if msg.containerID != "" {
				c := exec.Command("docker", "logs", "-f", "--tail", "50", msg.containerID)
				return m, tea.ExecProcess(c, func(err error) tea.Msg {
					return detailEditorDoneMsg{err: err}
				})
			}
			// Container gone -- show persistent log file.
			pager := os.Getenv("PAGER")
			if pager == "" {
				pager = "less"
			}
			c := exec.Command(pager, msg.logFile)
			return m, tea.ExecProcess(c, func(err error) tea.Msg {
				return detailEditorDoneMsg{err: err}
			})
		case "kill":
			return m, killContainerCmd(msg.containerID)
		}
		return m, nil

	case jobKilledMsg:
		m.confirmKill = false
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.Init()

	case jobDismissedMsg:
		m.confirmDismiss = false
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.Init()

	case jobRetriedMsg:
		m.confirmRetry = false
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.Init()

	case pylonTriggeredMsg:
		if msg.err != nil {
			var cmd tea.Cmd
			m.copyFlash, cmd = m.copyFlash.show("error: " + msg.err.Error())
			return m, cmd
		}
		m.showJobs = true
		var cmd tea.Cmd
		m.copyFlash, cmd = m.copyFlash.show("triggered")
		return m, tea.Batch(cmd, m.Init())

	case tea.KeyMsg:
		// Confirmation mode intercepts all keys
		if m.confirmKill || m.confirmDismiss || m.confirmRetry || m.confirmFire {
			switch msg.String() {
			case "y":
				if m.confirmFire {
					m.confirmFire = false
					return m, triggerPylonCmd(m.name, m.global)
				}
				j := m.selectedJob()
				if j != nil {
					if m.confirmKill {
						m.confirmKill = false
						return m, findContainerCmd(j.ID, "kill")
					}
					if m.confirmRetry {
						m.confirmRetry = false
						return m, retryJobCmd(m.name, j.ID, m.global)
					}
					m.confirmDismiss = false
					return m, dismissJobCmd(m.name, j.ID)
				}
				m.confirmKill = false
				m.confirmDismiss = false
				m.confirmRetry = false
			default:
				m.confirmKill = false
				m.confirmDismiss = false
				m.confirmRetry = false
				m.confirmFire = false
			}
			return m, nil
		}

		// Hard error: pressing enter reloads.
		if m.err != nil {
			m.err = nil
			return m, m.Init()
		}

		// Alert builder mode
		if m.alertBuilder {
			items := m.alertVisibleItems()
			switch msg.String() {
			case keyUp, keyK:
				if m.alertCursor > 0 {
					m.alertCursor--
				}
			case keyDown, keyJ:
				if m.alertCursor < len(items)-1 {
					m.alertCursor++
				}
			case " ":
				if m.alertCursor < len(items) {
					item := items[m.alertCursor]
					if item.isHeader {
						m.alertExpanded[item.groupFullKey] = !m.alertExpanded[item.groupFullKey]
						newItems := m.alertVisibleItems()
						if m.alertCursor >= len(newItems) {
							m.alertCursor = len(newItems) - 1
						}
					} else {
						m.alertChecked[item.path] = !m.alertChecked[item.path]
					}
				}
			case keyEnter:
				// Build labeled template from checked fields and save
				lines := collectCheckedFields(m.alertGroups, m.alertRootFields, m.alertChecked)
				if len(lines) > 0 && m.pylon != nil {
					if m.pylon.Channel == nil {
						m.pylon.Channel = &config.PylonChannel{}
					}
					m.pylon.Channel.Message = strings.Join(lines, "\n")
					config.SavePylon(m.pylon)
				}
				m.alertBuilder = false
				return m, m.Init()
			case keyEsc:
				m.alertBuilder = false
			}
			return m, nil
		}

		switch msg.String() {
		case keyUp, keyK:
			if m.showJobs && m.cursor > 0 {
				m.cursor--
			}
		case keyDown, keyJ:
			if m.showJobs && m.cursor < len(m.jobs)-1 {
				m.cursor++
			}
		case keyY:
			if url := m.webhookURL(); url != "" {
				return m, copyToClipboard(url, "Copied webhook URL!")
			}
		case keyP:
			m.showFullPrompt = !m.showFullPrompt
		case keyT:
			m.showJobs = !m.showJobs
		case keyE:
			return m, m.openEditor()
		case keyF:
			if m.pylon != nil && m.pylon.Trigger.Type == "cron" {
				m.confirmFire = true
			}
		case keyA:
			if paths, values := m.loadPayloadPaths(); len(paths) > 0 {
				groups, rootFields := buildAlertGroups(paths, values)
				m.alertBuilder = true
				m.alertGroups = groups
				m.alertRootFields = rootFields
				m.alertExpanded = make(map[string]bool)
				for _, key := range collectGroupKeys(groups) {
					m.alertExpanded[key] = true
				}
				m.alertCursor = 0
				m.alertChecked = make(map[string]bool)
				if m.pylon != nil && m.pylon.Channel != nil {
					preCheckFields(groups, rootFields, m.pylon.Channel.Message, m.alertChecked)
				}
			}
		case keyL:
			if j := m.selectedJob(); j != nil {
				return m, findContainerCmd(j.ID, "logs")
			}
		case keyX:
			if j := m.selectedJob(); j != nil {
				if isRunningStatus(j.Status) {
					m.confirmKill = true
				} else {
					m.confirmDismiss = true
				}
			}
		case keyR:
			if j := m.selectedJob(); j != nil && (j.Status == "failed" || j.Status == "timeout") {
				m.confirmRetry = true
			}
		}
	}
	return m, nil
}

// webhookURL returns the full public webhook URL, or empty if not a webhook pylon.
func (m detailModel) webhookURL() string {
	if m.pylon == nil || m.pylon.Trigger.Type != "webhook" || m.global == nil {
		return ""
	}
	return m.pylon.ResolvePublicURL(m.global)
}

func (m detailModel) View(width, height int) string {
	if m.alertBuilder {
		return m.renderAlertBuilder()
	}
	if m.err != nil {
		return statusFailed.Render(fmt.Sprintf("  Error: %v", m.err)) + "\n" +
			mutedStyle.Render("  [enter] reload")
	}
	if m.pylon == nil {
		return mutedStyle.Render("  Loading...")
	}

	out := m.renderConfig()

	if m.warning != "" {
		out += "\n" + statusFailed.Render(fmt.Sprintf("  Warning: %s", m.warning))
	}

	if m.confirmFire {
		out += "\n  " + lipgloss.NewStyle().Foreground(colorWarning).Render("Fire this pylon now?") + " " + mutedStyle.Render("y/n")
	}

	if m.showJobs {
		out += "\n" + m.renderJobs()
	}

	flash := m.copyFlash.View()
	if flash != "" {
		out += "\n  " + flash
	}
	return out
}

func (m detailModel) renderConfig() string {
	pyl := m.pylon
	global := m.global

	s := ""
	s += "  " + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(pyl.Name) + "\n"
	if pyl.Description != "" {
		s += "  " + subtextStyle.Render(pyl.Description) + "\n"
	}
	s += "\n"

	row := func(label, value string) string {
		return fmt.Sprintf("  %s  %s\n",
			mutedStyle.Width(12).Render(label),
			lipgloss.NewStyle().Foreground(colorText).Render(value))
	}

	// Trigger
	trigger := pyl.Trigger.Type
	if pyl.Trigger.Path != "" {
		trigger += " " + pyl.Trigger.Path
	}
	if pyl.Trigger.Cron != "" {
		trigger += " " + pyl.Trigger.Cron
		if desc := describeCronExpr(pyl.Trigger.Cron); desc != pyl.Trigger.Cron {
			trigger += "  " + mutedStyle.Render(desc)
		}
	}
	s += row("Trigger", trigger)

	// Timezone and next run for cron pylons
	if pyl.Trigger.Type == "cron" && global != nil {
		loc := pyl.ResolveTimezone(global)
		s += row("Timezone", loc.String())
		next := cron.NextFire(pyl.Trigger.Cron, loc)
		if !next.IsZero() {
			until := time.Until(next)
			var durStr string
			if until < time.Minute {
				durStr = "< 1m"
			} else if until < time.Hour {
				durStr = fmt.Sprintf("%dm", int(until.Minutes()))
			} else if until < 24*time.Hour {
				h := int(until.Hours())
				m := int(until.Minutes()) % 60
				durStr = fmt.Sprintf("%dh %dm", h, m)
			} else {
				d := int(until.Hours()) / 24
				h := int(until.Hours()) % 24
				durStr = fmt.Sprintf("%dd %dh", d, h)
			}
			nextStr := next.Format("Mon Jan 02 15:04") + "  " + mutedStyle.Render("(in "+durStr+")")
			s += row("Next run", nextStr)
		}
	}

	// Webhook URL
	if pyl.Trigger.Type == "webhook" && global != nil {
		url := pyl.ResolvePublicURL(global)
		if m.focused {
			s += row("Webhook", url+"  "+mutedStyle.Render("[y] copy"))
		} else {
			s += row("Webhook", url)
		}
	}

	// Workspace
	ws := pyl.Workspace.Type
	if pyl.Workspace.Repo != "" {
		ws += " " + pyl.Workspace.Repo
		if pyl.Workspace.Ref != "" {
			ws += " (" + pyl.Workspace.Ref + ")"
		}
	}
	if pyl.Workspace.Path != "" {
		ws += " " + pyl.Workspace.Path
	}
	s += row("Workspace", ws)

	// Agent
	if pyl.Agent != nil {
		agent := pyl.Agent.Type
		if agent == "" && global != nil {
			agent = global.Defaults.Agent.Type
		}
		if pyl.Agent.Auth != "" {
			agent += " (" + pyl.Agent.Auth + ")"
		}
		s += row("Agent", agent)
	}

	// Channel
	channelType := "default"
	if pyl.Channel != nil && pyl.Channel.Type != "" {
		channelType = pyl.Channel.Type
	}
	s += row("Channel", channelType)

	// Topic template
	if pyl.Channel != nil && pyl.Channel.Topic != "" {
		topic := strings.ReplaceAll(pyl.Channel.Topic, "\n", " ")
		maxLen := 36
		if len(topic) > maxLen {
			topic = topic[:maxLen-3] + "..."
		}
		s += row("Topic", topic)
	}

	// Message template
	if pyl.Channel != nil && pyl.Channel.Message != "" {
		msg := strings.ReplaceAll(pyl.Channel.Message, "\n", " ")
		maxLen := 36
		if len(msg) > maxLen {
			msg = msg[:maxLen-3] + "..."
		}
		s += row("Message", msg)
	}

	// Auto-run (inverted from Approval)
	if pyl.Channel != nil && pyl.Channel.Approval {
		s += row("Auto-run", "no")
	} else {
		s += row("Auto-run", "yes")
	}

	// Prompt
	if pyl.Agent != nil && pyl.Agent.Prompt != "" {
		if m.showFullPrompt {
			if m.focused {
				s += row("Prompt", mutedStyle.Render("[p] collapse"))
			} else {
				s += row("Prompt", "")
			}
			s += indentBlock(subtextStyle.Render(pyl.Agent.Prompt), "  ") + "\n"
		} else {
			// Flatten to single line
			prompt := strings.ReplaceAll(pyl.Agent.Prompt, "\n", " ")
			maxLen := 24
			if len(prompt) > maxLen {
				prompt = prompt[:maxLen-3] + "..."
			}
			if m.focused {
				s += row("Prompt", prompt+"  "+mutedStyle.Render("[p] expand"))
			} else {
				s += row("Prompt", prompt)
			}
		}
	}

	return s
}

func (m detailModel) renderJobs() string {
	if len(m.jobs) == 0 {
		return mutedStyle.Render("  No jobs yet.")
	}

	colID := 10
	colStatus := 14
	colTriggered := 14

	header := tableHeaderStyle.Render(
		fmt.Sprintf("   %-*s  %-*s  %-*s  %s",
			colID, "ID",
			colStatus, "STATUS",
			colTriggered, "TRIGGERED",
			"DURATION"))

	var rows string
	for i, j := range m.jobs {
		id := j.ID
		if len(id) > 8 {
			id = id[:8]
		}

		status := renderJobStatus(j.Status)
		triggered := "-"
		if !j.CreatedAt.IsZero() {
			triggered = j.CreatedAt.Local().Format("Jan 2 15:04")
		}
		duration := "-"
		if j.CompletedAt != nil && !j.CompletedAt.IsZero() {
			start := j.CreatedAt
			if j.StartedAt != nil && !j.StartedAt.IsZero() {
				start = *j.StartedAt
			}
			if !start.IsZero() {
				d := j.CompletedAt.Sub(start)
				if d >= 0 {
					duration = formatDuration(d)
				}
			}
		}

		cursor := " "
		style := tableRowStyle
		if i == m.cursor && m.focused {
			cursor = cursorStyle.Render("◆")
			style = selectedRowStyle
		}

		statusPad := colStatus - lipgloss.Width(status)
		if statusPad < 0 {
			statusPad = 0
		}

		line := cursor + fmt.Sprintf(" %-*s  ", colID, id) +
			status + spaces(statusPad) +
			fmt.Sprintf("  %-*s  %s", colTriggered, triggered, duration)
		rows += style.Render(line) + "\n"
		if i == m.cursor && m.focused && j.Error != "" {
			rows += "    " + statusFailed.Render(j.Error) + "\n"
		}
	}

	out := header + "\n" + rows

	if m.confirmKill {
		out += "\n  " + statusFailed.Render("Kill this job?") + " " + mutedStyle.Render("y/n")
	}
	if m.confirmDismiss {
		out += "\n  " + statusFailed.Render("Dismiss this job?") + " " + mutedStyle.Render("y/n")
	}
	if m.confirmRetry {
		out += "\n  " + lipgloss.NewStyle().Foreground(colorWarning).Render("Retry this job?") + " " + mutedStyle.Render("y/n")
	}

	return out
}

func renderJobStatus(status string) string {
	switch status {
	case "completed":
		return statusActive.Render("completed")
	case "failed", "timeout":
		return statusFailed.Render(status)
	case "dismissed":
		return mutedStyle.Render("dismissed")
	case "running", "active":
		return statusActive.Render("running")
	case "awaiting_approval":
		return lipgloss.NewStyle().Foreground(colorWarning).Render("approval")
	case "bootstrapping":
		return lipgloss.NewStyle().Foreground(colorAccent).Render("bootstrapping")
	case "stale":
		return lipgloss.NewStyle().Foreground(colorWarning).Render("stale")
	default:
		return mutedStyle.Render(status)
	}
}

func formatDuration(d interface{ Seconds() float64 }) string {
	secs := d.Seconds()
	if secs < 60 {
		return fmt.Sprintf("%.0fs", secs)
	}
	mins := int(secs) / 60
	remSecs := int(secs) % 60
	if mins < 60 {
		return fmt.Sprintf("%dm%ds", mins, remSecs)
	}
	hours := mins / 60
	remMins := mins % 60
	return fmt.Sprintf("%dh%dm", hours, remMins)
}

// selectedJob returns the currently selected job, or nil.
func (m detailModel) selectedJob() *store.Job {
	if len(m.jobs) == 0 || m.cursor >= len(m.jobs) {
		return nil
	}
	return m.jobs[m.cursor]
}

func isRunningStatus(status string) bool {
	return status == "running" || status == "active" || status == "bootstrapping"
}

// orphanJobStatus returns the display status for a running job whose
// container is not found. Recently created jobs get "bootstrapping";
// older ones get "stale".
func orphanJobStatus(createdAt time.Time) string {
	if time.Since(createdAt) < 2*time.Minute {
		return "bootstrapping"
	}
	return "stale"
}

// jobKilledMsg is sent after a kill attempt.
type jobKilledMsg struct {
	err error
}

// containerFoundMsg carries the resolved container ID for a job.
type containerFoundMsg struct {
	containerID string
	logFile     string // fallback log file path when container is gone
	action      string // "logs" or "kill"
	err         error
}

// findContainerCmd looks up the container ID for a job.
// If the container is gone, it falls back to the persistent log file.
func findContainerCmd(jobID, action string) tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command("docker", "ps", "-a", "--filter",
			fmt.Sprintf("label=pylon.job=%s", jobID), "--format", "{{.ID}}").Output()
		if err != nil {
			return containerFoundMsg{err: fmt.Errorf("docker not reachable -- is the Docker daemon running? (docker ps to check): %w", err)}
		}
		containerID := strings.TrimSpace(string(out))
		if containerID == "" {
			// Container gone -- check for persistent log file.
			logPath := runner.LogPath(jobID)
			if _, err := os.Stat(logPath); err == nil {
				return containerFoundMsg{logFile: logPath, action: action}
			}
			id := jobID
			if len(id) > 8 {
				id = id[:8]
			}
			return containerFoundMsg{err: fmt.Errorf("no logs available for job %s -- container was removed and no log file was saved", id)}
		}
		return containerFoundMsg{containerID: containerID, action: action}
	}
}

// killContainerCmd kills a container by ID.
func killContainerCmd(containerID string) tea.Cmd {
	return func() tea.Msg {
		c := exec.Command("docker", "kill", containerID)
		if err := c.Run(); err != nil {
			return jobKilledMsg{err: err}
		}
		return jobKilledMsg{}
	}
}

// jobDismissedMsg is sent after a dismiss attempt.
type jobDismissedMsg struct{ err error }

// dismissJobCmd marks a job as dismissed in the database.
func dismissJobCmd(pylonName, jobID string) tea.Cmd {
	return func() tea.Msg {
		s, err := store.Open(config.PylonDBPath(pylonName))
		if err != nil {
			return jobDismissedMsg{err: err}
		}
		defer s.Close()
		s.UpdateStatus(jobID, "dismissed")
		return jobDismissedMsg{}
	}
}

// jobRetriedMsg is sent after a retry attempt.
type jobRetriedMsg struct{ err error }

// retryJobCmd re-triggers a job by POSTing the original payload to the daemon webhook.
func retryJobCmd(pylonName, jobID string, global *config.GlobalConfig) tea.Cmd {
	return func() tea.Msg {
		// Load the original trigger payload from the database.
		s, err := store.Open(config.PylonDBPath(pylonName))
		if err != nil {
			return jobRetriedMsg{err: fmt.Errorf("cannot open job database (%s): %w", config.PylonDBPath(pylonName), err)}
		}
		defer s.Close()

		var payload string
		s.DB().QueryRow("SELECT trigger_payload FROM jobs WHERE id = ?", jobID).Scan(&payload) //nolint:errcheck // checked via empty string
		if payload == "" {
			return jobRetriedMsg{err: fmt.Errorf("no trigger payload stored for this job -- only webhook-triggered jobs store payloads for retry")}
		}

		// Load the pylon config to get the webhook path.
		pyl, err := config.LoadPylon(pylonName)
		if err != nil || pyl.Trigger.Path == "" {
			return jobRetriedMsg{err: fmt.Errorf("cannot resolve webhook path for %q -- check that trigger.path is set in the pylon config", pylonName)}
		}

		port := 8090
		if global != nil && global.Server.Port != 0 {
			port = global.Server.Port
		}
		url := fmt.Sprintf("http://localhost:%d%s", port, pyl.Trigger.Path)

		resp, err := http.Post(url, "application/json", strings.NewReader(payload))
		if err != nil {
			return jobRetriedMsg{err: fmt.Errorf("retry failed -- is the pylon daemon running? start it with: pylon up\n  %w", err)}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted {
			body, _ := io.ReadAll(resp.Body)
			return jobRetriedMsg{err: fmt.Errorf("daemon rejected retry (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))}
		}

		return jobRetriedMsg{}
	}
}

// pylonTriggeredMsg is sent after a manual trigger attempt.
type pylonTriggeredMsg struct{ err error }

// triggerPylonCmd fires a pylon via the daemon's /trigger/ endpoint.
func triggerPylonCmd(pylonName string, global *config.GlobalConfig) tea.Cmd {
	return func() tea.Msg {
		port := 8090
		if global != nil && global.Server.Port != 0 {
			port = global.Server.Port
		}
		url := fmt.Sprintf("http://localhost:%d/trigger/%s", port, pylonName)

		resp, err := http.Post(url, "application/json", strings.NewReader("{}"))
		if err != nil {
			return pylonTriggeredMsg{err: fmt.Errorf("trigger failed -- is the pylon daemon running? start it with: pylon up\n  %w", err)}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted {
			body, _ := io.ReadAll(resp.Body)
			return pylonTriggeredMsg{err: fmt.Errorf("daemon rejected trigger (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))}
		}

		return pylonTriggeredMsg{}
	}
}

// loadPayloadPaths extracts flattened field paths from the stored payload sample,
// falling back to the most recent job for pre-migration databases.
func (m detailModel) loadPayloadPaths() ([]string, map[string]string) {
	dbPath := config.PylonDBPath(m.name)
	s, err := store.Open(dbPath)
	if err != nil {
		return nil, nil
	}
	defer s.Close()

	// Prefer the dedicated sample (always from a real webhook).
	payload := s.LoadPayloadSample(m.name)

	// Fallback: most recent job (pre-migration databases).
	if payload == "" {
		jobs, _ := s.RecentJobs(m.name, 1)
		if len(jobs) == 0 {
			return nil, nil
		}
		s.DB().QueryRow("SELECT trigger_payload FROM jobs WHERE id = ?", jobs[0].ID).Scan(&payload) //nolint:errcheck // checked via empty string
	}

	if payload == "" {
		return nil, nil
	}
	var body map[string]interface{}
	if json.Unmarshal([]byte(payload), &body) != nil {
		return nil, nil
	}
	var paths []string
	values := make(map[string]string)
	flattenPaths(body, "", &paths, values)
	return paths, values
}

// flattenPaths recursively extracts dot-separated paths and sample values from a JSON object.
// Skips internal/noisy fields (underscore-prefixed), arrays, and deep nesting.
func flattenPaths(obj map[string]interface{}, prefix string, paths *[]string, values map[string]string) {
	for k, v := range obj {
		if strings.HasPrefix(k, "_") {
			continue
		}
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]interface{}:
			if strings.Count(path, ".") < 3 {
				flattenPaths(val, path, paths, values)
			}
		case string:
			if val != "" && len(val) < 500 {
				*paths = append(*paths, path)
				values[path] = val
			}
		case float64:
			*paths = append(*paths, path)
			values[path] = fmt.Sprintf("%g", val)
		case bool:
			*paths = append(*paths, path)
			values[path] = fmt.Sprintf("%v", val)
		}
	}
	sort.Strings(*paths)
}

// renderAlertBuilder renders the template field picker with collapsible groups.
func (m detailModel) renderAlertBuilder() string {
	var b strings.Builder
	b.WriteString("  " + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("Message Template Builder") + "\n")
	b.WriteString("  " + subtextStyle.Render("Select fields to include in the message") + "\n\n")

	items := m.alertVisibleItems()

	// Scrolling window: show ~15 items centered on cursor
	visible := 15
	start := m.alertCursor - visible/2
	if start < 0 {
		start = 0
	}
	end := start + visible
	if end > len(items) {
		end = len(items)
		start = end - visible
		if start < 0 {
			start = 0
		}
	}

	// Find max display name width for alignment (only leaf fields)
	maxField := 0
	for _, item := range items {
		if !item.isHeader {
			if w := len(item.displayName); w > maxField {
				maxField = w
			}
		}
	}
	if maxField < 12 {
		maxField = 12
	}

	if start > 0 {
		b.WriteString("  " + mutedStyle.Render(fmt.Sprintf("  ... %d more above", start)) + "\n")
	} else {
		b.WriteString("\n")
	}

	hdrStyle := lipgloss.NewStyle().Foreground(colorGold).Bold(true)

	for i := start; i < end; i++ {
		item := items[i]
		cursor := "  "
		if i == m.alertCursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("> ")
		}

		depthIndent := strings.Repeat("   ", item.depth)

		if item.isHeader {
			arrow := "▸"
			extra := mutedStyle.Render(fmt.Sprintf("  %d fields", item.childCount))
			if m.alertExpanded[item.groupFullKey] {
				arrow = "▾"
				extra = ""
			}
			hs := hdrStyle
			if i == m.alertCursor {
				hs = hs.Underline(true)
			}
			b.WriteString("  " + cursor + depthIndent + arrow + " " + hs.Render(item.groupKey) + extra + "\n")
		} else {
			check := "[ ] "
			if m.alertChecked[item.path] {
				check = lipgloss.NewStyle().Foreground(colorAccent).Render("[x] ")
			}
			style := lipgloss.NewStyle().Foreground(colorText)
			if i == m.alertCursor {
				style = style.Bold(true)
			}
			padded := fmt.Sprintf("%-*s", maxField, item.displayName)
			field := style.Render(padded)
			preview := ""
			if item.value != "" {
				val := item.value
				if len(val) > 40 {
					val = val[:37] + "..."
				}
				preview = "  " + mutedStyle.Render(val)
			}
			b.WriteString("  " + cursor + depthIndent + check + field + preview + "\n")
		}
	}

	if end < len(items) {
		b.WriteString("  " + mutedStyle.Render(fmt.Sprintf("  ... %d more below", len(items)-end)) + "\n")
	} else {
		b.WriteString("\n")
	}

	// Preview with labels
	selected := collectCheckedFields(m.alertGroups, m.alertRootFields, m.alertChecked)
	if len(selected) > 0 {
		b.WriteString("\n  " + mutedStyle.Render("Preview:") + "\n")
		for _, s := range selected {
			b.WriteString("  " + subtextStyle.Render(s) + "\n")
		}
	}

	b.WriteString("\n  " + mutedStyle.Render("space toggle/expand  enter save  esc cancel"))
	return b.String()
}

// detailEditorDoneMsg is sent when the editor closes so we can reload the config.
type detailEditorDoneMsg struct {
	err error
}

// resolveEditor returns the best available editor.
func resolveEditor() string {
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	if e := os.Getenv("VISUAL"); e != "" {
		return e
	}
	// Fallback chain: vi is POSIX-mandated
	return "vi"
}

func (m detailModel) openEditor() tea.Cmd {
	editor := resolveEditor()
	path := config.PylonPath(m.name)
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return detailEditorDoneMsg{err: err}
	})
}

func (m detailModel) footerBindings() []keyBinding {
	var bindings []keyBinding

	// Pylon actions
	if m.pylon != nil && m.pylon.Trigger.Type == "webhook" {
		bindings = append(bindings, keyBinding{"y", "copy url"})
	}
	if m.pylon != nil && m.pylon.Trigger.Type == "cron" {
		bindings = append(bindings, keyBinding{"f", "fire"})
	}
	bindings = append(bindings, keyBinding{"e", "edit"})
	bindings = append(bindings, keyBinding{"a", "message builder"})

	// View toggles
	bindings = append(bindings, separator)
	if m.pylon != nil && m.pylon.Agent != nil && m.pylon.Agent.Prompt != "" {
		if m.showFullPrompt {
			bindings = append(bindings, keyBinding{"p", "collapse prompt"})
		} else {
			bindings = append(bindings, keyBinding{"p", "expand prompt"})
		}
	}
	if m.showJobs {
		bindings = append(bindings, keyBinding{"t", "hide jobs"})
	} else {
		bindings = append(bindings, keyBinding{"t", "show jobs"})
	}

	// Job actions (when a job is selected)
	if m.showJobs {
		if j := m.selectedJob(); j != nil {
			bindings = append(bindings, separator)
			if isRunningStatus(j.Status) {
				bindings = append(bindings, keyBinding{"l", "logs"})
				bindings = append(bindings, keyBinding{"x", "kill"})
			} else {
				// Terminal statuses: failed, timeout, completed, dismissed
				bindings = append(bindings, keyBinding{"l", "logs"})
				bindings = append(bindings, keyBinding{"x", "dismiss"})
				if j.Status == "failed" || j.Status == "timeout" {
					bindings = append(bindings, keyBinding{"r", "retry"})
				}
			}
		}
	}
	return bindings
}
