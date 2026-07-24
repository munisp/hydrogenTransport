// Permify authorization checks (SPEC §3.5: "Permify checks on admin routes").
//
// This is a minimal, hand-written gRPC client for Permify's
// permify.v1.PermissionService/Check RPC. It talks plain protobuf wire format
// through a custom gRPC codec so the shared module does not need generated
// Permify protobuf bindings; the messages used here (Entity, Subject,
// CheckMetadata, CheckRequest, CheckResponse) are small and stable.
//
// Fail-closed / fallback semantics (documented contract):
//   - PERMIFY_GRPC set: checks are enforced. A DENIED result yields 403 and a
//     transport/check error yields 502 — the route never silently allows.
//   - PERMIFY_GRPC unset: NewPermify returns nil and the Require middleware is
//     a pass-through that logs a warning once per process. The route then
//     relies on the Keycloak realm-role check alone (role-only fallback,
//     acceptable for local dev; never unset PERMIFY_GRPC in production).
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
)

// permifyCheckMethod is the full gRPC method of Permify's permission check.
const permifyCheckMethod = "/permify.v1.PermissionService/Check"

// Permify check depth: Permify rejects depth < 3; 20 matches its own default.
const permifyCheckDepth = 20

// rawMessage carries already-encoded protobuf wire bytes.
type rawMessage struct{ b []byte }

// rawCodec passes wire bytes through unchanged. It is registered under its
// own content-subtype ("permify-raw") so it never interferes with the default
// "proto" codec used by other gRPC clients in the same binary (e.g. Dapr).
type rawCodec struct{}

func (rawCodec) Name() string { return "permify-raw" }

func (rawCodec) Marshal(v any) ([]byte, error) {
	m, ok := v.(*rawMessage)
	if !ok {
		return nil, fmt.Errorf("permify rawCodec cannot marshal %T", v)
	}
	return m.b, nil
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	m, ok := v.(*rawMessage)
	if !ok {
		return fmt.Errorf("permify rawCodec cannot unmarshal into %T", v)
	}
	m.b = append(m.b[:0], data...)
	return nil
}

func init() { encoding.RegisterCodec(rawCodec{}) }

// PermifyClient evaluates Permify permission checks over gRPC.
type PermifyClient struct {
	conn   *grpc.ClientConn
	tenant string
	log    *zap.Logger
}

// NewPermify builds a client for the PERMIFY_GRPC address (e.g.
// "permify:3476"). tenant is the Permify tenant id (PERMIFY_TENANT, default
// "t1" — the tenant the infra/permify schema is loaded into). Returns nil
// when addr is empty (Permify not configured: role-only fallback, see
// package comment above).
func NewPermify(addr, tenant string, log *zap.Logger) *PermifyClient {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	if tenant == "" {
		tenant = "t1"
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		// grpc.NewClient only fails on malformed target; treat as unconfigured.
		log.Error("permify client init failed; falling back to role-only authorization",
			zap.String("addr", addr), zap.Error(err))
		return nil
	}
	log.Info("permify authorization checks enabled", zap.String("addr", addr), zap.String("tenant", tenant))
	return &PermifyClient{conn: conn, tenant: tenant, log: log}
}

// Close releases the underlying gRPC connection (safe on nil).
func (c *PermifyClient) Close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
	}
}

// Check evaluates `entityType:entityID#permission@user:subjectID` and returns
// whether the decision is ALLOWED.
func (c *PermifyClient) Check(ctx context.Context, entityType, entityID, permission, subjectID string) (bool, error) {
	if c == nil || c.conn == nil {
		return false, errors.New("permify client not configured")
	}
	req := marshalCheckRequest(c.tenant, entityType, entityID, permission, subjectID)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp := &rawMessage{}
	if err := c.conn.Invoke(ctx, permifyCheckMethod, &rawMessage{b: req}, resp,
		grpc.CallContentSubtype(rawCodec{}.Name())); err != nil {
		return false, fmt.Errorf("permify check %s:%s#%s: %w", entityType, entityID, permission, err)
	}
	can, err := parseCheckResponse(resp.b)
	if err != nil {
		return false, fmt.Errorf("permify check response: %w", err)
	}
	return can == 1, nil // 1 = CHECK_RESULT_ALLOWED
}

var permifyFallbackWarnOnce sync.Once

// Require returns middleware that demands `entityType:<id>#permission` for the
// JWT subject, where the entity id is extracted from the request by idFrom
// (e.g. a chi URL parameter). It must run AFTER RequireAuth/RequireRole so
// claims are present.
//
// When the client is nil (PERMIFY_GRPC unset) the middleware logs a warning
// once and passes through — role-only fallback, see package comment.
func (c *PermifyClient) Require(entityType, permission string, idFrom func(*http.Request) string) func(http.Handler) http.Handler {
	log := c.log
	if log == nil {
		log = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c == nil || c.conn == nil {
				permifyFallbackWarnOnce.Do(func() {
					log.Warn("PERMIFY_GRPC unset; Permify check skipped, relying on Keycloak role only",
						zap.String("entity", entityType), zap.String("permission", permission))
				})
				next.ServeHTTP(w, r)
				return
			}
			sub := Subject(r.Context())
			if sub == "" {
				writeError(w, http.StatusUnauthorized, "authenticated subject required for authorization check")
				return
			}
			entityID := ""
			if idFrom != nil {
				entityID = idFrom(r)
			}
			allowed, err := c.Check(r.Context(), entityType, entityID, permission, sub)
			if err != nil {
				c.log.Error("permify check failed (fail-closed)",
					zap.String("entity", entityType+":"+entityID),
					zap.String("permission", permission),
					zap.Error(err))
				writeError(w, http.StatusBadGateway, "authorization check unavailable")
				return
			}
			if !allowed {
				writeError(w, http.StatusForbidden,
					fmt.Sprintf("permify denied %s:%s#%s", entityType, entityID, permission))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- minimal protobuf wire encoding for permify.v1.PermissionService/Check ---
//
//	message Entity        { string type = 1; string id = 2; }
//	message Subject       { string type = 1; string id = 2; string relation = 3; }
//	message CheckMetadata { string snap_token = 1; string schema_version = 2; int32 depth = 3; }
//	message CheckRequest  {
//	  string tenant_id = 1; CheckMetadata metadata = 2; Entity entity = 3;
//	  string permission = 4; Subject subject = 5;
//	}
//	message CheckResponse { CheckResult can = 1; CheckMetadata metadata = 2; }
//	enum CheckResult { CHECK_RESULT_UNSPECIFIED = 0; CHECK_RESULT_ALLOWED = 1; CHECK_RESULT_DENIED = 2; }

func marshalCheckRequest(tenant, entityType, entityID, permission, subjectID string) []byte {
	entity := append(pbString(1, entityType), pbString(2, entityID)...)
	subject := append(pbString(1, "user"), pbString(2, subjectID)...)
	metadata := pbVarint(3, permifyCheckDepth)

	out := pbString(1, tenant)
	out = append(out, pbBytes(2, metadata)...)
	out = append(out, pbBytes(3, entity)...)
	out = append(out, pbString(4, permission)...)
	out = append(out, pbBytes(5, subject)...)
	return out
}

// parseCheckResponse extracts field 1 (`can`) as an enum value.
func parseCheckResponse(b []byte) (int, error) {
	for len(b) > 0 {
		field, wire, n, err := consumeTag(b)
		if err != nil {
			return 0, err
		}
		b = b[n:]
		switch wire {
		case 0: // varint
			v, m, err := consumeVarint(b)
			if err != nil {
				return 0, err
			}
			if field == 1 {
				return int(v), nil
			}
			b = b[m:]
		case 2: // length-delimited
			l, m, err := consumeVarint(b)
			if err != nil {
				return 0, err
			}
			b = b[m:]
			if uint64(len(b)) < l {
				return 0, errors.New("truncated length-delimited field")
			}
			b = b[l:]
		default:
			return 0, fmt.Errorf("unsupported wire type %d", wire)
		}
	}
	return 0, errors.New("missing `can` field")
}

func pbString(field int, s string) []byte {
	return pbBytes(field, []byte(s))
}

func pbBytes(field int, b []byte) []byte {
	out := appendVarint(nil, uint64(field<<3|2))
	out = appendVarint(out, uint64(len(b)))
	return append(out, b...)
}

func pbVarint(field int, v uint64) []byte {
	out := appendVarint(nil, uint64(field<<3))
	return appendVarint(out, v)
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func consumeTag(b []byte) (field, wire, n int, err error) {
	v, n, err := consumeVarint(b)
	if err != nil {
		return 0, 0, 0, err
	}
	return int(v >> 3), int(v & 7), n, nil
}

func consumeVarint(b []byte) (uint64, int, error) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1, nil
		}
	}
	return 0, 0, errors.New("invalid varint")
}
