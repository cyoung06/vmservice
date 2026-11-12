// Package api is the HTTP API under /api/ (docs/api.md): the signed-in member's machines.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cyoung06/vmservice/internal/auth"
	"github.com/cyoung06/vmservice/internal/config"
	"github.com/cyoung06/vmservice/internal/console"
	"github.com/cyoung06/vmservice/internal/limit"
	"github.com/cyoung06/vmservice/internal/machines"
)

// API serves /api/.
type API struct {
	auth     *auth.Auth
	machines *machines.Service
	console  *console.Proxy
	origin   string
	streams  *limit.PerOwner
	log      *slog.Logger
}

// New is the API for origin (the public origin, no trailing slash; writes and consoles from another
// Origin are refused). streams caps each member's open event streams, consoles and SSH sessions.
func New(a *auth.Auth, m *machines.Service, c *console.Proxy, origin string, streams *limit.PerOwner, log *slog.Logger) *API {
	return &API{auth: a, machines: m, console: c, origin: origin, streams: streams, log: log}
}

func (a *API) tooMany(w http.ResponseWriter) {
	reply(w, http.StatusTooManyRequests, detail(fmt.Sprintf("You have %d consoles and live views open; close some first.", a.streams.Max)))
}

// Routes adds the API's endpoints to mux.
func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/session", a.read(a.session))
	mux.HandleFunc("GET /api/catalog", a.read(a.catalog))
	mux.HandleFunc("GET /api/vms", a.read(a.list))
	mux.HandleFunc("GET /api/vms/events", a.events)
	mux.HandleFunc("GET /api/vms/{id}", a.read(a.get))
	mux.HandleFunc("POST /api/vms", a.write(a.create))
	mux.HandleFunc("POST /api/vms/{id}/{action}", a.write(a.action))
	mux.HandleFunc("DELETE /api/vms/{id}", a.write(a.remove))
	mux.HandleFunc("GET /api/media", a.read(a.media))
	mux.HandleFunc("GET /api/media/{id}", a.read(a.medium))
	mux.HandleFunc("POST /api/media", a.write(a.addMedium))
	mux.HandleFunc("PUT /api/media/{id}/upload", noDeadlines(a.write(a.upload)))
	mux.HandleFunc("DELETE /api/media/{id}", a.write(a.deleteMedium))
	mux.HandleFunc("GET /api/keys", a.read(a.keys))
	mux.HandleFunc("POST /api/keys", a.write(a.addKey))
	mux.HandleFunc("DELETE /api/keys/{id}", a.write(a.deleteKey))
	mux.HandleFunc("GET /api/vms/{id}/serial", a.consoleOf("console"))
	mux.HandleFunc("GET /api/vms/{id}/vnc", a.consoleOf("vnc"))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusNotFound, detail("No such endpoint."))
	})
}

type handler func(r *http.Request, s auth.Session) (status int, body any, err error)

func detail(message string) map[string]string { return map[string]string{"detail": message} }

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if body != nil {
		json.NewEncoder(w).Encode(body)
	}
}

func (a *API) fail(w http.ResponseWriter, err error) {
	var e *machines.Error
	if errors.As(err, &e) {
		reply(w, e.Status, detail(e.Message))
		return
	}
	a.log.Error("api", "err", err)
	reply(w, http.StatusInternalServerError, detail("Something went wrong on the server."))
}

func (a *API) serve(w http.ResponseWriter, r *http.Request, h handler) {
	s, ok := a.auth.Session(r)
	if !ok {
		reply(w, http.StatusUnauthorized, detail("Sign in first."))
		return
	}
	status, body, err := h(r, s)
	if err != nil {
		a.fail(w, err)
		return
	}
	reply(w, status, body)
}

func (a *API) read(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { a.serve(w, r, h) }
}

// sameOrigin is the CSRF guard: X-Requested-With can't be sent cross-origin without a CORS
// preflight this server never answers, and an Origin, when there is one, must be ours.
func (a *API) sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" && o != a.origin {
		return false
	}
	return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

func (a *API) write(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" || !a.sameOrigin(r) {
			reply(w, http.StatusForbidden, detail("Cross-origin requests are refused."))
			return
		}
		a.serve(w, r, h)
	}
}

func (a *API) session(_ *http.Request, s auth.Session) (int, any, error) {
	return http.StatusOK, map[string]any{"username": s.Username, "expires": s.Expires.UTC()}, nil
}

func (a *API) catalog(_ *http.Request, _ auth.Session) (int, any, error) {
	c := a.machines.Catalog
	images := make([]map[string]string, 0, len(c.Images))
	for _, i := range c.Images {
		images = append(images, map[string]string{"id": i.ID, "name": i.Name, "defaultUser": i.DefaultUser})
	}
	return http.StatusOK, map[string]any{"images": images, "sizes": c.Sizes, "disk": c.Disk, "limits": c.Limits}, nil
}

func (a *API) list(r *http.Request, s auth.Session) (int, any, error) {
	vms, err := a.machines.List(r.Context(), s.Owner)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"vms": vms}, nil
}

func (a *API) get(r *http.Request, s auth.Session) (int, any, error) {
	m, err := a.machines.Get(r.Context(), s.Owner, r.PathValue("id"))
	return http.StatusOK, m, err
}

func bad(format string, args ...any) error {
	return &machines.Error{Status: http.StatusBadRequest, Message: fmt.Sprintf(format, args...)}
}

type createBody struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	Media    string `json:"media"`
	Profile  string `json:"profile"`
	Size     string `json:"size"`
	DiskGiB  int    `json:"diskGiB"`
	Password string `json:"password"`
}

var control = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func (a *API) create(r *http.Request, s auth.Session) (int, any, error) {
	var b createBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return 0, nil, bad("The request isn't valid JSON for a new machine.")
	}
	c := a.machines.Catalog
	b.Name = strings.TrimSpace(b.Name)
	if n := utf8.RuneCountInString(b.Name); n < 1 || n > 63 || control.MatchString(b.Name) {
		return 0, nil, bad("The name must be 1 to 63 characters.")
	}
	img, ok := c.Image(b.Image)
	if !ok && b.Media == "" {
		return 0, nil, bad("There is no image %q.", b.Image)
	}
	size, ok := c.Size(b.Size)
	if !ok {
		return 0, nil, bad("There is no size %q.", b.Size)
	}
	if b.DiskGiB < c.Disk.MinGiB || b.DiskGiB > c.Disk.MaxGiB {
		return 0, nil, bad("The disk must be %d to %d GiB.", c.Disk.MinGiB, c.Disk.MaxGiB)
	}
	// Optional: without one the default user has no password (SSH keys only); Create refuses that
	// when the member has no keys either.
	if n := utf8.RuneCountInString(b.Password); b.Password != "" && (n < 8 || n > 128 || control.MatchString(b.Password)) {
		return 0, nil, bad("The password must be 8 to 128 characters, without control characters.")
	}
	if err := a.machines.EnsureMember(r.Context(), s.Owner, s.Username); err != nil {
		return 0, nil, err
	}
	var hash string
	if b.Password != "" {
		var err error
		if hash, err = machines.HashPassword(b.Password); err != nil {
			return 0, nil, err
		}
	}
	if b.Media != "" {
		img = config.Image{} // the medium decides
	}
	m, err := a.machines.Create(r.Context(), s.Owner, machines.Request{Name: b.Name, OwnerName: s.Username, Image: img, Size: size, DiskGiB: b.DiskGiB, PasswordHash: hash,
		MediaID: b.Media, ProfileOverride: b.Profile})
	return http.StatusCreated, m, err
}

func (a *API) media(r *http.Request, s auth.Session) (int, any, error) {
	list, err := a.machines.Media(r.Context(), s.Owner)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"media": list}, nil
}

func (a *API) medium(r *http.Request, s auth.Session) (int, any, error) {
	m, err := a.machines.GetMedium(r.Context(), s.Owner, r.PathValue("id"))
	return http.StatusOK, m, err
}

func (a *API) addMedium(r *http.Request, s auth.Session) (int, any, error) {
	var b struct {
		Name      string `json:"name"`
		Source    string `json:"source"`
		SizeBytes int64  `json:"sizeBytes"`
		Header    []byte `json:"header"` // base64 in JSON
		URL       string `json:"url"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return 0, nil, bad("The request isn't valid JSON for a medium.")
	}
	if err := a.machines.EnsureMember(r.Context(), s.Owner, s.Username); err != nil {
		return 0, nil, err
	}
	var m machines.Medium
	var err error
	switch b.Source {
	case "upload":
		m, err = a.machines.AddUpload(r.Context(), s.Owner, b.Name, b.SizeBytes, b.Header)
	case "url":
		m, err = a.machines.AddURL(r.Context(), s.Owner, b.Name, b.URL)
	default:
		return 0, nil, bad(`The source must be "upload" or "url".`)
	}
	return http.StatusCreated, m, err
}

// upload streams the file in the body to the medium; it takes as long as the upload does.
func (a *API) upload(r *http.Request, s auth.Session) (int, any, error) {
	if r.ContentLength <= 0 {
		return 0, nil, bad("Send the file as the body, with its Content-Length.")
	}
	return http.StatusNoContent, nil, a.machines.Upload(r.Context(), s.Owner, r.PathValue("id"), r.Body, r.ContentLength)
}

func (a *API) deleteMedium(r *http.Request, s auth.Session) (int, any, error) {
	return http.StatusNoContent, nil, a.machines.DeleteMedium(r.Context(), s.Owner, r.PathValue("id"))
}

func (a *API) keys(r *http.Request, s auth.Session) (int, any, error) {
	keys, err := a.machines.Keys(r.Context(), s.Owner)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"keys": keys}, nil
}

func (a *API) addKey(r *http.Request, s auth.Session) (int, any, error) {
	var b struct {
		PublicKey string `json:"publicKey"`
		Label     string `json:"label"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return 0, nil, bad("The request isn't valid JSON for a key.")
	}
	if err := a.machines.EnsureMember(r.Context(), s.Owner, s.Username); err != nil {
		return 0, nil, err
	}
	k, err := a.machines.AddKey(r.Context(), s.Owner, b.PublicKey, b.Label)
	return http.StatusCreated, k, err
}

func (a *API) deleteKey(r *http.Request, s auth.Session) (int, any, error) {
	return http.StatusNoContent, nil, a.machines.DeleteKey(r.Context(), s.Owner, r.PathValue("id"))
}

func (a *API) action(r *http.Request, s auth.Session) (int, any, error) {
	switch act := r.PathValue("action"); act {
	case "start", "stop", "restart":
		return http.StatusNoContent, nil, a.machines.Action(r.Context(), s.Owner, r.PathValue("id"), act)
	case "eject":
		return http.StatusNoContent, nil, a.machines.Eject(r.Context(), s.Owner, r.PathValue("id"))
	default:
		return 0, nil, &machines.Error{Status: http.StatusNotFound, Message: "No such action."}
	}
}

func (a *API) remove(r *http.Request, s auth.Session) (int, any, error) {
	return http.StatusNoContent, nil, a.machines.Delete(r.Context(), s.Owner, r.PathValue("id"))
}

// events is the server-sent event stream of the member's machines: a snapshot, then changes as the
// informer sees them, until the session expires or the client goes away.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	s, ok := a.auth.Session(r)
	if !ok {
		reply(w, http.StatusUnauthorized, detail("Sign in first."))
		return
	}
	release, ok := a.streams.Take(s.Owner)
	if !ok {
		a.tooMany(w)
		return
	}
	defer release()
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Time{}) // a stream, not a request: the server's timeouts don't apply
	rc.SetWriteDeadline(time.Time{})
	ctx, cancel := context.WithDeadline(r.Context(), s.Expires)
	defer cancel()

	// Subscribe before taking the snapshot, so nothing between the two is missed (a change seen in
	// both is sent twice, which the client treats as the same update).
	ch, unsubscribe := a.machines.Subscribe(s.Owner)
	defer unsubscribe()
	vms, err := a.machines.List(ctx, s.Owner)
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(kind string, data any) bool {
		body, _ := json.Marshal(data)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, body); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send("snapshot", map[string]any{"vms": vms}) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case e, open := <-ch:
			if !open || !send(e.Kind, e.Data) {
				return
			}
		}
	}
}

// consoleOf serves a machine's serial ("console") or VNC ("vnc") websocket.
func (a *API) consoleOf(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := a.auth.Session(r)
		if !ok {
			reply(w, http.StatusUnauthorized, detail("Sign in first."))
			return
		}
		// Browsers send cookies on cross-site websockets too, and always send Origin on them: it must be ours.
		if r.Header.Get("Origin") != a.origin {
			reply(w, http.StatusForbidden, detail("Cross-origin requests are refused."))
			return
		}
		if !console.IsUpgrade(r) {
			reply(w, http.StatusBadRequest, detail("This is a websocket with the subprotocol "+console.Subprotocol+"."))
			return
		}
		id := r.PathValue("id")
		running, err := a.machines.Running(r.Context(), s.Owner, id)
		if err != nil {
			a.fail(w, err)
			return
		}
		if !running {
			reply(w, http.StatusConflict, detail("The machine isn't running."))
			return
		}
		release, ok := a.streams.Take(s.Owner)
		if !ok {
			a.tooMany(w)
			return
		}
		defer release()
		path := fmt.Sprintf("/apis/subresources.kubevirt.io/v1/namespaces/%s/virtualmachineinstances/%s/%s", a.machines.Namespace, id, kind)
		err = a.console.Serve(r.Context(), w, r, path, s.Expires)
		var up *console.UpstreamError
		var hijacked *console.Hijacked
		switch {
		case errors.As(err, &hijacked):
			a.log.Info("console ended", "id", id, "kind", kind, "err", err) // nothing more can be written
		case errors.As(err, &up):
			a.log.Warn("console refused", "id", id, "kind", kind, "status", up.Status, "body", up.Body)
			reply(w, http.StatusBadGateway, detail("The cluster refused the console."))
		case err != nil:
			a.log.Warn("console", "id", id, "kind", kind, "err", err)
			reply(w, http.StatusBadGateway, detail("The console couldn't be opened."))
		}
	}
}

// noDeadlines lifts the server's read and write timeouts for one long request (an upload of
// several GB).
func noDeadlines(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		rc.SetReadDeadline(time.Time{})
		rc.SetWriteDeadline(time.Time{})
		h(w, r)
	}
}
