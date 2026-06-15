package handler

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const verificationCodeViewerEnv = "MULTICA_VERIFICATION_CODE_VIEWER"

type VerificationCodeResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	Used      bool   `json:"used"`
	CreatedAt string `json:"created_at"`
	Attempts  int32  `json:"attempts"`
}

type ListVerificationCodesResponse struct {
	Codes []VerificationCodeResponse `json:"codes"`
}

func verificationCodeViewerEnabled() bool {
	raw := strings.TrimSpace(os.Getenv(verificationCodeViewerEnv))
	if raw != "" {
		switch strings.ToLower(raw) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}

	appEnv := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	switch appEnv {
	case "dev", "development", "local", "test":
		return true
	default:
		return false
	}
}

func verificationCodeToResponse(code db.VerificationCode) VerificationCodeResponse {
	return VerificationCodeResponse{
		ID:        uuidToString(code.ID),
		Email:     code.Email,
		Code:      code.Code,
		ExpiresAt: timestampToString(code.ExpiresAt),
		Used:      code.Used,
		CreatedAt: timestampToString(code.CreatedAt),
		Attempts:  code.Attempts,
	}
}

func parseVerificationCodeLimit(r *http.Request) int32 {
	const defaultLimit int32 = 20
	const maxLimit int32 = 100

	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultLimit
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return defaultLimit
	}
	if parsed > int(maxLimit) {
		return maxLimit
	}
	return int32(parsed)
}

func (h *Handler) ListVerificationCodes(w http.ResponseWriter, r *http.Request) {
	if !verificationCodeViewerEnabled() {
		writeError(w, http.StatusForbidden, "verification code viewer is disabled")
		return
	}

	codes, err := h.Queries.ListLatestVerificationCodes(r.Context(), parseVerificationCodeLimit(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list verification codes")
		return
	}

	resp := make([]VerificationCodeResponse, len(codes))
	for i, code := range codes {
		resp[i] = verificationCodeToResponse(code)
	}
	writeJSON(w, http.StatusOK, ListVerificationCodesResponse{Codes: resp})
}
