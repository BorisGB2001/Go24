// Package tasks holds the task tracker domain: portal-style statuses and roles, and the PostgreSQL store.
package tasks

import "go24/internal/i18n"

type Status string

// Statuses mirror the portal (REAL_STATUS 2…6); their names live in the locale files (status.<code>).
const (
	StatusPending         Status = "pending"          // 2 — ждёт выполнения
	StatusInProgress      Status = "in_progress"      // 3 — выполняется
	StatusAwaitingControl Status = "awaiting_control" // 4 — ждёт контроля
	StatusCompleted       Status = "completed"        // 5 — завершена
	StatusDeferred        Status = "deferred"         // 6 — отложена
)

// Closed reports whether the task no longer needs work.
func (s Status) Closed() bool { return s == StatusCompleted }

// Action is a status-changing operation, named like the portal REST methods tasks.task.<action>.
// Button labels are locale keys action.<code>.
type Action string

const (
	ActionStart      Action = "start"
	ActionPause      Action = "pause"
	ActionComplete   Action = "complete"
	ActionApprove    Action = "approve"
	ActionDisapprove Action = "disapprove"
	ActionDefer      Action = "defer"
	ActionRenew      Action = "renew"
)

// actionOrder fixes the order of buttons on the task card.
var actionOrder = []Action{
	ActionStart, ActionPause, ActionComplete, ActionApprove, ActionDisapprove, ActionDefer, ActionRenew,
}

// Roles of the acting user in a task. A user can hold several at once.
type Roles struct {
	Creator     bool // постановщик
	Responsible bool // исполнитель
	Accomplice  bool // соисполнитель
	Auditor     bool // наблюдатель
	Manager     bool // owner/manager of the task's project
}

// Worker reports whether the user takes part in doing the task.
func (r Roles) Worker() bool { return r.Responsible || r.Accomplice || r.Creator }

// Transition returns the status after applying action, or an error if the action is not allowed.
// requiresControl is the task's "review the result after completion" flag.
func Transition(cur Status, a Action, r Roles, requiresControl bool) (Status, error) {
	deny := func() (Status, error) {
		return cur, i18n.E("err.action_not_allowed", i18n.Ref("action."+string(a)), i18n.Ref("status."+string(cur)))
	}
	switch a {
	case ActionStart:
		if !r.Worker() || (cur != StatusPending && cur != StatusDeferred) {
			return deny()
		}
		return StatusInProgress, nil
	case ActionPause:
		if !r.Worker() || cur != StatusInProgress {
			return deny()
		}
		return StatusPending, nil
	case ActionComplete:
		if !r.Worker() || (cur != StatusPending && cur != StatusInProgress) {
			return deny()
		}
		// The creator closes their own task directly; anyone else sends it to control when required.
		if requiresControl && !r.Creator {
			return StatusAwaitingControl, nil
		}
		return StatusCompleted, nil
	case ActionApprove:
		if !r.Creator || cur != StatusAwaitingControl {
			return deny()
		}
		return StatusCompleted, nil
	case ActionDisapprove:
		if !r.Creator || cur != StatusAwaitingControl {
			return deny()
		}
		return StatusPending, nil
	case ActionDefer:
		if !r.Worker() || (cur != StatusPending && cur != StatusInProgress) {
			return deny()
		}
		return StatusDeferred, nil
	case ActionRenew:
		if !(r.Creator || r.Responsible || r.Manager) || (cur != StatusCompleted && cur != StatusDeferred) {
			return deny()
		}
		return StatusPending, nil
	}
	return cur, i18n.E("err.unknown_action", string(a))
}

// AllowedActions lists the actions the user may apply now, in button order.
func AllowedActions(cur Status, r Roles, requiresControl bool) []Action {
	var out []Action
	for _, a := range actionOrder {
		if _, err := Transition(cur, a, r, requiresControl); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// Priority 0 low, 1 normal, 2 high —; names are locale keys priority.<n>.
type Priority int16
