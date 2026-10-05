package app

import (
	"fmt"
	"slices"
	"time"

	"github.com/Iliorn/tjek/todo"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Detail pane ───────────────────────────────────────────────────────────────

func (m model) updateDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch key.String() {
	case "q": // ctrl+c is handled globally in dispatch
		m.flushPendingWrites()
		m.closeWatcher()
		return m, tea.Quit
	case "?":
		m.mode = modeHelp
		return m, nil
	case "ctrl+k":
		return m, m.openPalette()
	case "u":
		return m, m.performUndo()
	case "n":
		if m.currentTodo() != nil {
			return m, m.openEditorForNotes()
		}
		return m, nil
	case "esc":
		if len(m.detailStack) > 0 {
			return m.popSubtaskDetail()
		}
		m.popFocus()

	case "tab":
		m.switchTab(m.boardCfg.nextTab(m.tab, 1))
		return m, nil
	case "shift+tab":
		m.switchTab(m.boardCfg.nextTab(m.tab, -1))
		return m, nil

	case "1", "2", "3", "4", "5", "6", "7":
		// The digits are advertised as global navigation, so they must leave
		// the detail pane too — without this they silently did nothing here.
		if t, ok := m.tabForNumberKey(key.String()); ok {
			m.switchTab(t)
		}
		return m, nil

	case "left", "right":
		dir := 1
		if key.String() == "left" {
			dir = -1
		}
		m.detailSectionJump(dir)

	case "backspace":
		// enter steps a stepped field forward, backspace back, as on a
		// Settings row.
		return m.detailStepValue(-1)

	case "up":
		m.detailCursorUp()
	case "down":
		m.detailCursorDown()

	case "enter":
		if m.detail.field == fieldSubtasks {
			return m.openSelectedSubtaskDetail()
		}
		return m.startEditing()

	case "d":
		if m.detail.field == fieldSubtasks {
			if t := m.currentTodo(); t != nil {
				if m.detail.subtaskCursor < m.subtaskCount(t.ID) {
					// Full snapshot: toggleSubtask cascades up through
					// ancestors and may spawn new recurrence tasks —
					// neither is knowable until after the call.
					m.pushUndo("toggle subtask")
					ids := m.toggleSubtask(t.ID, m.detail.subtaskCursor)
					if len(ids) > 0 {
						m.markModified(ids...)
					}
				}
			}
		}

	case "t":
		// Toggle the timer on the selected subtask. Mirrors the
		// top-level `t` handler: done + no running timer = no-op,
		// idle threshold opens the runaway prompt, otherwise
		// toggleTimer enforces single-task tracking.
		if m.detail.field == fieldSubtasks {
			if parent := m.currentTodo(); parent != nil {
				ids := m.subtaskIDs(parent.ID)
				if m.detail.subtaskCursor < len(ids) {
					sub := m.get(ids[m.detail.subtaskCursor])
					if sub == nil {
						return m, nil
					}
					if sub.Status == todo.Done && !m.timerRunning(sub) {
						return m, nil
					}
					if e := m.runningEntry(sub); e != nil && time.Since(e.StartedAt) > idleThreshold {
						m.openIdlePrompt(sub)
						return m, nil
					}
					undoIDs := []string{sub.ID}
					if !m.timerRunning(sub) {
						for otherID := range m.runningTimers {
							if otherID != sub.ID {
								undoIDs = append(undoIDs, otherID)
							}
						}
					}
					m.pushUndo("toggle timer", undoIDs...)
					m.toggleTimer(sub)
					m.markModified(undoIDs...) // the timer it stopped too
					if !m.timerTickOn && m.anyTimerRunning() {
						m.timerTickOn = true
						return m, timerTick()
					}
				}
			}
		}

	case "#":
		// Tags are commonly entered through quick-add syntax, so make the same
		// sigil a direct route to the tag picker from anywhere in details.
		return m.openTagSearch()

	case "@":
		// Mirror quick-add's project sigil without making the user navigate back
		// to the Project field first.
		return m.openProjectSearch()

	case "a":
		return m.detailAdd()

	case "r":
		if m.detail.field == fieldSubtasks {
			return m.startRenamingSelectedSubtask()
		}
		// Enter steps through the common rules; r writes any rule, and
		// writing one again starts its series over from the due date.
		if m.detail.field == fieldRecurrence {
			if t := m.currentTodo(); t != nil {
				m.mode = modeInput
				m.textInput.SetValue(recurInputValue(t.Recurrence))
				m.textInput.CursorEnd()
				m.textInput.Placeholder = tr("Repeat (weekly, mon,thu, 2w:fri, monthly/until:31-12-27, daily/10x)...")
				m.textInput.Focus()
				return m, textinput.Blink
			}
		}
		// Edit the selected time entry from the detail pane — reuses the same
		// startEditTimeEntry flow as the calendar timeline so parsing and undo
		// are identical across both surfaces.
		if m.detail.field == fieldTimeEntries {
			if t := m.currentTodo(); t != nil {
				idx := m.detail.timeEntryCursor
				if idx < len(t.TimeEntries) {
					e := &t.TimeEntries[idx]
					if e.IsRunning() {
						// Don't allow editing a still-running entry via the
						// detail pane — use the calendar or stop the timer first.
						m.flashInfo(tr("Stop the timer before editing a running entry"))
						return m, clearErrAfter()
					}
					return m, m.startEditTimeEntry(t.ID, e.ID)
				}
			}
		}

	case "x", "delete":
		return m.detailDelete()
	}
	return m, nil
}

// completedFieldVisible reports whether the detail pane shows the editable
// "Completed on" row: only a done task has a completion time to correct.
func completedFieldVisible(t *todo.Todo) bool {
	return t != nil && t.Status == todo.Done && !t.CompletedAt.IsZero()
}

// detailSectionJump moves the detail cursor to the next/previous section
// head.
func (m *model) detailSectionJump(dir int) {
	next := detailSectionOf(m.detail.field) + dir
	for next >= 0 && next < len(detailSections) && !m.detailSectionShown(next) {
		next += dir
	}
	if next < 0 || next >= len(detailSections) {
		return
	}
	m.detail.field = detailSections[next].first
	m.detail.tagCursor = 0
	m.detail.subtaskCursor = 0
	m.detail.depCursor = 0
	m.detail.timeEntryCursor = 0
	m.detail.commentCursor = 0
	m.detail.historyCursor = 0
	m.invalidateDetailCache()
	// The section opens at the top of the pane, the way a page would, rather
	// than wherever the least scrolling leaves it. The offset is the blank line
	// above its heading, which is as high as the scroll margin lets the cursor
	// sit; clampDetailScroll keeps it inside the document.
	m.detail.scroll = max(m.estimateDetailCursorLine()-detailScrollMargin, 0)
}

// detailSection is one stop of ←/→ in the detail pane, in document order, with
// the field the cursor lands on and the name the section bar gives it.
type detailSection struct {
	first detailField
	label string
}

var detailSections = []detailSection{
	{fieldStartDate, "Fields"},
	{fieldTags, "Tags"},
	{fieldSubtasks, "Subtasks"},
	{fieldDependencies, "Dependencies"},
	{fieldTimeEntries, "Time"},
	{fieldComments, "Comments"},
	{fieldHistory, "History"},
}

// detailSectionShown reports whether section i of detailSections is drawn:
// Tags goes with the Tags and Projects tabs when those are switched off.
func (m model) detailSectionShown(i int) bool {
	return m.boardCfg.groups || detailSections[i].first != fieldTags
}

// detailSectionOf is the index in detailSections of the section a field is
// in. The field enum is not in document order (Dependencies precedes
// Subtasks), so it is a switch rather than a comparison.
func detailSectionOf(f detailField) int {
	switch f {
	case fieldTags:
		return 1
	case fieldSubtasks:
		return 2
	case fieldDependencies:
		return 3
	case fieldTimeEntries:
		return 4
	case fieldComments:
		return 5
	case fieldHistory:
		return 6
	}
	return 0
}

// detailCursorUp/Down walk one continuous field chain over the whole detail
// column: dates → priority/size/project/notes → tags → subtasks →
// dependencies → time entries → comments, wrapping at the ends.
func (m *model) detailCursorUp() {
	m.invalidateDetailCache()
	t := m.currentTodo()
	switch m.detail.field {
	case fieldStartDate:
		// Wrap to the bottom of the column: the oldest history row.
		m.detail.field = fieldHistory
		m.detail.historyCursor = 0
		if t != nil {
			m.detail.historyCursor = max(len(historyRows(t.History))-1, 0)
		}
	case fieldDueDate:
		m.detail.field = fieldStartDate
	case fieldCompleted:
		m.detail.field = fieldDueDate
	case fieldRecurrence:
		m.detail.field = fieldDueDate
		if completedFieldVisible(t) {
			m.detail.field = fieldCompleted
		}
	case fieldPriority:
		m.detail.field = fieldRecurrence
	case fieldSize:
		m.detail.field = fieldPriority
	case fieldStage:
		m.detail.field = fieldSize
	case fieldProject:
		m.detail.field = fieldSize
		if m.boardCfg.stageFieldVisible(t) {
			m.detail.field = fieldStage
		}
	case fieldNotes:
		m.detail.field = fieldProject
		if !m.boardCfg.groups {
			m.detail.field = fieldSize
			if m.boardCfg.stageFieldVisible(t) {
				m.detail.field = fieldStage
			}
		}
	case fieldTags:
		if m.detail.tagCursor > 0 {
			m.detail.tagCursor--
		} else {
			m.detail.field = fieldNotes
		}
	case fieldSubtasks:
		if m.detail.subtaskCursor > 0 {
			m.detail.subtaskCursor--
		} else if !m.boardCfg.groups {
			m.detail.field = fieldNotes
		} else {
			m.detail.field = fieldTags
			m.detail.tagCursor = 0
			if t != nil && len(t.Tags) > 0 {
				m.detail.tagCursor = len(t.Tags) - 1
			}
		}
	case fieldDependencies:
		if m.detail.depCursor > 0 {
			m.detail.depCursor--
		} else {
			m.detail.field = fieldSubtasks
			if t != nil && m.subtaskCount(t.ID) > 0 {
				m.detail.subtaskCursor = m.subtaskCount(t.ID) - 1
			}
		}
	case fieldTimeEntries:
		if m.detail.timeEntryCursor > 0 {
			m.detail.timeEntryCursor--
		} else {
			m.detail.field = fieldDependencies
			if t != nil {
				if n := m.detailDepTotal(t); n > 0 {
					m.detail.depCursor = n - 1
				}
			}
		}
	case fieldComments:
		if m.detail.commentCursor > 0 {
			m.detail.commentCursor--
		} else {
			m.detail.field = fieldTimeEntries
			m.detail.timeEntryCursor = 0
			if t != nil && len(t.TimeEntries) > 0 {
				m.detail.timeEntryCursor = len(t.TimeEntries) - 1
			}
		}
	case fieldHistory:
		if m.detail.historyCursor > 0 {
			m.detail.historyCursor--
		} else {
			m.detail.field = fieldComments
			m.detail.commentCursor = 0
			if t != nil && len(t.Comments) > 0 {
				m.detail.commentCursor = len(t.Comments) - 1
			}
		}
	}
}

// fieldAfterSize is the row below Size (or Stage) once the Stage row is
// accounted for: Project, or Description when Tags and Projects are off.
func (m model) fieldAfterSize() detailField {
	if m.boardCfg.groups {
		return fieldProject
	}
	return fieldNotes
}

func (m *model) detailCursorDown() {
	m.invalidateDetailCache()
	t := m.currentTodo()
	switch m.detail.field {
	case fieldStartDate:
		m.detail.field = fieldDueDate
	case fieldDueDate:
		m.detail.field = fieldRecurrence
		if completedFieldVisible(t) {
			m.detail.field = fieldCompleted
		}
	case fieldCompleted:
		m.detail.field = fieldRecurrence
	case fieldRecurrence:
		m.detail.field = fieldPriority
	case fieldPriority:
		m.detail.field = fieldSize
	case fieldSize:
		m.detail.field = m.fieldAfterSize()
		if m.boardCfg.stageFieldVisible(t) {
			m.detail.field = fieldStage
		}
	case fieldStage:
		m.detail.field = m.fieldAfterSize()
	case fieldProject:
		m.detail.field = fieldNotes
	case fieldNotes:
		m.detail.field = fieldTags
		m.detail.tagCursor = 0
		if !m.boardCfg.groups {
			m.detail.field = fieldSubtasks
			m.detail.subtaskCursor = 0
		}
	case fieldTags:
		if t != nil && m.detail.tagCursor < len(t.Tags)-1 {
			m.detail.tagCursor++
		} else {
			m.detail.field = fieldSubtasks
			m.detail.subtaskCursor = 0
		}
	case fieldSubtasks:
		if t != nil && m.detail.subtaskCursor < m.subtaskCount(t.ID)-1 {
			m.detail.subtaskCursor++
		} else {
			m.detail.field = fieldDependencies
			m.detail.depCursor = 0
		}
	case fieldDependencies:
		if t != nil && m.detail.depCursor < m.detailDepTotal(t)-1 {
			m.detail.depCursor++
		} else {
			m.detail.field = fieldTimeEntries
			m.detail.timeEntryCursor = 0
		}
	case fieldTimeEntries:
		if t != nil && m.detail.timeEntryCursor < len(t.TimeEntries)-1 {
			m.detail.timeEntryCursor++
		} else {
			m.detail.field = fieldComments
			m.detail.commentCursor = 0
		}
	case fieldComments:
		if t != nil && m.detail.commentCursor < len(t.Comments)-1 {
			m.detail.commentCursor++
		} else {
			m.detail.field = fieldHistory
			m.detail.historyCursor = 0
		}
	case fieldHistory:
		if t != nil && m.detail.historyCursor < len(historyRows(t.History))-1 {
			m.detail.historyCursor++
		} else {
			// Wrap to the top of the column.
			m.detail.field = fieldStartDate
			m.detail.historyCursor = 0
		}
	}
}

func (m model) detailAdd() (tea.Model, tea.Cmd) {
	if m.detail.field == fieldComments {
		m.mode = modeInput
		m.textInput.SetValue("")
		m.textInput.Placeholder = tr("Add comment...")
		m.textInput.Focus()
		return m, textinput.Blink
	}
	switch m.detail.field {
	case fieldDependencies:
		m.mode = modeSearchDep
		m.depSearchInput.SetValue("")
		m.depSearch = searchState{}
		m.depSearchInput.Focus()
		return m, textinput.Blink
	case fieldTags:
		return m.openTagSearch()
	case fieldProject:
		return m.openProjectSearch()
	case fieldTimeEntries:
		if t := m.currentTodo(); t != nil {
			return m, m.startAddTimeEntry(t.ID)
		}
	case fieldSubtasks:
		m.mode = modeAddSubtask
		m.textInput.SetValue("")
		m.textInput.Placeholder = tr("Add subtask...")
		m.textInput.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

func (m model) openTagSearch() (tea.Model, tea.Cmd) {
	m.mode = modeSearchTag
	m.tagSearchInput.SetValue("")
	m.tagSearch = searchState{}
	m.tagSearchInput.Focus()
	return m, textinput.Blink
}

func (m model) openProjectSearch() (tea.Model, tea.Cmd) {
	m.mode = modeSearchProject
	m.projSearchInput.SetValue("")
	m.projSearch = searchState{}
	m.projSearchInput.Focus()
	return m, textinput.Blink
}

func (m model) detailDelete() (tea.Model, tea.Cmd) {
	t := m.currentTodo()
	if t == nil {
		return m, nil
	}
	if m.detail.field == fieldComments {
		if len(t.Comments) > 0 {
			m.mode = modeConfirm
			m.confirmOnYes = (*model).confirmDeleteComment
			m.pendingComment = m.detail.commentCursor
			m.confirmMsg = tr("Delete this comment? (y/n)")
		}
		return m, nil
	}
	switch m.detail.field {
	case fieldProject:
		if t.Project != "" {
			m.mode = modeConfirm
			m.confirmOnYes = (*model).confirmDeleteProject
			m.confirmMsg = fmt.Sprintf(tr("Remove project '%s' from this task? (y/n)"), t.Project)
		}
	case fieldNotes:
		if t.Notes != "" {
			m.pushUndo("clear description", t.ID)
			t.SetNotes("")
			m.markModified(t.ID)
		}
	case fieldTags:
		if len(t.Tags) > 0 && m.detail.tagCursor < len(t.Tags) {
			m.mode = modeConfirm
			m.confirmOnYes = (*model).confirmDeleteTag
			m.pendingTag = m.detail.tagCursor
			m.confirmMsg = fmt.Sprintf(tr("Remove tag '#%s' from this task? (y/n)"), t.Tags[m.detail.tagCursor])
		}
	case fieldDependencies:
		if m.detail.depCursor >= len(t.Dependencies) {
			// ↥ row: the edge lives on the other task.
			m.flashInfo(tr("Inbound dependency: remove it from the other task"))
		} else if len(t.Dependencies) > 0 {
			m.mode = modeConfirm
			m.confirmOnYes = (*model).confirmDeleteDep
			m.pendingDep = m.detail.depCursor
			m.confirmMsg = tr("Remove this dependency? (y/n)")
		}
	case fieldSubtasks:
		if m.subtaskCount(t.ID) > 0 && m.detail.subtaskCursor < m.subtaskCount(t.ID) {
			subID := m.subtaskIDs(t.ID)[m.detail.subtaskCursor]
			subTitle := subID
			if sub := m.findTodoByID(subID); sub != nil {
				subTitle = sub.Title
			}
			m.mode = modeConfirm
			m.confirmOnYes = (*model).confirmDeleteSubtask
			m.pendingSubtask = m.detail.subtaskCursor
			m.confirmMsg = fmt.Sprintf(tr("Delete subtask '%s'? (y/n)"), truncate(subTitle, 40))
		}
	case fieldTimeEntries:
		idx := m.detail.timeEntryCursor
		if idx < len(t.TimeEntries) {
			e := &t.TimeEntries[idx]
			if e.IsRunning() {
				// Guard: deleting a running entry is the same footgun as on
				// the calendar. Require the user to stop it first.
				m.flashInfo(tr("Stop the timer before deleting a running entry"))
				return m, clearErrAfter()
			}
			m.mode = modeConfirm
			m.confirmOnYes = (*model).confirmDeleteTimeEntryFromDetail
			m.pendingEntryTaskID = t.ID
			m.pendingEntryID = e.ID
			m.confirmMsg = fmt.Sprintf(tr("Delete %s entry? (y/n)"), formatDuration(e.Duration()))
		}
	}
	return m, nil
}

func (m model) startEditing() (tea.Model, tea.Cmd) {
	t := m.currentTodo()
	if t == nil {
		return m, nil
	}

	if m.detail.field == fieldComments {
		if len(t.Comments) > 0 {
			m.mode = modeEditComment
			m.pendingComment = m.detail.commentCursor
			m.textInput.SetValue(t.Comments[m.detail.commentCursor].Text)
			m.textInput.Placeholder = tr("Edit comment...")
			m.textInput.Focus()
		}
		return m, textinput.Blink
	}

	switch m.detail.field {
	case fieldStartDate:
		m.mode = modeInput
		if !t.StartDate.IsZero() {
			m.textInput.SetValue(t.StartDate.Format("02-01-06"))
		} else {
			m.textInput.SetValue("")
		}
		m.textInput.Placeholder = tr("Start date (dd-mm-yy, 'today', 'next week', '+3d')...")
		m.textInput.Focus()
	case fieldDueDate:
		m.mode = modeInput
		if !t.DueDate.IsZero() {
			m.textInput.SetValue(t.DueDate.Format("02-01-06"))
		} else {
			m.textInput.SetValue("")
		}
		m.textInput.Placeholder = tr("Due date (dd-mm-yy, 'today', 'next week', '+3d')...")
		m.textInput.Focus()
	case fieldCompleted:
		if !completedFieldVisible(t) {
			return m, nil
		}
		m.mode = modeInput
		m.textInput.SetValue(t.CompletedAt.Format("02-01-06 15:04"))
		m.textInput.Placeholder = tr("Completed (dd-mm-yy hh:mm, 'today', 'yesterday')...")
		m.textInput.Focus()
	case fieldRecurrence, fieldPriority, fieldSize, fieldStage:
		return m.detailStepValue(+1)
	case fieldProject:
		m.mode = modeSearchProject
		m.projSearchInput.SetValue(t.Project)
		m.projSearch = searchState{query: t.Project}
		m.projSearchInput.Focus()
		return m, textinput.Blink
	case fieldNotes:
		return m, m.openEditorForNotes()
	case fieldTags:
		m.mode = modeSearchTag
		m.tagSearchInput.SetValue("")
		m.tagSearch = searchState{}
		m.tagSearchInput.Focus()
		return m, textinput.Blink
	case fieldDependencies:
		if total := m.detailDepTotal(t); total > 0 && m.detail.depCursor < total {
			depID := ""
			if m.detail.depCursor < len(t.Dependencies) {
				depID = t.Dependencies[m.detail.depCursor]
			} else {
				// ↥ row: jump to the task waiting on this one.
				depID = dependentsOf(m.allTodos(), t.ID)[m.detail.depCursor-len(t.Dependencies)].ID
			}
			for i, candidate := range m.cache.active {
				if candidate.ID == depID {
					// Leaving the detail pane without esc — drop its entry
					// before m.tab changes so it's removed from the right tab.
					m.dropFocus(stateDetailPane)
					m.pane = paneList
					m.detailTaskID = ""
					m.detailStack = nil
					m.cursor = i
					m.tab = tabTasks
					m.invalidateDetailCache()
					return m, nil
				}
			}
			// Not in the active list: it's done, or filtered out of the current
			// view. Enter can't scroll to it, so explain why instead of no-oping.
			switch dep := m.findTodoByID(depID); {
			case dep == nil:
				m.flashInfo(tr("Dependency no longer exists"))
			case dep.Status == todo.Done:
				m.flashInfo(fmt.Sprintf(tr("Dependency '%s' is done"), truncate(dep.Title, 40)))
			default:
				m.flashInfo(fmt.Sprintf(tr("Dependency '%s' is hidden by the current filter"), truncate(dep.Title, 40)))
			}
		}
		return m, nil
	case fieldSubtasks:
		// Enter is handled by updateDetail so it can open the selected
		// subtask's full detail pane. Renaming is deliberately bound to 'r'.
	}
	return m, textinput.Blink
}

// openSelectedSubtaskDetail changes the detail pane's explicit target rather
// than moving the list cursor. This also works for deeply nested subtasks,
// which do not necessarily have their own visible Tasks-list row.
func (m model) openSelectedSubtaskDetail() (tea.Model, tea.Cmd) {
	parent := m.currentTodo()
	if parent == nil {
		return m, nil
	}
	ids := m.subtaskIDs(parent.ID)
	if m.detail.subtaskCursor >= len(ids) {
		return m, nil
	}
	sub := m.findTodoByID(ids[m.detail.subtaskCursor])
	if sub == nil {
		return m, nil
	}
	// Remember the task we drilled from so esc can walk back up the chain one
	// level at a time instead of collapsing straight to the list.
	m.detailStack = append(m.detailStack, m.detailTaskID)
	m.detailTaskID = sub.ID
	m.detail = detailState{field: fieldStartDate}
	m.invalidateDetailCache()
	return m, nil
}

// popSubtaskDetail backs out of a drilled-in subtask detail to its parent's
// detail pane, one level per esc. It lands on the Subtasks section — where the
// user drilled from — and keeps the pane open; only the top-level detail (an
// empty stack) exits to the list, which the caller handles via popFocus.
func (m model) popSubtaskDetail() (tea.Model, tea.Cmd) {
	n := len(m.detailStack)
	m.detailTaskID = m.detailStack[n-1]
	m.detailStack = m.detailStack[:n-1]
	m.detail = detailState{field: fieldSubtasks}
	m.invalidateDetailCache()
	return m, nil
}

func (m model) startRenamingSelectedSubtask() (tea.Model, tea.Cmd) {
	parent := m.currentTodo()
	if parent == nil {
		return m, nil
	}
	ids := m.subtaskIDs(parent.ID)
	if m.detail.subtaskCursor >= len(ids) {
		return m, nil
	}
	sub := m.findTodoByID(ids[m.detail.subtaskCursor])
	if sub == nil {
		return m, nil
	}
	m.mode = modeEditSubtask
	m.pendingSubtask = m.detail.subtaskCursor
	m.textInput.SetValue(sub.Title)
	m.textInput.Placeholder = tr("Edit subtask title...")
	m.textInput.Focus()
	return m, textinput.Blink
}

// detailDepTotal is the number of selectable rows in the merged dependency
// list: outbound ↧ edges first, then inbound ↥ dependents.
func (m model) detailDepTotal(t *todo.Todo) int {
	return len(t.Dependencies) + len(dependentsOf(m.allTodos(), t.ID))
}

// recurrenceSteps, sizeSteps and prioritySteps are the orders enter walks the
// stepped detail fields in, backspace the other way. Size starts at Medium so
// the first press moves toward Small, the direction most often wanted.
var (
	recurrenceSteps = []string{"", "daily", "weekdays", "weekly", "monthly", "yearly"}
	sizeSteps       = []todo.Size{todo.SizeMedium, todo.SizeSmall, todo.SizeLarge}
	prioritySteps   = []todo.Priority{todo.PriorityLow, todo.PriorityMedium, todo.PriorityHigh}
)

// stepIn is the value dir steps away from cur in steps, wrapping at the ends;
// a value not in steps goes to the first.
func stepIn[T comparable](steps []T, cur T, dir int) T {
	i := slices.Index(steps, cur)
	if i < 0 {
		return steps[0]
	}
	return steps[(i+dir+len(steps))%len(steps)]
}

// detailStepValue steps the selected detail field's value by dir, for the
// fields that pick from a short list (recurrence, priority, size, stage);
// elsewhere it does nothing. A custom "every:Nd" rule steps to none; quick-add
// (r:Nd) is where one is written.
func (m model) detailStepValue(dir int) (tea.Model, tea.Cmd) {
	t := m.currentTodo()
	if t == nil {
		return m, nil
	}
	switch m.detail.field {
	case fieldRecurrence:
		m.pushUndo("cycle recurrence", t.ID)
		if r := stepIn(recurrenceSteps, t.Recurrence, dir); r == "" {
			t.ClearRecurrence()
		} else {
			t.SetRecurrence(r)
		}
		m.markModified(t.ID)
	case fieldPriority:
		if m.cyclePriority(t, dir) {
			// Without this the keypress looks dead: the cycle ran, the cap put
			// the value straight back, and nothing on screen moved.
			m.flashInfo(tr("A subtask can't outrank its parent"))
			return m, clearErrAfter()
		}
	case fieldSize:
		m.pushUndo("cycle size", t.ID)
		t.SetSize(stepIn(sizeSteps, t.Size, dir))
		m.markModified(t.ID)
	case fieldStage:
		if m.boardCfg.stageFieldVisible(t) {
			m.pushUndo("move stage", t.ID)
			t.SetStage(m.boardCfg.cycleStage(t.Stage, dir))
			m.markModified(t.ID)
		}
	}
	return m, nil
}
