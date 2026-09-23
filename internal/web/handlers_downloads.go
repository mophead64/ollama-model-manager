package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/downloads"
	"github.com/mophead64/ollama-model-manager/internal/store"
)

const historyPageSize = 25

// downloadRow pairs a queued/running download with its live progress (only
// the running one has any).
type downloadRow struct {
	store.Download
	Position int // 1-based place in the queue; 0 for the running download
	Live     *downloads.Live
}

// historyRow is a finished download plus the title of the first suggestion
// for fixing it, if it failed.
type historyRow struct {
	store.Download
	Hint string
}

// downloadsData gathers everything the downloads panel shows. It's rendered
// both as the full page and as the fragment htmx polls to keep it live.
func (s *Server) downloadsData(r *http.Request) (map[string]any, error) {
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	active, err := s.st.ActiveDownloads(r.Context())
	if err != nil {
		return nil, err
	}
	live := s.dl.Live()
	rows := make([]downloadRow, len(active))
	pos := 0
	for i, d := range active {
		rows[i] = downloadRow{Download: d}
		if d.Status == store.DownloadDownloading {
			if live != nil && live.ID == d.ID {
				rows[i].Live = live
			}
		} else {
			pos++
			rows[i].Position = pos
		}
	}

	finished, total, err := s.st.FinishedDownloads(r.Context(), page, historyPageSize)
	if err != nil {
		return nil, err
	}
	history := make([]historyRow, len(finished))
	for i, d := range finished {
		history[i] = historyRow{Download: d}
		if d.Error != "" {
			if hs := downloads.Suggest(d.Model, d.Error, nil, ""); len(hs) > 0 {
				history[i].Hint = hs[0].Title
			}
		}
	}
	totalPages := max(1, (total+historyPageSize-1)/historyPageSize)

	return map[string]any{
		"Active":       rows,
		"History":      history,
		"HistoryTotal": total,
		"Page":         page,
		"TotalPages":   totalPages,
		// Poll quickly while something's happening, lazily otherwise (to pick
		// up downloads queued from another browser).
		"PollEvery": map[bool]string{true: "2s", false: "15s"}[len(active) > 0],
	}, nil
}

func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	data, err := s.downloadsData(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		data["Fragment"] = true // also refresh the nav badge, out of band
		s.render(w, r, "downloads_panel.html", data)
		return
	}
	q := r.URL.Query()
	data["Queued"] = q.Get("queued")
	data["Warning"] = q.Get("warning")
	s.render(w, r, "downloads.html", data)
}

func (s *Server) handleQueueDownload(w http.ResponseWriter, r *http.Request) {
	input := r.FormValue("model")
	_, name, warning, err := s.dl.Enqueue(r.Context(), input, currentUser(r).Username)
	if err != nil {
		data, derr := s.downloadsData(r)
		if derr != nil {
			s.serverError(w, r, derr)
			return
		}
		msg := err.Error() // already phrased for display; starts with the model name
		if errors.Is(err, store.ErrAlreadyQueued) {
			msg = name + " is already queued or downloading."
		}
		data["FormError"] = msg
		data["FormValue"] = input
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "downloads.html", data)
		return
	}
	s.log.Info("download queued", "model", name, "by", currentUser(r).Username)
	v := url.Values{"queued": {name}}
	if warning != "" {
		v.Set("warning", warning)
	}
	http.Redirect(w, r, "/downloads?"+v.Encode(), http.StatusSeeOther)
}

// downloadAction runs one of the per-download buttons, then returns to
// wherever the button was (the list or the download's own page).
func (s *Server) downloadAction(w http.ResponseWriter, r *http.Request, act func(id int64) error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.badRequest(w, r, errMsg("bad download id"))
		return
	}
	if err := act(id); err != nil {
		if errors.Is(err, store.ErrAlreadyQueued) {
			s.badRequest(w, r, errMsg("That model is already queued or downloading."))
			return
		}
		s.serverError(w, r, err)
		return
	}
	if r.FormValue("from") == "detail" {
		http.Redirect(w, r, fmt.Sprintf("/downloads/%d", id), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/downloads", http.StatusSeeOther)
}

func (s *Server) handleCancelDownload(w http.ResponseWriter, r *http.Request) {
	s.downloadAction(w, r, func(id int64) error { return s.dl.Cancel(r.Context(), id) })
}

// handleRetryDownload requeues a failed/cancelled download, optionally under a
// corrected model name from the retry dialog. If the new name is rejected, the
// page it came from is re-rendered with the dialog reopened on the error.
func (s *Server) handleRetryDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.badRequest(w, r, errMsg("bad download id"))
		return
	}
	input := r.FormValue("model")
	fromDetail := r.FormValue("from") == "detail"
	name, warning, err := s.dl.Retry(r.Context(), id, currentUser(r).Username, input)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, store.ErrAlreadyQueued) {
			msg = name + " is already queued or downloading."
		} else if name == "" && input == "" {
			s.serverError(w, r, err)
			return
		}
		retry := map[string]any{"ID": id, "Model": input, "Error": msg}
		w.WriteHeader(http.StatusBadRequest)
		if fromDetail {
			s.renderDownloadDetail(w, r, id, retry)
			return
		}
		data, derr := s.downloadsData(r)
		if derr != nil {
			s.serverError(w, r, derr)
			return
		}
		data["Retry"] = retry
		s.render(w, r, "downloads.html", data)
		return
	}
	if fromDetail {
		http.Redirect(w, r, fmt.Sprintf("/downloads/%d", id), http.StatusSeeOther)
		return
	}
	v := url.Values{"queued": {name}}
	if warning != "" {
		v.Set("warning", warning)
	}
	http.Redirect(w, r, "/downloads?"+v.Encode(), http.StatusSeeOther)
}

func (s *Server) handleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.badRequest(w, r, errMsg("bad download id"))
		return
	}
	if err := s.st.DeleteDownload(r.Context(), id); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/downloads", http.StatusSeeOther)
}

func (s *Server) handleClearDownloads(w http.ResponseWriter, r *http.Request) {
	if _, err := s.st.ClearFinishedDownloads(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/downloads", http.StatusSeeOther)
}

func (s *Server) handleDownloadDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.badRequest(w, r, errMsg("bad download id"))
		return
	}
	s.renderDownloadDetail(w, r, id, nil)
}

// renderDownloadDetail shows one download and its log. retry, when set, is a
// rejected retry to reopen the dialog with (ID, Model, Error).
func (s *Server) renderDownloadDetail(w http.ResponseWriter, r *http.Request, id int64, retry map[string]any) {
	d, err := s.st.GetDownload(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if d == nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "download_detail.html", map[string]any{"NotFound": true})
		return
	}
	logs, err := s.st.DownloadLogs(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := map[string]any{"D": d, "Logs": logs, "Retry": retry}
	if d.Status != store.DownloadCompleted {
		var warnings []string
		for _, l := range logs {
			if l.Level == "warn" {
				warnings = append(warnings, l.Message)
			}
		}
		if d.Error != "" || len(warnings) > 0 {
			version := ""
			if d.Error != "" {
				vctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
				version, _ = s.ol.Version(vctx)
				cancel()
			}
			data["Hints"] = downloads.Suggest(d.Model, d.Error, warnings, version)
		}
	}
	if l := s.dl.Live(); l != nil && l.ID == id {
		data["Live"] = l
	}
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, r, "download_detail_body.html", data)
		return
	}
	s.render(w, r, "download_detail.html", data)
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "error", err)
	w.WriteHeader(http.StatusInternalServerError)
	s.render(w, r, "error_fragment.html", map[string]any{"Error": "Something went wrong; see the app log."})
}
