package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// REST compatible with the portal, workgroups (GO24DEV-940): sonet_group.create, update, delete, get, setowner;
// sonet_group.user.get, add, delete, invite, request, groups; sonet_group.feature.access; socialnetwork.api.workgroup.get
// and list. A group is a go24 group or project by its number (migration 662), a subject by its number (migration 880).
// Its type is VISIBLE and OPENED — an open group has both, a closed one VISIBLE alone, a secret one neither;
// CLOSED is the archive, PROJECT a project or a scrum. The roles: A — the owner, E — a moderator, K — a member, Z — one
// invited or asking to join. The rights are those of the group's pages.

func init() {
	for name, m := range map[string]portalMethod{
		"sonet_group.create":               {Scope: "projects:write", Run: (*Server).portalGroupCreate},
		"sonet_group.update":               {Scope: "projects:write", Run: (*Server).portalGroupUpdate},
		"sonet_group.delete":               {Scope: "projects:write", Run: (*Server).portalGroupDelete},
		"sonet_group.get":                  {Scope: "projects:read", Run: (*Server).portalGroupList},
		"sonet_group.setowner":             {Scope: "projects:write", Run: (*Server).portalGroupOwner},
		"sonet_group.user.get":             {Scope: "projects:read", Run: (*Server).portalGroupUsers},
		"sonet_group.user.add":             {Scope: "projects:write", Run: (*Server).portalGroupUserAdd},
		"sonet_group.user.delete":          {Scope: "projects:write", Run: (*Server).portalGroupUserDelete},
		"sonet_group.user.invite":          {Scope: "projects:write", Run: (*Server).portalGroupUserInvite},
		"sonet_group.user.request":         {Scope: "projects:write", Run: (*Server).portalGroupRequest},
		"sonet_group.user.groups":          {Scope: "projects:read", Run: (*Server).portalGroupsOfUser},
		"sonet_group.feature.access":       {Scope: "projects:read", Run: (*Server).portalGroupFeature},
		"socialnetwork.api.workgroup.get":  {Scope: "projects:read", Run: (*Server).portalWorkgroupGet},
		"socialnetwork.api.workgroup.list": {Scope: "projects:read", Run: (*Server).portalWorkgroupList},
	} {
		portalMethods[strings.ToLower(name)] = m
	}
}

// portalGroupRoles: a role in a go24 group → the portal's.
var portalGroupRoles = map[string]string{"owner": "A", "manager": "E", "member": "K", "viewer": "K"}

// portalInvitePerms: «кто может приглашать» of go24 → INITIATE_PERMS.
var portalInvitePerms = map[string]string{"owner": "A", "managers": "E", "members": "K"}

// portalGroupNum is the group of GROUP_ID (or of the parameter given) as the caller sees it. One the caller is not in is
// found too when it is open or closed and outsider is true (asking to join, the features of an open group).
func (s *Server) portalGroupNum(c portalCall, v any, outsider bool) (tasks.Project, error) {
	ctx := c.r.Context()
	n := strings.TrimSpace(portalStr(v))
	if n == "" || n == "0" {
		return tasks.Project{}, s.portalErr(c.r, http.StatusBadRequest, "GROUP_ID_EMPTY", "err.portal_field", "GROUP_ID")
	}
	var key string
	err := s.DB.QueryRow(ctx, `SELECT key FROM projects WHERE num::text = $1`, n).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return tasks.Project{}, i18n.E("err.not_found")
	}
	if err != nil {
		return tasks.Project{}, err
	}
	p, err := s.Tasks.ProjectByKey(ctx, c.p.UserID, key)
	if errors.Is(err, tasks.ErrForbidden) && outsider {
		if _, gerr := s.Tasks.GroupForOutsider(ctx, p, c.p.UserID); gerr == nil {
			return p, nil
		}
	}
	return p, err
}

// portalGroup is the group of GROUP_ID.
func (s *Server) portalGroup(c portalCall) (tasks.Project, error) {
	return s.portalGroupNum(c, portalParam(c.in, "GROUP_ID"), false)
}

// portalGroupSQL reads the groups the caller sees ($1; $2 — the admin mode): those they are in, the open and closed
// ones of the company (not to a guest); the collabs only to their members.
var portalGroupSQL = `SELECT p.id::text, p.key, p.num, p.name, p.description, p.created_at, p.updated_at, p.privacy, p.archived,
		p.kind, coalesce(gs.num, 0), coalesce(gs.name, ''), coalesce(u.num, 0), array_to_string(p.keywords, ','), p.date_start,
		p.date_finish, p.invite_perm, p.photo_key, coalesce(pm.role, ''), coalesce(sm.num, 0),
		(SELECT count(*) FROM project_members m WHERE m.project_id = p.id)::int,
		(SELECT count(*) FROM project_members m WHERE m.project_id = p.id AND m.role IN ('owner', 'manager'))::int
	FROM projects p LEFT JOIN group_subjects gs ON gs.id = p.subject_id LEFT JOIN users u ON u.id = p.owner_id
		LEFT JOIN users sm ON sm.id = p.scrum_master_id
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $1
	WHERE (pm.user_id IS NOT NULL OR $2 OR (p.kind <> 'collab' AND p.privacy <> 'secret' AND NOT ` + tasks.GuestSQL("$1") + `))`

// portalGroupRow is a group as sonet_group.get gives it, with what the other methods need besides.
type portalGroupRow struct {
	ID, Key, Role string
	Num           int64
	Members       int
	Row           map[string]any
}

func (s *Server) portalGroupRows(c portalCall, where string, args ...any) ([]portalGroupRow, error) {
	ctx := c.r.Context()
	rows, err := s.DB.Query(ctx, portalGroupSQL+where+` ORDER BY p.num`, append([]any{c.p.UserID, tasks.IsAdmin(ctx)}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []portalGroupRow
	for rows.Next() {
		var g portalGroupRow
		var name, desc, privacy, kind, subject, keywords, perm, photo string
		var subjectNum, owner, master int64
		var created, updated time.Time
		var start, finish *time.Time
		var archived bool
		var moderators int
		if err := rows.Scan(&g.ID, &g.Key, &g.Num, &name, &desc, &created, &updated, &privacy, &archived, &kind, &subjectNum,
			&subject, &owner, &keywords, &start, &finish, &perm, &photo, &g.Role, &master, &g.Members, &moderators); err != nil {
			return nil, err
		}
		day := func(t *time.Time) any {
			if t == nil {
				return nil
			}
			return t.Format("2006-01-02")
		}
		num := func(n int64) string { return strconv.FormatInt(n, 10) }
		g.Row = map[string]any{"ID": num(g.Num), "SITE_ID": "s1", "NAME": name, "DESCRIPTION": desc,
			"DATE_CREATE": created.In(time.Local).Format(time.RFC3339), "DATE_UPDATE": updated.In(time.Local).Format(time.RFC3339),
			"DATE_ACTIVITY": updated.In(time.Local).Format(time.RFC3339), "ACTIVE": "Y", "VISIBLE": portalYN(privacy != "secret" && kind != "collab"),
			"OPENED": portalYN(privacy == "open" && kind != "collab"), "CLOSED": portalYN(archived), "SUBJECT_ID": num(subjectNum),
			"SUBJECT_NAME": subject, "OWNER_ID": num(owner), "KEYWORDS": keywords, "NUMBER_OF_MEMBERS": strconv.Itoa(g.Members),
			"NUMBER_OF_MODERATORS": strconv.Itoa(moderators), "INITIATE_PERMS": portalInvitePerms[perm],
			"PROJECT": portalYN(kind == "project" || kind == "scrum"), "PROJECT_DATE_START": day(start), "PROJECT_DATE_FINISH": day(finish),
			"IS_EXTRANET": portalYN(kind == "collab"), "TYPE": kind, "SCRUM_MASTER_ID": num(master), "IMAGE": ""}
		if photo != "" {
			g.Row["IMAGE"] = strings.TrimRight(s.BaseURL, "/") + tasks.Project{Key: g.Key, PhotoKey: photo}.PhotoURL()
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// portalGroupRowOf is the row of one group.
func (s *Server) portalGroupRowOf(c portalCall, id string) (portalGroupRow, error) {
	list, err := s.portalGroupRows(c, ` AND p.id = $3`, id)
	if err != nil {
		return portalGroupRow{}, err
	}
	if len(list) == 0 {
		return portalGroupRow{}, i18n.E("err.not_found")
	}
	return list[0], nil
}

// portalGroupKey is the key of a new group, which the portal does not have: SG and the next free number.
func (s *Server) portalGroupKey(ctx context.Context) (string, error) {
	var n int64
	if err := s.DB.QueryRow(ctx, `SELECT coalesce(max(num), 0) + 1 FROM projects`).Scan(&n); err != nil {
		return "", err
	}
	for ; ; n++ {
		key := "SG" + strconv.FormatInt(n, 10)
		var taken bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE key = $1)`, key).Scan(&taken); err != nil || !taken {
			return key, err
		}
	}
}

// portalGroupPrivacy is the go24 type of a group of VISIBLE and OPENED (each nil — not given), from the current one; an
// open group is seen by all.
func portalGroupPrivacy(now string, visible, opened *bool) string {
	vis, open := now != "secret", now == "open"
	if opened != nil {
		open = *opened
		vis = vis || open
	}
	if visible != nil {
		vis = *visible
	}
	switch {
	case !vis:
		return "secret"
	case open:
		return "open"
	}
	return "closed"
}

// portalGroupImage is IMAGE of a group: [name, base64] (or the base64 alone, a data: URL too); nil — not given.
func portalGroupImage(in map[string]any) ([]byte, bool, error) {
	v, ok := portalParamOK(in, "IMAGE")
	if !ok {
		return nil, false, nil
	}
	list := portalList(v)
	if len(list) == 0 {
		return nil, false, nil
	}
	data := list[len(list)-1]
	if _, b64, ok := strings.Cut(data, ";base64,"); ok {
		data = b64
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	if err != nil || len(raw) == 0 {
		return nil, true, i18n.E("err.photo")
	}
	return raw, true, nil
}

// portalGroupFields applies the fields of sonet_group.create or update to the group: only those given.
func (s *Server) portalGroupFields(c portalCall, p tasks.Project) error {
	ctx := c.r.Context()
	in := c.in
	name, desc, start, finish := p.Name, p.Description, p.DateStart, p.DateFinish
	info := false
	if v, ok := portalParamOK(in, "NAME"); ok && c.method == "sonet_group.update" {
		name, info = portalStr(v), true
	}
	if v, ok := portalParamOK(in, "DESCRIPTION"); ok && c.method == "sonet_group.update" {
		desc, info = portalStr(v), true
	}
	for _, f := range []struct {
		k  string
		to **time.Time
	}{{"PROJECT_DATE_START", &start}, {"PROJECT_DATE_FINISH", &finish}} {
		if v, ok := portalParamOK(in, f.k); ok {
			t, err := portalDate(portalStr(v))
			if err != nil {
				return s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", f.k)
			}
			*f.to, info = t, true
		}
	}
	if info {
		if err := s.Tasks.SaveProjectInfo(ctx, p, name, desc, start, finish); err != nil {
			return err
		}
	}
	if v := portalFlagOf(in, "PROJECT"); v != nil && *v != (p.Kind == "project" || p.Kind == "scrum") && !p.Collab() {
		kind := "group"
		if *v {
			kind = "project"
		}
		if err := s.Tasks.SetProjectKind(ctx, p, kind); err != nil {
			return err
		}
	}
	visible, opened := portalFlagOf(in, "VISIBLE"), portalFlagOf(in, "OPENED")
	if privacy := portalGroupPrivacy(p.Privacy, visible, opened); (visible != nil || opened != nil) && privacy != p.Privacy && !p.Collab() {
		if err := s.Tasks.SetPrivacy(ctx, p, privacy); err != nil {
			return err
		}
	}
	if v, ok := portalParamOK(in, "INITIATE_PERMS"); ok {
		var perm string
		for k, x := range portalInvitePerms {
			if strings.EqualFold(x, strings.TrimSpace(portalStr(v))) {
				perm = k
			}
		}
		if perm == "" {
			return s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "INITIATE_PERMS")
		}
		if err := s.Tasks.SetInvitePerm(ctx, p, perm); err != nil {
			return err
		}
	}
	subject, keywords := p.SubjectID, p.Keywords
	_, hasSubject := portalParamOK(in, "SUBJECT_ID")
	kw, hasKeywords := portalParamOK(in, "KEYWORDS")
	if hasSubject {
		subject = ""
		if n := strings.TrimSpace(portalStr(portalParam(in, "SUBJECT_ID"))); n != "" && n != "0" {
			err := s.DB.QueryRow(ctx, `SELECT id::text FROM group_subjects WHERE num::text = $1`, n).Scan(&subject)
			if errors.Is(err, pgx.ErrNoRows) {
				return s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "SUBJECT_ID")
			}
			if err != nil {
				return err
			}
		}
	}
	if hasKeywords {
		keywords = tasks.ParseKeywords(strings.Join(portalList(kw), ","))
	}
	if (hasSubject || hasKeywords) && !p.Collab() {
		if err := s.Tasks.SetGroupSubject(ctx, p, subject, keywords); err != nil {
			return err
		}
	}
	if raw, ok, err := portalGroupImage(in); err != nil {
		return err
	} else if ok {
		if !p.CanManage() {
			return tasks.ErrForbidden
		}
		if err := s.storeProjectPhoto(ctx, c.p.UserID, p, bytes.NewReader(raw), int64(len(raw))); err != nil {
			return err
		}
	}
	if v := portalFlagOf(in, "CLOSED"); v != nil && *v != p.Archived {
		if err := s.Tasks.SetArchived(ctx, p, *v); err != nil {
			return err
		}
	}
	return nil
}

// portalGroupReload is the group again, with what the changes made of it.
func (s *Server) portalGroupReload(c portalCall, p tasks.Project) (tasks.Project, error) {
	return s.Tasks.ProjectByKey(c.r.Context(), c.p.UserID, p.Key)
}

// sonet_group.create: NAME, DESCRIPTION, VISIBLE, OPENED, CLOSED, KEYWORDS, INITIATE_PERMS, PROJECT,
// PROJECT_DATE_START, PROJECT_DATE_FINISH, SUBJECT_ID, IMAGE, OWNER_ID (an administrator's) → the group's number. A
// group made so is closed unless VISIBLE or OPENED say otherwise.
func (s *Server) portalGroupCreate(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	if tasks.IsGuestCtx(ctx) {
		return portalOut{}, tasks.ErrForbidden
	}
	key, err := s.portalGroupKey(ctx)
	if err != nil {
		return portalOut{}, err
	}
	key, err = s.Tasks.CreateProject(ctx, c.p.UserID, key, portalStr(portalParam(c.in, "NAME")), portalStr(portalParam(c.in, "DESCRIPTION")))
	if err != nil {
		return portalOut{}, err
	}
	s.Matrix.Wake() // the group's messenger room
	p, err := s.Tasks.ProjectByKey(ctx, c.p.UserID, key)
	if err != nil {
		return portalOut{}, err
	}
	fields := c
	if _, ok := portalParamOK(c.in, "VISIBLE"); !ok { //, a new group is seen in the list
		fields.in = maps.Clone(c.in)
		fields.in["VISIBLE"] = "Y"
	}
	if err := s.portalGroupFields(fields, p); err != nil {
		return portalOut{}, err
	}
	if owner, err := s.portalNum(ctx, "users", portalParam(c.in, "OWNER_ID")); err != nil {
		return portalOut{}, err
	} else if owner != "" && owner != c.p.UserID {
		if !s.apiAdmin(ctx, c.p.UserID) {
			return portalOut{}, tasks.ErrForbidden
		}
		if err := s.portalGroupSetOwner(c, p, owner); err != nil {
			return portalOut{}, err
		}
	}
	var num int64
	err = s.DB.QueryRow(ctx, `SELECT num FROM projects WHERE id = $1`, p.ID).Scan(&num)
	return portalOut{Result: num}, err
}

// sonet_group.update: GROUP_ID and the fields of sonet_group.create to change (OWNER_ID hands the group over) → the
// group's number.
func (s *Server) portalGroupUpdate(c portalCall) (portalOut, error) {
	p, err := s.portalGroup(c)
	if err != nil {
		return portalOut{}, err
	}
	if !p.CanManage() {
		return portalOut{}, tasks.ErrForbidden
	}
	if err := s.portalGroupFields(c, p); err != nil {
		return portalOut{}, err
	}
	if owner, err := s.portalNum(c.r.Context(), "users", portalParam(c.in, "OWNER_ID")); err != nil {
		return portalOut{}, err
	} else if owner != "" {
		if p, err = s.portalGroupReload(c, p); err != nil {
			return portalOut{}, err
		}
		if err := s.portalGroupSetOwner(c, p, owner); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: portalInt(portalStr(portalParam(c.in, "GROUP_ID")))}, nil
}

// portalInt is a number of a parameter (0 — none).
func portalInt(v string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n
}

// sonet_group.delete: GROUP_ID → true. As on the group's page, its owner or an administrator; a group with tasks is
// not deleted.
func (s *Server) portalGroupDelete(c portalCall) (portalOut, error) {
	p, err := s.portalGroup(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Tasks.DeleteProject(c.r.Context(), p, c.p.UserID, p.Name); err != nil {
		return portalOut{}, err
	}
	s.Log.Info("project deleted", "user_id", c.p.UserID, "project", p.Key, "portal", true)
	s.Matrix.Wake()
	return portalOut{Result: true}, nil
}

// portalGroupSetOwner hands the group to the employee, who joins it first when not in it.
func (s *Server) portalGroupSetOwner(c portalCall, p tasks.Project, user string) error {
	ctx := c.r.Context()
	var in bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id = $1 AND user_id::text = $2)`,
		p.ID, user).Scan(&in); err != nil {
		return err
	}
	if !in && !p.Collab() {
		if err := s.Tasks.SetMember(ctx, p, user, "member"); err != nil {
			return err
		}
	}
	return s.Tasks.TransferProject(ctx, p, c.p.UserID, user)
}

// sonet_group.setowner: GROUP_ID, USER_ID → true.
func (s *Server) portalGroupOwner(c portalCall) (portalOut, error) {
	p, err := s.portalGroup(c)
	if err != nil {
		return portalOut{}, err
	}
	user, err := s.portalNum(c.r.Context(), "users", portalParam(c.in, "USER_ID"))
	if err != nil {
		return portalOut{}, err
	}
	if user == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "USER_ID_EMPTY", "err.portal_user")
	}
	if err := s.portalGroupSetOwner(c, p, user); err != nil {
		return portalOut{}, err
	}
	s.Matrix.Wake()
	return portalOut{Result: true}, nil
}

// sonet_group.get: the groups the caller sees — FILTER (ID, NAME, OWNER_ID, PROJECT, CLOSED, VISIBLE, OPENED,
// SUBJECT_ID… with «!», «%», «>», «>=», «<», «<=»), ORDER (ID DESC by default); IS_ADMIN Y — all of them, to an
// administrator.
func (s *Server) portalGroupList(c portalCall) (portalOut, error) {
	if portalFlag(portalParam(c.in, "IS_ADMIN")) && s.apiAdmin(c.r.Context(), c.p.UserID) {
		c.r = c.r.WithContext(tasks.WithAdmin(c.r.Context()))
	}
	list, err := s.portalGroupRows(c, "")
	if err != nil {
		return portalOut{}, err
	}
	rows := make([]map[string]any, 0, len(list))
	for _, g := range list {
		rows = append(rows, g.Row)
	}
	return portalRowsPageBy(c, rows, "ID", true), nil
}

// sonet_group.user.get: ID (GROUP_ID) → [{USER_ID, ROLE}] of the group's members.
func (s *Server) portalGroupUsers(c portalCall) (portalOut, error) {
	v := portalParam(c.in, "ID")
	if portalStr(v) == "" {
		v = portalParam(c.in, "GROUP_ID")
	}
	p, err := s.portalGroupNum(c, v, true)
	if err != nil {
		return portalOut{}, err
	}
	rows, err := s.DB.Query(c.r.Context(), `SELECT u.num, pm.role FROM project_members pm JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = $1 AND u.active ORDER BY CASE pm.role WHEN 'owner' THEN 0 WHEN 'manager' THEN 1 ELSE 2 END, u.num`, p.ID)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for rows.Next() {
		var n int64
		var role string
		if err := rows.Scan(&n, &role); err != nil {
			rows.Close()
			return portalOut{}, err
		}
		out = append(out, map[string]any{"USER_ID": strconv.FormatInt(n, 10), "ROLE": portalGroupRoles[role]})
	}
	rows.Close()
	return portalOut{Result: out}, rows.Err()
}

// portalGroupPeople are the employees of USER_ID (a number or a list of them): their go24 ids by their numbers.
func (s *Server) portalGroupPeople(c portalCall) (map[string]string, []string, error) {
	ids, nums := map[string]string{}, []string{}
	for _, v := range portalList(portalParam(c.in, "USER_ID")) {
		id, err := s.portalNum(c.r.Context(), "users", v)
		if err != nil || id == "" {
			continue //, the ones not found are skipped
		}
		if _, ok := ids[id]; !ok {
			ids[id] = strings.TrimSpace(v)
			nums = append(nums, id)
		}
	}
	if len(nums) == 0 {
		return nil, nil, s.portalErr(c.r, http.StatusBadRequest, "USER_ID_EMPTY", "err.portal_user")
	}
	return ids, nums, nil
}

// sonet_group.user.add: GROUP_ID, USER_ID (one or a list) — they become members at once, with no invitation (its
// owner and moderators) → the numbers of those added.
func (s *Server) portalGroupUserAdd(c portalCall) (portalOut, error) {
	p, err := s.portalGroup(c)
	if err != nil {
		return portalOut{}, err
	}
	nums, ids, err := s.portalGroupPeople(c)
	if err != nil {
		return portalOut{}, err
	}
	out := []string{}
	for _, id := range ids {
		var in bool
		if err := s.DB.QueryRow(c.r.Context(), `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)`,
			p.ID, id).Scan(&in); err != nil {
			return portalOut{}, err
		}
		if in {
			continue
		}
		if err := s.Tasks.SetMember(c.r.Context(), p, id, "member"); err != nil {
			if errors.Is(err, tasks.ErrForbidden) {
				return portalOut{}, err
			}
			continue
		}
		out = append(out, nums[id])
	}
	if len(out) > 0 {
		s.Matrix.Wake() // they join the group's chat
	}
	return portalOut{Result: out}, nil
}

// sonet_group.user.delete: GROUP_ID, USER_ID (one or a list) → the numbers of those taken out; the owner stays.
func (s *Server) portalGroupUserDelete(c portalCall) (portalOut, error) {
	p, err := s.portalGroup(c)
	if err != nil {
		return portalOut{}, err
	}
	if !p.CanManage() {
		return portalOut{}, tasks.ErrForbidden
	}
	nums, ids, err := s.portalGroupPeople(c)
	if err != nil {
		return portalOut{}, err
	}
	out := []string{}
	for _, id := range ids {
		var role string
		err := s.DB.QueryRow(c.r.Context(), `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`, p.ID, id).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) || role == "owner" {
			continue
		}
		if err != nil {
			return portalOut{}, err
		}
		if err := s.Tasks.RemoveMember(c.r.Context(), p, id); err != nil {
			if errors.Is(err, tasks.ErrForbidden) {
				return portalOut{}, err
			}
			continue
		}
		out = append(out, nums[id])
	}
	if len(out) > 0 {
		s.Matrix.Wake()
	}
	return portalOut{Result: out}, nil
}

// sonet_group.user.invite: GROUP_ID, USER_ID (one or a list), MESSAGE → the numbers of those invited; the bell asks
// each, as «Пригласить в группу» does.
func (s *Server) portalGroupUserInvite(c portalCall) (portalOut, error) {
	p, err := s.portalGroup(c)
	if err != nil {
		return portalOut{}, err
	}
	nums, ids, err := s.portalGroupPeople(c)
	if err != nil {
		return portalOut{}, err
	}
	out := []string{}
	for _, id := range ids {
		if err := s.Tasks.InviteMember(c.r.Context(), p, c.p.UserID, id, "member"); err != nil {
			if errors.Is(err, tasks.ErrForbidden) {
				return portalOut{}, err
			}
			continue // in the group already
		}
		out = append(out, nums[id])
	}
	return portalOut{Result: out}, nil
}

// sonet_group.user.request: GROUP_ID, MESSAGE → true; the caller joins an open group at once or asks to join a
// closed one, as «Вступить» of «Все группы».
func (s *Server) portalGroupRequest(c portalCall) (portalOut, error) {
	p, err := s.portalGroupNum(c, portalParam(c.in, "GROUP_ID"), true)
	if err != nil {
		return portalOut{}, err
	}
	if p.MyRole != "" && p.MyRole != "admin" {
		return portalOut{}, i18n.E("err.already_member")
	}
	joined, err := s.Tasks.JoinGroup(c.r.Context(), p, c.p.UserID, portalStr(portalParam(c.in, "MESSAGE")))
	if err != nil {
		return portalOut{}, err
	}
	if joined {
		s.Matrix.Wake()
	}
	return portalOut{Result: true}, nil
}

// sonet_group.user.groups: the groups the caller is in → [{GROUP_ID, GROUP_NAME, ROLE, GROUP_IMAGE_ID, GROUP_IMAGE,
// IS_EXTRANET}].
func (s *Server) portalGroupsOfUser(c portalCall) (portalOut, error) {
	list, err := s.portalGroupRows(c, ` AND pm.user_id IS NOT NULL`)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for _, g := range list {
		out = append(out, map[string]any{"GROUP_ID": g.Row["ID"], "GROUP_NAME": g.Row["NAME"], "ROLE": portalGroupRoles[g.Role],
			"GROUP_IMAGE_ID": nil, "GROUP_IMAGE": g.Row["IMAGE"], "IS_EXTRANET": g.Row["IS_EXTRANET"]})
	}
	return portalOut{Result: out}, nil
}

// portalGroupFeatures: a feature of a portal group → the go24 tool.
var portalGroupFeatures = map[string]string{"tasks": "tasks", "files": "drive", "calendar": "calendar", "blog": "feed",
	"chat": "chat", "photo": ""}

// sonet_group.feature.access: GROUP_ID, FEATURE (tasks, files, calendar, blog, chat, photo), OPERATION → whether the
// caller may: «view…» — one who sees the group (anyone in an open one), «create_tasks» — as the group's rights to its
// tasks say, «full…», «moderate…», «premoderate…», «delete…», «edit_tasks», «modify_folders» — its owner and
// moderators, the rest — its members; a tool switched off, or one go24 has not (photo), — none.
func (s *Server) portalGroupFeature(c portalCall) (portalOut, error) {
	p, err := s.portalGroupNum(c, portalParam(c.in, "GROUP_ID"), true)
	if err != nil {
		return portalOut{}, err
	}
	feature := strings.ToLower(strings.TrimSpace(portalStr(portalParam(c.in, "FEATURE"))))
	tool, ok := portalGroupFeatures[feature]
	if !ok {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "FEATURE")
	}
	op := strings.ToLower(strings.TrimSpace(portalStr(portalParam(c.in, "OPERATION"))))
	if op == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "OPERATION")
	}
	if tool == "" || !p.ToolOn(tool) {
		return portalOut{Result: false}, nil
	}
	member := p.MyRole != ""
	var may bool
	switch {
	case strings.HasPrefix(op, "view"):
		may = member || p.Privacy == "open" && !p.Collab()
	case op == "create_tasks":
		may = p.MayCreateTasks()
	case slices.ContainsFunc([]string{"full", "moderate", "premoderate", "delete"}, func(x string) bool { return strings.HasPrefix(op, x) }),
		op == "edit_tasks", op == "modify_folders":
		may = !p.Archived && p.CanManage()
	default:
		may = p.CanWork()
	}
	return portalOut{Result: may}, nil
}

// portalGroupFull is a group as socialnetwork.api.workgroup.get gives it: the fields of sonet_group.get as numbers, the
// members by their roles, the group's chat.
func (s *Server) portalGroupFull(c portalCall, g portalGroupRow) (map[string]any, error) {
	ctx := c.r.Context()
	out := map[string]any{}
	for k, v := range g.Row {
		out[k] = v
	}
	for _, k := range []string{"ID", "SUBJECT_ID", "OWNER_ID", "NUMBER_OF_MEMBERS", "NUMBER_OF_MODERATORS", "SCRUM_MASTER_ID"} {
		out[k] = portalInt(portalStr(g.Row[k]))
	}
	out["SCRUM_OWNER_ID"], out["IMAGE_ID"], out["AVATAR"] = 0, 0, g.Row["IMAGE"]
	if g.Row["TYPE"] == "scrum" {
		out["SCRUM_OWNER_ID"] = out["OWNER_ID"]
	}
	members, ordinary, moderators, invited := []int64{}, []int64{}, []int64{}, []int64{}
	rows, err := s.DB.Query(ctx, `SELECT u.num, pm.role FROM project_members pm JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = $1 AND u.active ORDER BY u.num`, g.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n int64
		var role string
		if err := rows.Scan(&n, &role); err != nil {
			rows.Close()
			return nil, err
		}
		members = append(members, n)
		switch role {
		case "manager":
			moderators = append(moderators, n)
		case "member", "viewer":
			ordinary = append(ordinary, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.DB.Query(ctx, `SELECT u.num FROM project_invites i JOIN users u ON u.id = i.user_id WHERE i.project_id = $1 ORDER BY u.num`, g.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		invited = append(invited, n)
	}
	rows.Close()
	out["MEMBERS"], out["ORDINARY_MEMBERS"], out["MODERATOR_MEMBERS"], out["INVITED_MEMBERS"] = members, ordinary, moderators, invited
	out["CHAT_ID"], out["DIALOG_ID"] = 0, ""
	if room, err := s.Matrix.ProjectRoom(ctx, g.ID); err == nil && room != "" {
		if n, err := s.portalChatNum(ctx, room); err == nil {
			out["CHAT_ID"], out["DIALOG_ID"] = n, "chat"+strconv.FormatInt(n, 10)
		}
	}
	return out, rows.Err()
}

// socialnetwork.api.workgroup.get: params {groupId} → the group (an open or closed one to an outsider too).
func (s *Server) portalWorkgroupGet(c portalCall) (portalOut, error) {
	params := portalMap(portalParam(c.in, "params"))
	id := portalParam(params, "groupId")
	if id == nil {
		id = portalParam(c.in, "groupId")
	}
	p, err := s.portalGroupNum(c, id, true)
	if err != nil {
		return portalOut{}, err
	}
	g, err := s.portalGroupRowOf(c, p.ID)
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalGroupFull(c, g)
	return portalOut{Result: out}, err
}

// socialnetwork.api.workgroup.list: filter, order and select as sonet_group.get has them → {workgroups: [{id, name,
// type, avatar, dialogId (the group's chat, to its members), additionalData: {role}} and the fields selected, in camelCase]}.
func (s *Server) portalWorkgroupList(c portalCall) (portalOut, error) {
	list, err := s.portalGroupRows(c, "")
	if err != nil {
		return portalOut{}, err
	}
	rows, ids := make([]map[string]any, 0, len(list)), map[string]string{}
	for _, g := range list {
		row := maps.Clone(g.Row)
		row["ROLE"] = portalGroupRoles[g.Role]
		rows, ids[portalStr(g.Row["ID"])] = append(rows, row), g.ID
	}
	sel := portalList(portalParam(c.in, "select"))
	c.in = maps.Clone(c.in)
	delete(c.in, "select")
	page := portalRowsPageBy(c, rows, "ID", false)
	groups := []map[string]any{}
	for _, row := range page.Result.([]map[string]any) {
		g := map[string]any{"id": portalInt(portalStr(row["ID"])), "name": row["NAME"], "type": row["TYPE"], "imageId": nil,
			"avatarType": nil, "avatar": row["IMAGE"], "dialogId": "", "additionalData": map[string]any{"role": row["ROLE"], "initiatedByType": ""}}
		if room, err := s.Matrix.ProjectRoom(c.r.Context(), ids[portalStr(row["ID"])]); err == nil && room != "" && row["ROLE"] != "" {
			if n, err := s.portalChatNum(c.r.Context(), room); err == nil {
				g["dialogId"] = "chat" + strconv.FormatInt(n, 10)
			}
		}
		for _, k := range sel {
			if v, ok := row[strings.ToUpper(k)]; ok {
				g[portalCamel(k)] = v
			}
		}
		groups = append(groups, g)
	}
	page.Result = map[string]any{"workgroups": groups}
	return page, nil
}
