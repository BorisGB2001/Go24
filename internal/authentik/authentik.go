// Package authentik writes employee data from go24 into Authentik, only where Authentik has none:
// the person's name and attributes (phones, position, department…). It never overwrites a value that is
// already there and never touches passwords, groups or the e-mail. It also makes invitations (sign-up links).
// It runs as the service account go24-sync (blueprint go24.yaml), which may only view and change users,
// view flows and create invitations.
package authentik

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	base, token string
	http        *http.Client
}

func New(baseURL, token string) *Client {
	if baseURL == "" || token == "" {
		return nil
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Timeout: 10 * time.Second}}
}

type user struct {
	PK         int            `json:"pk"`
	Name       string         `json:"name"`
	Email      string         `json:"email"`
	Attributes map[string]any `json:"attributes"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v3"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("authentik %s %s: HTTP %d", method, strings.SplitN(path, "?", 2)[0], res.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// Profile is what go24 knows about the person. Empty values are skipped.
type Profile struct {
	Email      string
	Name       string
	Attributes map[string]string // e.g. phone, work_phone, inner_phone, position, department, city
}

// FillMissing finds the Authentik user by e-mail and sets the name and the attributes that are empty there.
// It reports whether anything changed; "no such user" is not an error (not everyone is in Authentik).
func (c *Client) FillMissing(ctx context.Context, p Profile) (bool, error) {
	if c == nil || strings.TrimSpace(p.Email) == "" {
		return false, nil
	}
	var page struct {
		Results []user `json:"results"`
	}
	if err := c.do(ctx, http.MethodGet, "/core/users/?email="+url.QueryEscape(p.Email), nil, &page); err != nil {
		return false, err
	}
	var u *user
	for i := range page.Results { // the filter is exact in current Authentik; double-check anyway
		if strings.EqualFold(page.Results[i].Email, p.Email) {
			u = &page.Results[i]
			break
		}
	}
	if u == nil {
		return false, nil
	}
	patch := map[string]any{}
	if strings.TrimSpace(u.Name) == "" && strings.TrimSpace(p.Name) != "" {
		patch["name"] = p.Name
	}
	attrs := map[string]any{}
	for k, v := range u.Attributes {
		attrs[k] = v
	}
	changed := false
	for k, v := range p.Attributes {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if cur, ok := attrs[k]; ok && strings.TrimSpace(fmt.Sprint(cur)) != "" {
			continue // Authentik already has it: keep theirs
		}
		attrs[k] = v
		changed = true
	}
	if changed {
		patch["attributes"] = attrs
	}
	if len(patch) == 0 {
		return false, nil
	}
	return true, c.do(ctx, http.MethodPatch, fmt.Sprintf("/core/users/%d/", u.PK), patch, nil)
}

// EnrollmentFlow is the flow of the go24 enrollment blueprint (deploy/authentik/blueprints/go24-enrollment.yaml).
const EnrollmentFlow = "go24-enrollment"

// GuestEnrollmentFlow is the sign-up of guests invited into collabs (deploy/authentik/blueprints/go24-guests.yaml):
// the people it makes are in the group GuestGroup, by which go24 knows them for guests.
const GuestEnrollmentFlow = "go24-guest-enrollment"

// GuestGroup is the Authentik group of guests (GO24_GUEST_GROUPS by default).
const GuestGroup = "go24-guests"

// LeaveGroup takes the Authentik user with this login out of the group (a guest made an employee leaves the
// guests' group, so the next sign-in does not make them a guest again). No such user or group, or not a member —
// nothing to do.
func (c *Client) LeaveGroup(ctx context.Context, username, group string) error {
	if c == nil || strings.TrimSpace(username) == "" {
		return nil
	}
	var users struct {
		Results []struct {
			PK       int    `json:"pk"`
			Username string `json:"username"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodGet, "/core/users/?username="+url.QueryEscape(username), nil, &users); err != nil {
		return err
	}
	var groups struct {
		Results []struct {
			PK   string `json:"pk"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodGet, "/core/groups/?name="+url.QueryEscape(group), nil, &groups); err != nil {
		return err
	}
	for _, u := range users.Results {
		if u.Username != username {
			continue
		}
		for _, g := range groups.Results {
			if g.Name == group {
				if err := c.do(ctx, http.MethodPost, "/core/groups/"+g.PK+"/remove_user/", map[string]int{"pk": u.PK}, nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Invitation is a one-time sign-up link: the person sets a login and a password, the name and e-mail are fixed.
type Invitation struct {
	ID      string
	Link    string
	Expires time.Time
}

// Invite makes a single-use invitation into the go24 enrollment flow. public is the Authentik address people
// open (the OIDC issuer's host), unlike the internal base go24 calls.
func (c *Client) Invite(ctx context.Context, public, email, name string, ttl time.Duration) (Invitation, error) {
	return c.invite(ctx, public, EnrollmentFlow, email, name, ttl)
}

// InviteGuest makes a single-use sign-up link into the guests' flow for a person invited into a collab. A guest
// invited personally gets their e-mail and name fixed in it (email ""): one who came by a common link types them.
func (c *Client) InviteGuest(ctx context.Context, public, email, name string, ttl time.Duration) (Invitation, error) {
	return c.invite(ctx, public, GuestEnrollmentFlow, email, name, ttl)
}

// DeleteInvitation cancels an invitation not used yet (one already gone is fine).
func (c *Client) DeleteInvitation(ctx context.Context, id string) error {
	if c == nil {
		return fmt.Errorf("authentik: not configured")
	}
	err := c.do(ctx, http.MethodDelete, "/stages/invitation/invitations/"+url.PathEscape(id)+"/", nil, nil)
	if err != nil && strings.Contains(err.Error(), "HTTP 404") {
		return nil
	}
	return err
}

func (c *Client) invite(ctx context.Context, public, flowSlug, email, name string, ttl time.Duration) (Invitation, error) {
	if c == nil {
		return Invitation{}, fmt.Errorf("authentik: not configured")
	}
	var flow struct {
		PK string `json:"pk"`
	}
	if err := c.do(ctx, http.MethodGet, "/flows/instances/"+flowSlug+"/", nil, &flow); err != nil {
		return Invitation{}, err
	}
	exp := time.Now().Add(ttl).UTC().Truncate(time.Second)
	var out struct {
		PK string `json:"pk"`
	}
	who := strings.NewReplacer("@", "-at-", ".", "-").Replace(strings.ToLower(email))
	fixed := map[string]string{}
	if email != "" {
		fixed["email"] = email
	} else {
		who = "guest"
	}
	if name != "" {
		fixed["name"] = name
	}
	err := c.do(ctx, http.MethodPost, "/stages/invitation/invitations/", map[string]any{
		"name":    "go24-" + who + "-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		"expires": exp.Format(time.RFC3339), "single_use": true, "flow": flow.PK, "fixed_data": fixed,
	}, &out)
	if err != nil {
		return Invitation{}, err
	}
	return Invitation{ID: out.PK, Expires: exp,
		Link: strings.TrimRight(public, "/") + "/if/flow/" + flowSlug + "/?itoken=" + url.QueryEscape(out.PK)}, nil
}
