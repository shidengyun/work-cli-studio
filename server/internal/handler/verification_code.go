package handler

import (
	"net/http"
	"strconv"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
