package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
)

func TestSuperAdminCanReadIssuesInForeignWorkspace(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	suffix := time.Now().UnixNano()
	adminEmail := fmt.Sprintf("super-admin-%d@multica.test", suffix)
	foreignSlug := fmt.Sprintf("super-admin-foreign-%d", suffix)

	var adminID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO "user" (name, email) VALUES ('Super Admin Test', $1) RETURNING id`,
		adminEmail,
	).Scan(&adminID); err != nil {
		t.Fatalf("create super admin user: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, adminID) })

	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Super Admin Foreign', $1, 'super admin read test', 'SAF')
		RETURNING id
	`, foreignSlug).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, description, status, priority,
			creator_type, creator_id, position, number
		)
		VALUES ($1, 'super admin visible issue', NULL, 'todo', 'none', 'member', $2, 0, 1)
		RETURNING id
	`, foreignWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create foreign issue: %v", err)
	}

	prevCfg := testHandler.cfg
	testHandler.cfg.SuperAdminEmails = []string{adminEmail}
	t.Cleanup(func() { testHandler.cfg = prevCfg })

	r := chi.NewRouter()
	r.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURLWithSuperAdmin(testHandler.Queries, "id", testHandler.IsSuperAdminRequest))
		r.Get("/", testHandler.GetWorkspace)
	})
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberWithSuperAdminReadGate(testHandler.Queries, testHandler.IsSuperAdminRequest, func(r *http.Request) bool {
			return r.URL.Path != "/api/skills"
		}))
		r.Get("/api/issues", testHandler.ListIssues)
		r.Get("/api/issues/{id}", testHandler.GetIssue)
		r.Post("/api/issues", testHandler.CreateIssue)
		r.Get("/api/skills", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/"+foreignWorkspaceID, nil)
	req.Header.Set("X-User-ID", adminID)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("super admin GetWorkspace: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/issues?workspace_id="+foreignWorkspaceID, nil)
	req.Header.Set("X-User-ID", adminID)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("super admin ListIssues foreign workspace: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Issues []IssueResponse `json:"issues"`
		Total  int64           `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listResp.Total != 1 || len(listResp.Issues) != 1 {
		t.Fatalf("expected one visible issue, got total=%d len=%d body=%s", listResp.Total, len(listResp.Issues), w.Body.String())
	}
	if listResp.Issues[0].Identifier != "SAF-1" {
		t.Fatalf("expected foreign workspace issue prefix SAF-1, got %q", listResp.Issues[0].Identifier)
	}

	homeTitle := fmt.Sprintf("super admin home visible %d", suffix)
	var homeIssueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, description, status, priority,
			creator_type, creator_id, position, number
		)
		VALUES ($1, $2, NULL, 'todo', 'none', 'member', $3, 0, 100001)
		RETURNING id
	`, testWorkspaceID, homeTitle, testUserID).Scan(&homeIssueID); err != nil {
		t.Fatalf("create home issue: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, homeIssueID) })

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/issues?workspace_id="+foreignWorkspaceID+"&all_workspaces=true&limit=100", nil)
	req.Header.Set("X-User-ID", adminID)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("super admin ListIssues all workspaces: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var allResp struct {
		Issues []IssueResponse `json:"issues"`
		Total  int64           `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&allResp); err != nil {
		t.Fatalf("decode all-workspaces response: %v", err)
	}
	foundForeign := false
	foundHome := false
	for _, issue := range allResp.Issues {
		switch issue.ID {
		case issueID:
			foundForeign = issue.WorkspaceID == foreignWorkspaceID && issue.Identifier == "SAF-1"
		case homeIssueID:
			foundHome = issue.WorkspaceID == testWorkspaceID
		}
	}
	if !foundForeign || !foundHome {
		t.Fatalf("expected all-workspaces list to include foreign=%t home=%t; got %+v", foundForeign, foundHome, allResp.Issues)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/issues?workspace_id="+testWorkspaceID+"&all_workspaces=true", nil)
	req.Header.Set("X-User-ID", testUserID)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("plain user all-workspaces ListIssues: expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/skills?workspace_id="+foreignWorkspaceID, nil)
	req.Header.Set("X-User-ID", adminID)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("super admin non-issue read without membership: expected 404, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/issues/"+issueID+"?workspace_id="+foreignWorkspaceID, nil)
	req.Header.Set("X-User-ID", adminID)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("super admin GetIssue foreign workspace: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/issues?workspace_id="+foreignWorkspaceID, nil)
	req.Header.Set("X-User-ID", testUserID)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("plain non-member ListIssues: expected 404, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/issues?workspace_id="+foreignWorkspaceID, nil)
	req.Header.Set("X-User-ID", adminID)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("super admin write without membership: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSuperAdminListWorkspacesReturnsAllWorkspaces(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	suffix := time.Now().UnixNano()
	adminEmail := fmt.Sprintf("super-admin-list-%d@multica.test", suffix)
	foreignSlug := fmt.Sprintf("super-admin-list-%d", suffix)

	var adminID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO "user" (name, email) VALUES ('Super Admin List Test', $1) RETURNING id`,
		adminEmail,
	).Scan(&adminID); err != nil {
		t.Fatalf("create super admin user: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, adminID) })

	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Super Admin List Foreign', $1, 'super admin workspace list test', 'SAL')
		RETURNING id
	`, foreignSlug).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})

	prevCfg := testHandler.cfg
	testHandler.cfg.SuperAdminEmails = []string{adminEmail}
	t.Cleanup(func() { testHandler.cfg = prevCfg })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	req.Header.Set("X-User-ID", adminID)
	testHandler.ListWorkspaces(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("super admin ListWorkspaces: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var workspaces []WorkspaceResponse
	if err := json.NewDecoder(w.Body).Decode(&workspaces); err != nil {
		t.Fatalf("decode workspace list: %v", err)
	}
	foundForeign := false
	for _, ws := range workspaces {
		if ws.ID == foreignWorkspaceID {
			foundForeign = true
			break
		}
	}
	if !foundForeign {
		t.Fatalf("expected super admin workspace list to include foreign workspace %s; got %+v", foreignWorkspaceID, workspaces)
	}
}
