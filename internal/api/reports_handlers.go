package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/webex"
)

// poster delivers digests; tests replace it.
func (s *Server) poster() finops.Poster {
	if s.Poster != nil {
		return s.Poster
	}
	return &webex.Notifier{Client: s.Client}
}

// handleListReports lists stored reports with the digest schedule.
func (s *Server) handleListReports(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := config.OperatorNamespace()
	reports, err := finops.ListReports(ctx, s.Client, ns)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	sched := cfg.Spec.Reports.Digest
	out := map[string]any{"reports": reports, "schedule": sched}
	wx := cfg.Spec.Integrations.Messenger
	out["webexReady"] = wx != nil && wx.Webex != nil && wx.Webex.Enabled && wx.Webex.RoomID != ""
	if last := finops.DigestLastSent(ctx, s.Client, ns); !last.IsZero() {
		out["lastSent"] = last
	}
	if sched.Enabled {
		from := time.Now()
		if last := finops.DigestLastSent(ctx, s.Client, ns); last.After(from) {
			from = last
		}
		if next, err := finops.NextDigest(sched, from); err == nil {
			out["nextDue"] = next
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetReport(w http.ResponseWriter, r *http.Request) {
	entry, md, err := finops.GetReport(r.Context(), s.Client, config.OperatorNamespace(), r.PathValue("id"))
	if errors.Is(err, finops.ErrReportNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": entry, "markdown": md})
}

func (s *Server) handleDeleteReport(w http.ResponseWriter, r *http.Request) {
	err := finops.DeleteReport(r.Context(), s.Client, config.OperatorNamespace(), r.PathValue("id"))
	if errors.Is(err, finops.ErrReportNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeK8sError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// digest builds the digest of the last complete week or month.
func (s *Server) digest(r *http.Request) (finops.Digest, error) {
	ctx := r.Context()
	overview, err := s.finopsOverview(ctx)
	if err != nil {
		return finops.Digest{}, err
	}
	ledger, err := s.costLedger(ctx)
	if err != nil {
		return finops.Digest{}, err
	}
	return finops.NewDigest(overview, ledger, r.URL.Query().Get("period"), time.Now()), nil
}

// handleDigest serves the digest of the last complete week or month (?period=week|month).
func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	d, err := s.digest(r)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"digest": d, "markdown": d.Markdown()})
}

// handleSendDigest posts the digest to the Webex space now and keeps it in the history.
func (s *Server) handleSendDigest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Period string `json:"period"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	q := r.URL.Query()
	q.Set("period", req.Period)
	r.URL.RawQuery = q.Encode()
	d, err := s.digest(r)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	md := d.Markdown()
	if err := s.poster().Post(r.Context(), md); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, webex.ErrNoSpace) {
			status = http.StatusBadRequest
		}
		writeError(w, status, "Could not post the digest: "+err.Error())
		return
	}
	by := ""
	if id := auth.FromContext(r.Context()); id != nil {
		by = id.Name
	}
	entry, err := finops.SaveReport(r.Context(), s.Client, config.OperatorNamespace(), finops.ReportEntry{
		Kind: finops.ReportDigest, Title: d.Title, Period: d.Period, By: by, Delivered: "webex",
	}, md)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "The digest was posted but could not be saved: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entry)
}
