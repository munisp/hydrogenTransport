// Package keycloak is a minimal Keycloak Admin REST client used by admin-api
// for stakeholder onboarding and user management. It authenticates with
// client-credentials (KEYCLOAK_ADMIN_CLIENT_ID/SECRET, service account in the
// h2fleet realm with realm-management roles) against
// KEYCLOAK_ADMIN_URL (default http://keycloak:8080).
//
// When the admin client credentials are unset, New returns a simulated
// in-memory client so local development works without a privileged Keycloak
// service account; every simulated call is logged loudly.
package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// User is the admin-api view of a Keycloak realm user.
type User struct {
	ID        string   `json:"id"`
	Username  string   `json:"username"`
	Email     string   `json:"email"`
	FirstName string   `json:"first_name"`
	LastName  string   `json:"last_name"`
	Enabled   bool     `json:"enabled"`
	Roles     []string `json:"roles"`
}

// CreateUserSpec describes a user to provision.
type CreateUserSpec struct {
	Username    string // login name (we use the email address)
	Email       string
	DisplayName string // split into first/last name
}

// AdminClient is the subset of the Keycloak Admin REST API admin-api needs.
type AdminClient interface {
	// CreateUser provisions a user and returns the Keycloak user id.
	CreateUser(ctx context.Context, spec CreateUserSpec) (string, error)
	SetTemporaryPassword(ctx context.Context, userID, password string) error
	AssignRealmRole(ctx context.Context, userID, role string) error
	RevokeRealmRole(ctx context.Context, userID, role string) error
	// SendActionsEmail triggers the Keycloak "execute actions" email
	// (e.g. VERIFY_EMAIL, UPDATE_PASSWORD).
	SendActionsEmail(ctx context.Context, userID string, actions []string) error
	ListUsers(ctx context.Context, search, role string, max int) ([]User, error)
	SetEnabled(ctx context.Context, userID string, enabled bool) error
}

// New returns an HTTP AdminClient when clientID and clientSecret are set,
// otherwise a simulated in-memory client (dev fallback, clearly logged).
func New(adminURL, realm, clientID, clientSecret string, log *zap.Logger) AdminClient {
	if clientID == "" || clientSecret == "" {
		log.Warn("KEYCLOAK_ADMIN_CLIENT_ID/SECRET unset: Keycloak admin operations are SIMULATED (dev fallback, no real users are created)")
		return newSimulated(log)
	}
	return &httpClient{
		adminURL:     strings.TrimSuffix(adminURL, "/"),
		realm:        realm,
		clientID:     clientID,
		clientSecret: clientSecret,
		log:          log,
		http:         &http.Client{Timeout: 10 * time.Second},
	}
}

// --------------------------------------------------------------------------
// HTTP implementation
// --------------------------------------------------------------------------

type httpClient struct {
	adminURL     string
	realm        string
	clientID     string
	clientSecret string
	log          *zap.Logger
	http         *http.Client

	mu        sync.Mutex
	token     string
	tokenExpr time.Time
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func (c *httpClient) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExpr) {
		return c.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	endpoint := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", c.adminURL, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak token request: status %d", resp.StatusCode)
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("keycloak token decode: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("keycloak token response had empty access_token")
	}
	c.token = tr.AccessToken
	// Refresh 30s early to avoid racing token expiry.
	c.tokenExpr = time.Now().Add(time.Duration(tr.ExpiresIn-30) * time.Second)
	return c.token, nil
}

// do issues an authenticated Admin REST call. When body is non-nil it is sent
// as JSON. Returns the response; caller closes the body.
func (c *httpClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.adminURL+"/admin/realms/"+c.realm+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func splitDisplayName(name string) (first, last string) {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return strings.Join(parts[:len(parts)-1], " "), parts[len(parts)-1]
}

type kcUserRep struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Enabled   bool   `json:"enabled"`
}

type kcRoleRep struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *httpClient) CreateUser(ctx context.Context, spec CreateUserSpec) (string, error) {
	first, last := splitDisplayName(spec.DisplayName)
	rep := map[string]any{
		"username":  spec.Username,
		"email":     spec.Email,
		"firstName": first,
		"lastName":  last,
		"enabled":   true,
	}
	resp, err := c.do(ctx, http.MethodPost, "/users", rep)
	if err != nil {
		return "", fmt.Errorf("create user: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated:
		loc := resp.Header.Get("Location")
		id := loc[strings.LastIndex(loc, "/")+1:]
		if id == "" {
			return "", fmt.Errorf("create user: 201 without Location header")
		}
		return id, nil
	case http.StatusConflict:
		// User already exists (e.g. retried onboarding): adopt the existing id.
		id, lookupErr := c.findUserIDByEmail(ctx, spec.Email)
		if lookupErr != nil {
			return "", fmt.Errorf("create user: conflict and lookup failed: %w", lookupErr)
		}
		return id, nil
	default:
		return "", fmt.Errorf("create user: status %d", resp.StatusCode)
	}
}

func (c *httpClient) findUserIDByEmail(ctx context.Context, email string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/users?email="+url.QueryEscape(email)+"&exact=true", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("lookup user by email: status %d", resp.StatusCode)
	}
	var users []kcUserRep
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return "", err
	}
	if len(users) == 0 {
		return "", fmt.Errorf("no user with email %q", email)
	}
	return users[0].ID, nil
}

func (c *httpClient) SetTemporaryPassword(ctx context.Context, userID, password string) error {
	cred := map[string]any{"type": "password", "value": password, "temporary": true}
	resp, err := c.do(ctx, http.MethodPut, "/users/"+userID+"/reset-password", cred)
	if err != nil {
		return fmt.Errorf("reset-password: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("reset-password: status %d", resp.StatusCode)
	}
	return nil
}

func (c *httpClient) roleRep(ctx context.Context, role string) (*kcRoleRep, error) {
	resp, err := c.do(ctx, http.MethodGet, "/roles/"+url.PathEscape(role), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get role %q: status %d", role, resp.StatusCode)
	}
	var rep kcRoleRep
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

func (c *httpClient) AssignRealmRole(ctx context.Context, userID, role string) error {
	rep, err := c.roleRep(ctx, role)
	if err != nil {
		return fmt.Errorf("assign role: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, "/users/"+userID+"/role-mappings/realm", []kcRoleRep{*rep})
	if err != nil {
		return fmt.Errorf("assign role: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("assign role %q: status %d", role, resp.StatusCode)
	}
	return nil
}

func (c *httpClient) RevokeRealmRole(ctx context.Context, userID, role string) error {
	rep, err := c.roleRep(ctx, role)
	if err != nil {
		return fmt.Errorf("revoke role: %w", err)
	}
	resp, err := c.do(ctx, http.MethodDelete, "/users/"+userID+"/role-mappings/realm", []kcRoleRep{*rep})
	if err != nil {
		return fmt.Errorf("revoke role: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("revoke role %q: status %d", role, resp.StatusCode)
	}
	return nil
}

func (c *httpClient) SendActionsEmail(ctx context.Context, userID string, actions []string) error {
	resp, err := c.do(ctx, http.MethodPut, "/users/"+userID+"/execute-actions-email", actions)
	if err != nil {
		return fmt.Errorf("execute-actions-email: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("execute-actions-email: status %d", resp.StatusCode)
	}
	return nil
}

func (c *httpClient) ListUsers(ctx context.Context, search, role string, max int) ([]User, error) {
	if max <= 0 || max > 500 {
		max = 100
	}
	var path string
	if role != "" {
		path = fmt.Sprintf("/roles/%s/users?first=0&max=%d", url.PathEscape(role), max)
	} else {
		path = fmt.Sprintf("/users?first=0&max=%d", max)
		if search != "" {
			path += "&search=" + url.QueryEscape(search)
		}
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list users: status %d", resp.StatusCode)
	}
	var reps []kcUserRep
	if err := json.NewDecoder(resp.Body).Decode(&reps); err != nil {
		return nil, fmt.Errorf("list users decode: %w", err)
	}
	users := make([]User, 0, len(reps))
	for _, rep := range reps {
		u := User{
			ID:        rep.ID,
			Username:  rep.Username,
			Email:     rep.Email,
			FirstName: rep.FirstName,
			LastName:  rep.LastName,
			Enabled:   rep.Enabled,
			Roles:     []string{},
		}
		// Client-side search filter for the role-filtered path (Keycloak's
		// /roles/{role}/users has no search parameter).
		if search != "" && role != "" && !strings.Contains(strings.ToLower(u.Username+" "+u.Email), strings.ToLower(search)) {
			continue
		}
		roles, err := c.userRoles(ctx, rep.ID)
		if err != nil {
			c.log.Warn("failed to load role mappings; returning user without roles",
				zap.String("user_id", rep.ID), zap.Error(err))
		} else {
			u.Roles = roles
		}
		users = append(users, u)
	}
	return users, nil
}

func (c *httpClient) userRoles(ctx context.Context, userID string) ([]string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/users/"+userID+"/role-mappings/realm", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("role-mappings: status %d", resp.StatusCode)
	}
	var reps []kcRoleRep
	if err := json.NewDecoder(resp.Body).Decode(&reps); err != nil {
		return nil, err
	}
	roles := make([]string, 0, len(reps))
	for _, rep := range reps {
		roles = append(roles, rep.Name)
	}
	return roles, nil
}

func (c *httpClient) SetEnabled(ctx context.Context, userID string, enabled bool) error {
	// Keycloak's PUT /users/{id} requires the full representation, so fetch
	// the current one, flip `enabled` and PUT it back.
	resp, err := c.do(ctx, http.MethodGet, "/users/"+userID, nil)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get user: status %d", resp.StatusCode)
	}
	var rep map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		return fmt.Errorf("get user decode: %w", err)
	}
	rep["enabled"] = enabled
	putResp, err := c.do(ctx, http.MethodPut, "/users/"+userID, rep)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("update user: status %d", putResp.StatusCode)
	}
	return nil
}
