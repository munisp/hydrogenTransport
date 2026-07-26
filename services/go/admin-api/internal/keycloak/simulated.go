package keycloak

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// simulated is the dev fallback AdminClient, selected only via the explicit
// opt-in H2_SIMULATED_KEYCLOAK=true when KEYCLOAK_ADMIN_CLIENT_ID/SECRET are
// unset (New fails closed otherwise). It keeps users in memory so the
// onboarding and user-management flows are fully exercisable locally; every
// operation is logged with a clear "SIMULATED" marker.
type simulated struct {
	log   *zap.Logger
	mu    sync.Mutex
	users map[string]*User // id -> user
}

func newSimulated(log *zap.Logger) *simulated {
	return &simulated{log: log, users: map[string]*User{}}
}

func simID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "sim-" + hex.EncodeToString(b)
}

func (s *simulated) CreateUser(_ context.Context, spec CreateUserSpec) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if strings.EqualFold(u.Email, spec.Email) {
			s.log.Warn("SIMULATED keycloak: user already exists, adopting existing id",
				zap.String("email", spec.Email), zap.String("user_id", u.ID))
			return u.ID, nil
		}
	}
	first, last := splitDisplayName(spec.DisplayName)
	u := &User{
		ID:        simID(),
		Username:  spec.Username,
		Email:     spec.Email,
		FirstName: first,
		LastName:  last,
		Enabled:   true,
		Roles:     []string{},
	}
	s.users[u.ID] = u
	s.log.Warn("SIMULATED keycloak: created user (no real Keycloak user was provisioned)",
		zap.String("user_id", u.ID), zap.String("username", u.Username), zap.String("email", u.Email))
	return u.ID, nil
}

func (s *simulated) SetTemporaryPassword(_ context.Context, userID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[userID]; !ok {
		return fmt.Errorf("SIMULATED keycloak: unknown user %q", userID)
	}
	s.log.Warn("SIMULATED keycloak: temporary password set", zap.String("user_id", userID))
	return nil
}

func (s *simulated) AssignRealmRole(_ context.Context, userID, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return fmt.Errorf("SIMULATED keycloak: unknown user %q", userID)
	}
	for _, r := range u.Roles {
		if r == role {
			return nil
		}
	}
	u.Roles = append(u.Roles, role)
	s.log.Warn("SIMULATED keycloak: assigned realm role",
		zap.String("user_id", userID), zap.String("role", role))
	return nil
}

func (s *simulated) RevokeRealmRole(_ context.Context, userID, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return fmt.Errorf("SIMULATED keycloak: unknown user %q", userID)
	}
	out := u.Roles[:0]
	for _, r := range u.Roles {
		if r != role {
			out = append(out, r)
		}
	}
	u.Roles = out
	s.log.Warn("SIMULATED keycloak: revoked realm role",
		zap.String("user_id", userID), zap.String("role", role))
	return nil
}

func (s *simulated) SendActionsEmail(_ context.Context, userID string, actions []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[userID]; !ok {
		return fmt.Errorf("SIMULATED keycloak: unknown user %q", userID)
	}
	s.log.Warn("SIMULATED keycloak: execute-actions email sent",
		zap.String("user_id", userID), zap.Strings("actions", actions))
	return nil
}

func (s *simulated) ListUsers(_ context.Context, search, role string, max int) ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		if role != "" && !contains(u.Roles, role) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(u.Username+" "+u.Email), strings.ToLower(search)) {
			continue
		}
		cp := *u
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out, nil
}

func (s *simulated) SetEnabled(_ context.Context, userID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return fmt.Errorf("SIMULATED keycloak: unknown user %q", userID)
	}
	u.Enabled = enabled
	s.log.Warn("SIMULATED keycloak: user enabled flag changed",
		zap.String("user_id", userID), zap.Bool("enabled", enabled))
	return nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
