package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Zubimendi/vote-guard/internal/polls"
	"github.com/Zubimendi/vote-guard/internal/reconcile"
	"github.com/Zubimendi/vote-guard/internal/votes"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

type Server struct {
	adminKey  string
	polls     *polls.Service
	votes     *votes.Service
	reconcile *reconcile.Service
}

func New(adminKey string, pollSvc *polls.Service, voteSvc *votes.Service, recon *reconcile.Service) http.Handler {
	s := &Server{
		adminKey:  adminKey,
		polls:     pollSvc,
		votes:     voteSvc,
		reconcile: recon,
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/v1", func(r chi.Router) {
		r.With(s.requireAdmin).Post("/polls", s.createPoll)
		r.Route("/polls/{id}", func(r chi.Router) {
			r.With(s.requireAdmin).Post("/tokens", s.issueTokens)
			r.With(s.requireAdmin).Post("/close", s.closePoll)
			r.With(s.requireAdmin).Post("/reconcile", s.triggerReconcile)
			r.With(s.requireAdmin).Get("/reconciliation", s.listReconciliation)
			r.Post("/vote", s.castVote)
			r.Get("/results/live", s.liveResults)
			r.Get("/results/verified", s.verifiedResults)
		})
	})
	return r
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Admin-Key")
		if key == "" || key != s.adminKey {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid or missing X-Admin-Key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) createPoll(w http.ResponseWriter, r *http.Request) {
	var in polls.CreatePollInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	p, err := s.polls.Create(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) issueTokens(w http.ResponseWriter, r *http.Request) {
	pollID, err := parseID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid poll id")
		return
	}
	var body struct {
		IdentityRefs []string `json:"identity_refs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	tokens, err := s.polls.IssueTokens(r.Context(), pollID, body.IdentityRefs)
	if errors.Is(err, polls.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "poll not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"poll_id": pollID,
		"tokens":  tokens,
	})
}

func (s *Server) closePoll(w http.ResponseWriter, r *http.Request) {
	pollID, err := parseID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid poll id")
		return
	}
	p, err := s.polls.Close(r.Context(), pollID)
	if errors.Is(err, polls.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "poll not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) castVote(w http.ResponseWriter, r *http.Request) {
	pollID, err := parseID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid poll id")
		return
	}
	var body struct {
		Token    string `json:"token"`
		OptionID string `json:"option_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	optionID, err := uuid.Parse(body.OptionID)
	if err != nil || body.Token == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "token and option_id required")
		return
	}

	result, err := s.votes.CastRedeem(r.Context(), pollID, optionID, body.Token)
	switch {
	case errors.Is(err, votes.ErrInvalidToken):
		writeErr(w, http.StatusUnauthorized, "invalid_token", "token is invalid for this poll")
	case errors.Is(err, votes.ErrAlreadyUsed):
		writeErr(w, http.StatusConflict, "already_used", "token already redeemed")
	case errors.Is(err, votes.ErrPollNotOpen):
		writeErr(w, http.StatusConflict, "poll_not_open", "poll is not open for voting")
	case errors.Is(err, votes.ErrBadOption):
		writeErr(w, http.StatusBadRequest, "bad_option", "option does not belong to this poll")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
	default:
		writeJSON(w, http.StatusCreated, result)
	}
}

func (s *Server) liveResults(w http.ResponseWriter, r *http.Request) {
	pollID, err := parseID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid poll id")
		return
	}
	res, err := s.polls.LiveResults(r.Context(), pollID)
	if errors.Is(err, polls.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "poll not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) verifiedResults(w http.ResponseWriter, r *http.Request) {
	pollID, err := parseID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid poll id")
		return
	}
	res, err := s.polls.VerifiedResults(r.Context(), pollID)
	if errors.Is(err, polls.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "poll not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) listReconciliation(w http.ResponseWriter, r *http.Request) {
	pollID, err := parseID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid poll id")
		return
	}
	runs, err := s.polls.ListReconciliation(r.Context(), pollID)
	if errors.Is(err, polls.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "poll not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"poll_id": pollID, "runs": runs})
}

func (s *Server) triggerReconcile(w http.ResponseWriter, r *http.Request) {
	pollID, err := parseID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid poll id")
		return
	}
	if err := s.reconcile.RunPoll(r.Context(), pollID); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	runs, err := s.polls.ListReconciliation(r.Context(), pollID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"poll_id": pollID, "runs": runs})
}

func parseID(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, "id"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"error":   code,
		"message": message,
	})
}
