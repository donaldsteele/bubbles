package httpapi

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/files"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// multipartOverhead is what the body of an upload may exceed upload_max_bytes
// by: part headers, boundaries and form fields around the file.
const multipartOverhead = 1 << 20

// registerFileRoutes adds the file upload and serving endpoints. Both need a
// signed-in user or an app token (uploads need files:write).
func registerFileRoutes(mux *http.ServeMux, d Deps) {
	a := &api{auth: d.Auth, files: d.Files, logger: d.Logger}
	route := appRoute(d)
	mux.Handle("POST "+apiPrefix+"/files", route(a.uploadFile))
	mux.Handle("GET "+apiPrefix+"/files/{id}/{name}", route(a.serveFile))
}

// uploadFile answers POST /files: one file in the multipart field "file",
// streamed to disk, never held in memory.
func (a *api) uploadFile(w http.ResponseWriter, r *http.Request) {
	if !requireScope(w, r, protocol.ScopeFilesWrite) {
		return
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		WriteError(w, http.StatusUnsupportedMediaType, CodeUnsupportedMedia, "Content-Type must be multipart/form-data")
		return
	}
	user := auth.UserFrom(r.Context())
	release, err := a.files.Begin(user.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer release()

	limit := a.files.MaxUploadBytes()
	if r.ContentLength > limit+multipartOverhead {
		a.fail(w, r, &files.TooLargeError{Limit: limit})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+multipartOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "the request body is not valid multipart/form-data")
		return
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
				map[string]string{"file": "a file in the form field \"file\" is required"})
			return
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				a.fail(w, r, &files.TooLargeError{Limit: limit})
				return
			}
			WriteError(w, http.StatusBadRequest, CodeBadRequest, "the request body is not valid multipart/form-data")
			return
		}
		if part.FormName() != "file" {
			continue
		}
		f, err := a.files.Upload(r.Context(), files.Upload{
			UserID: user.ID, Name: part.FileName(), Body: part, SizeHint: max(r.ContentLength, 0),
		})
		if err != nil {
			a.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, f)
		return
	}
}

// serveFile answers GET /files/{id}/{name}. The name in the URL is cosmetic:
// the file is found by id and named from the database.
func (a *api) serveFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := a.files.Open(r.Context(), actorFrom(r), r.PathValue("id"), files.OpenOptions{
		Thumb: q.Get("thumb") == "1", Download: q.Get("download") == "1",
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer c.Close()
	files.Serve(w, r, c)
}

// tooLargeMessage is the client text of a 413 from the upload path.
func tooLargeMessage(e *files.TooLargeError) string {
	if e.Quota {
		return "your storage quota is used up"
	}
	return fmt.Sprintf("the file is larger than the upload limit of %d bytes", e.Limit)
}
