package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Context keys for workspace-scoped request data.
type contextKey int

const (
	ctxKeyWorkspaceID contextKey = iota
	ctxKeyMember
	ctxKeySuperAdminRead
)

// MemberFromContext returns the workspace member injected by the workspace middleware.
func MemberFromContext(ctx context.Context) (db.Member, bool) {
	m, ok := ctx.Value(ctxKeyMember).(db.Member)
	return m, ok
}

// WorkspaceIDFromContext returns the workspace ID injected by the workspace middleware.
func WorkspaceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyWorkspaceID).(string)
	return id
}

// SuperAdminReadFromContext reports whether a read-only request was authorized
// through the instance-level super-admin bypass rather than workspace membership.
func SuperAdminReadFromContext(ctx context.Context) bool {
	ok, _ := ctx.Value(ctxKeySuperAdminRead).(bool)
	return ok
}

// SetMemberContext injects workspace ID and member into the context.
// This is useful for handlers that resolve the workspace from an entity lookup
// and want to share the member with downstream code.
func SetMemberContext(ctx context.Context, workspaceID string, member db.Member) context.Context {
	ctx = context.WithValue(ctx, ctxKeyWorkspaceID, workspaceID)
	ctx = context.WithValue(ctx, ctxKeyMember, member)
	return ctx
}

func SetSuperAdminReadContext(ctx context.Context, workspaceID string, userID pgtype.UUID) context.Context {
	workspaceUUID, _ := util.ParseUUID(workspaceID)
	ctx = context.WithValue(ctx, ctxKeyWorkspaceID, workspaceID)
	ctx = context.WithValue(ctx, ctxKeyMember, db.Member{
		WorkspaceID: workspaceUUID,
		UserID:      userID,
		Role:        "member",
	})
	ctx = context.WithValue(ctx, ctxKeySuperAdminRead, true)
	return ctx
}

// errWorkspaceNotFound is returned when a slug was provided but doesn't match
// any workspace. This lets the middleware distinguish "no identifier provided"
// (400) from "identifier provided but invalid" (404).
var errWorkspaceNotFound = errors.New("workspace not found")

// ResolveWorkspaceIDFromRequest returns the workspace UUID for an HTTP
// request using the same priority order as the workspace middleware. This is
// the single source of truth for "which workspace is this request targeting?",
// shared by middleware-protected routes (via context fast path) and
// middleware-less routes (e.g. /api/upload-file) that must resolve the slug
// themselves.
//
// Priority:
//  1. task-token binding (X-Actor-Source == "task_token") — authoritative,
//     server-set, cannot be re-negotiated by the client (MUL-2600)
//  2. middleware-injected context (fast path for middleware-protected routes)
//  3. X-Workspace-Slug header → GetWorkspaceBySlug → UUID (post-refactor frontend)
//  4. ?workspace_slug query → GetWorkspaceBySlug → UUID
//  5. X-Workspace-ID header (CLI/daemon compat)
//  6. ?workspace_id query (CLI/daemon compat)
//
// Returns "" when no identifier was provided OR a slug was provided but
// doesn't resolve to any workspace. Callers that need to distinguish "no
// identifier" (400) from "invalid slug" (404) should use the middleware's
// internal resolver instead — this helper collapses both cases to "" for
// simpler handler-level checks.
func ResolveWorkspaceIDFromRequest(r *http.Request, queries *db.Queries) string {
	// A mat_ task token is bound to exactly one workspace by the token
	// row. Auth middleware writes that workspace into X-Workspace-ID
	// after stripping any client-supplied X-Actor-Source. Any other
	// workspace identifier on the request (slug header/query, ID
	// query, URL param) is the agent trying to widen its blast
	// radius — ignore it.
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return r.Header.Get("X-Workspace-ID")
	}
	if id := WorkspaceIDFromContext(r.Context()); id != "" {
		return id
	}
	if slug := r.Header.Get("X-Workspace-Slug"); slug != "" {
		if ws, err := queries.GetWorkspaceBySlug(r.Context(), slug); err == nil {
			return util.UUIDToString(ws.ID)
		}
	}
	if slug := r.URL.Query().Get("workspace_slug"); slug != "" {
		if ws, err := queries.GetWorkspaceBySlug(r.Context(), slug); err == nil {
			return util.UUIDToString(ws.ID)
		}
	}
	if id := r.Header.Get("X-Workspace-ID"); id != "" {
		return id
	}
	return r.URL.Query().Get("workspace_id")
}

// workspaceResolver extracts a workspace UUID from the request.
// Returns ("", nil) if no workspace identifier was provided at all.
// Returns ("", errWorkspaceNotFound) if a slug was provided but doesn't exist.
// Returns (uuid, nil) on success.
type workspaceResolver func(r *http.Request) (string, error)

// resolveWorkspaceUUID builds a resolver that accepts slug-first identification.
//
// Priority:
//  1. task-token binding (X-Actor-Source == "task_token") — authoritative,
//     server-set; the agent cannot widen its workspace scope by passing a
//     different slug/id (MUL-2600)
//  2. X-Workspace-Slug header / ?workspace_slug query → GetWorkspaceBySlug → UUID
//  3. X-Workspace-ID header / ?workspace_id query → UUID directly (CLI/daemon compat)
//
// TODO: cache slug→UUID lookup (slug is immutable, safe to cache with short TTL)
func resolveWorkspaceUUID(queries *db.Queries) workspaceResolver {
	return func(r *http.Request) (string, error) {
		// Task-token-authenticated requests must operate on the
		// token's bound workspace. The auth middleware wrote that ID
		// into X-Workspace-ID; nothing the agent can put on the wire
		// (slug header/query, id query, URL param) can override it.
		if r.Header.Get("X-Actor-Source") == "task_token" {
			id := r.Header.Get("X-Workspace-ID")
			if id == "" {
				return "", errWorkspaceNotFound
			}
			return id, nil
		}
		// Slug path (preferred — frontend sends this after the URL refactor)
		if slug := r.URL.Query().Get("workspace_slug"); slug != "" {
			ws, err := queries.GetWorkspaceBySlug(r.Context(), slug)
			if err != nil {
				return "", errWorkspaceNotFound
			}
			return util.UUIDToString(ws.ID), nil
		}
		if slug := r.Header.Get("X-Workspace-Slug"); slug != "" {
			ws, err := queries.GetWorkspaceBySlug(r.Context(), slug)
			if err != nil {
				return "", errWorkspaceNotFound
			}
			return util.UUIDToString(ws.ID), nil
		}
		// UUID fallback (CLI, daemon, legacy clients)
		if id := r.URL.Query().Get("workspace_id"); id != "" {
			return id, nil
		}
		if id := r.Header.Get("X-Workspace-ID"); id != "" {
			return id, nil
		}
		return "", nil
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(`{"error":"` + msg + `"}`))
}

// RequireWorkspaceMember resolves the workspace from slug (preferred) or UUID
// (fallback), validates membership, and injects the member and workspace ID
// into the request context.
func RequireWorkspaceMember(queries *db.Queries) func(http.Handler) http.Handler {
	return buildMiddleware(queries, resolveWorkspaceUUID(queries), nil, nil, nil)
}

// RequireWorkspaceRole is like RequireWorkspaceMember but additionally checks
// that the member has one of the specified roles.
func RequireWorkspaceRole(queries *db.Queries, roles ...string) func(http.Handler) http.Handler {
	return buildMiddleware(queries, resolveWorkspaceUUID(queries), roles, nil, nil)
}

// RequireWorkspaceMemberFromURL resolves the workspace ID from a chi URL
// parameter, validates membership, and injects into context.
func RequireWorkspaceMemberFromURL(queries *db.Queries, param string) func(http.Handler) http.Handler {
	return buildMiddleware(queries, func(r *http.Request) (string, error) {
		id := chi.URLParam(r, param)
		if id == "" {
			return "", nil
		}
		return id, nil
	}, nil, nil, nil)
}

// RequireWorkspaceRoleFromURL is like RequireWorkspaceMemberFromURL but
// additionally checks that the member has one of the specified roles.
func RequireWorkspaceRoleFromURL(queries *db.Queries, param string, roles ...string) func(http.Handler) http.Handler {
	return buildMiddleware(queries, func(r *http.Request) (string, error) {
		id := chi.URLParam(r, param)
		if id == "" {
			return "", nil
		}
		return id, nil
	}, roles, nil, nil)
}

type SuperAdminChecker func(*http.Request) bool
type SuperAdminReadGate func(*http.Request) bool

func RequireWorkspaceMemberWithSuperAdmin(queries *db.Queries, isSuperAdmin SuperAdminChecker) func(http.Handler) http.Handler {
	return RequireWorkspaceMemberWithSuperAdminReadGate(queries, isSuperAdmin, nil)
}

func RequireWorkspaceMemberWithSuperAdminReadGate(queries *db.Queries, isSuperAdmin SuperAdminChecker, allowRead SuperAdminReadGate) func(http.Handler) http.Handler {
	return buildMiddleware(queries, resolveWorkspaceUUID(queries), nil, isSuperAdmin, allowRead)
}

func RequireWorkspaceMemberFromURLWithSuperAdmin(queries *db.Queries, param string, isSuperAdmin SuperAdminChecker) func(http.Handler) http.Handler {
	return RequireWorkspaceMemberFromURLWithSuperAdminReadGate(queries, param, isSuperAdmin, nil)
}

func RequireWorkspaceMemberFromURLWithSuperAdminReadGate(queries *db.Queries, param string, isSuperAdmin SuperAdminChecker, allowRead SuperAdminReadGate) func(http.Handler) http.Handler {
	return buildMiddleware(queries, func(r *http.Request) (string, error) {
		id := chi.URLParam(r, param)
		if id == "" {
			return "", nil
		}
		return id, nil
	}, nil, isSuperAdmin, allowRead)
}

func buildMiddleware(queries *db.Queries, resolve workspaceResolver, roles []string, isSuperAdmin SuperAdminChecker, allowSuperAdminRead SuperAdminReadGate) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			workspaceID, resolveErr := resolve(r)
			if resolveErr != nil {
				writeError(w, http.StatusNotFound, "workspace not found")
				return
			}
			if workspaceID == "" {
				writeError(w, http.StatusBadRequest, "workspace_id or workspace_slug is required")
				return
			}

			// Final task-token binding check: even when the workspace
			// was resolved from a chi URL parameter
			// (RequireWorkspaceMemberFromURL), the agent must not be
			// allowed to operate on a workspace other than the one
			// stamped into its task token. This is the catch-all
			// behind resolveWorkspaceUUID's earlier check. MUL-2600.
			if r.Header.Get("X-Actor-Source") == "task_token" {
				bound := r.Header.Get("X-Workspace-ID")
				if bound == "" || workspaceID != bound {
					writeError(w, http.StatusForbidden, "task token is bound to a different workspace")
					return
				}
			}

			userID := r.Header.Get("X-User-ID")
			if userID == "" {
				writeError(w, http.StatusUnauthorized, "user not authenticated")
				return
			}

			userUUID, err := util.ParseUUID(userID)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "user not authenticated")
				return
			}
			wsUUID, err := util.ParseUUID(workspaceID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid workspace_id")
				return
			}
			member, err := queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
				UserID:      userUUID,
				WorkspaceID: wsUUID,
			})
			if err != nil {
				allowRead := allowSuperAdminRead == nil || allowSuperAdminRead(r)
				if len(roles) == 0 && isReadMethod(r.Method) && allowRead && isSuperAdmin != nil && isSuperAdmin(r) {
					ctx := SetSuperAdminReadContext(r.Context(), workspaceID, userUUID)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				writeError(w, http.StatusNotFound, "workspace not found")
				return
			}

			if len(roles) > 0 {
				allowed := false
				for _, role := range roles {
					if member.Role == role {
						allowed = true
						break
					}
				}
				if !allowed {
					writeError(w, http.StatusForbidden, "insufficient permissions")
					return
				}
			}

			ctx := SetMemberContext(r.Context(), workspaceID, member)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func isReadMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
