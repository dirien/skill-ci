package reportsite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
	"github.com/dirien/skill-ci/controller/internal/session"
)

// Cache-Control values of the API (CONTRACT.md, "Reporting site").
const (
	cacheNoStore = "no-store"
	cacheSession = "private, max-age=300"
	// refreshSeconds is how often a page with a pending or running run polls.
	refreshSeconds = 15
	// maxParamLength bounds a free-text query parameter.
	maxParamLength = 256
)

var (
	// runName is a Kubernetes object name (DNS subdomain).
	runName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	// jobName is a Job name (DNS label).
	jobName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	// skillName is a Harbor repository name inside the skills project.
	skillName = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	// digestPattern is the only digest form the site passes to the registry.
	digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// apiError is an API failure: the status, code and sentence of an Error body.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.message }

func badRequest(format string, args ...any) *apiError {
	return &apiError{http.StatusBadRequest, apiv1.CodeBadRequest, fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) *apiError {
	return &apiError{http.StatusNotFound, apiv1.CodeNotFound, fmt.Sprintf(format, args...)}
}

func withheld(format string, args ...any) *apiError {
	return &apiError{http.StatusNotFound, apiv1.CodeWithheld, fmt.Sprintf(format, args...)}
}

func registryError(format string, args ...any) *apiError {
	return &apiError{http.StatusBadGateway, apiv1.CodeRegistry, fmt.Sprintf(format, args...)}
}

func unavailable(format string, args ...any) *apiError {
	return &apiError{http.StatusServiceUnavailable, apiv1.CodeUnavailable, fmt.Sprintf(format, args...)}
}

func internalError(format string, args ...any) *apiError {
	return &apiError{http.StatusInternalServerError, apiv1.CodeInternal, fmt.Sprintf(format, args...)}
}

// writeErr writes e as an Error body.
func writeErr(w http.ResponseWriter, e *apiError) {
	apiv1.WriteError(w, e.status, e.code, e.message)
}

// apiRoutes registers the JSON API under /api/v1/. The API is read-only: the
// mux answers other methods with 405 because every pattern is GET (which
// also matches HEAD), and unknown API paths get a JSON 404, never the app.
func (s *Server) apiRoutes() {
	p := "GET " + apiv1.Prefix
	s.mux.HandleFunc(p+"/site", s.apiSite)
	s.mux.HandleFunc(p+"/overview", s.apiOverview)
	s.mux.HandleFunc(p+"/runs", s.apiRuns)
	s.mux.HandleFunc(p+"/runs/{name}", s.apiRun)
	s.mux.HandleFunc(p+"/runs/{name}/trials/{trial}", s.apiTrial)
	s.mux.HandleFunc(p+"/runs/{name}/trials/{trial}/steps/{step}/output/{k}", s.apiOutput)
	s.mux.HandleFunc(p+"/runs/{name}/trials/{trial}/trajectory.json", s.apiTrajectory)
	s.mux.HandleFunc(p+"/runs/{name}/cases/{case}/compare", s.apiCompare)
	s.mux.HandleFunc(p+"/skills", s.apiSkills)
	s.mux.HandleFunc(p+"/skills/{skill}", s.apiSkill)
	s.mux.HandleFunc(p+"/models", s.apiModels)
	s.mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, notFound("There is no API route at %s.", r.URL.Path))
	})
}

// checkQuery fails on a query parameter the route does not take, or one given twice.
func checkQuery(q url.Values, allowed ...string) *apiError {
	for key, values := range q {
		if !contains(allowed, key) {
			if len(allowed) == 0 {
				return badRequest("This route takes no query parameters, got %q.", key)
			}
			return badRequest("Unknown query parameter %q; this route takes %s.", key, strings.Join(allowed, ", "))
		}
		if len(values) > 1 {
			return badRequest("Query parameter %q is given %d times.", key, len(values))
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// textParam returns a free-text query parameter after checking its length and characters.
func textParam(q url.Values, key string) (string, *apiError) {
	v := q.Get(key)
	if len(v) > maxParamLength {
		return "", badRequest("Query parameter %q is longer than %d bytes.", key, maxParamLength)
	}
	for _, c := range v {
		if unicode.IsControl(c) || c == unicode.ReplacementChar {
			return "", badRequest("Query parameter %q has a control character or invalid UTF-8.", key)
		}
	}
	return v, nil
}

// enumParam returns a query parameter that must be one of values (or empty).
func enumParam(q url.Values, key string, values []string) (string, *apiError) {
	v := q.Get(key)
	if v != "" && !contains(values, v) {
		return "", badRequest("Query parameter %q must be one of %s, got %q.", key, strings.Join(values, ", "), v)
	}
	return v, nil
}

// intParam returns a query parameter as an integer in [lo, hi]; def when absent.
func intParam(q url.Values, key string, lo, hi, def int) (int, *apiError) {
	v := q.Get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi || v != strconv.Itoa(n) {
		return 0, badRequest("Query parameter %q must be a whole number from %d to %d, got %q.", key, lo, hi, v)
	}
	return n, nil
}

// pathName returns the {name} path value after checking it is an object name.
func pathName(r *http.Request) (string, *apiError) {
	name := r.PathValue("name")
	if len(name) > 253 || !runName.MatchString(name) {
		return "", badRequest("%q is not a valid run name.", name)
	}
	return name, nil
}

// pathID returns a path value that must be a trial or case id.
func pathID(r *http.Request, key, what string) (string, *apiError) {
	v := r.PathValue(key)
	if !session.TrialID.MatchString(v) {
		return "", badRequest("%q is not a valid %s id: it must match %s.", v, what, session.TrialID.String())
	}
	return v, nil
}

// getRun reads one SkillEval.
func (s *Server) getRun(ctx context.Context, name string) (*v1alpha1.SkillEval, *apiError) {
	var se v1alpha1.SkillEval
	err := s.cfg.Reader.Get(ctx, client.ObjectKey{Namespace: s.cfg.Namespace, Name: name}, &se)
	switch {
	case apierrors.IsNotFound(err):
		return nil, notFound("No run named %s in %s. Runs are removed when their SkillEval is deleted.", name, s.cfg.Namespace)
	case err != nil:
		s.cfg.Log.Error(err, "reading run", "run", name)
		return nil, unavailable("The run could not be read from the cluster: %v", err)
	}
	return &se, nil
}

// listRuns lists the SkillEvals of the namespace, newest first.
func (s *Server) listRuns(ctx context.Context) ([]Run, *apiError) {
	var list v1alpha1.SkillEvalList
	if err := s.cfg.Reader.List(ctx, &list, client.InNamespace(s.cfg.Namespace)); err != nil {
		s.cfg.Log.Error(err, "listing runs")
		return nil, unavailable("The SkillEvals could not be listed: %v", err)
	}
	runs := make([]Run, 0, len(list.Items))
	for i := range list.Items {
		runs = append(runs, newRun(&list.Items[i], s.cfg.Thresholds.Percent()))
	}
	sortRuns(runs)
	return runs, nil
}

// writeView redacts every string of v (a pointer to a response) at render
// time, encodes it and writes it with Cache-Control no-store. It fails closed:
// a body that still holds a secret value is not sent.
func (s *Server) writeView(w http.ResponseWriter, r *http.Request, v any) {
	red, _, err := s.secrets.redactor(r.Context())
	if err != nil {
		s.cfg.Log.Error(err, "render-time redaction runs without the llm-keys values")
	}
	redactStrings(reflect.ValueOf(v), red)
	body, err := encodeJSON(v)
	if err != nil {
		s.cfg.Log.Error(err, "encoding response", "path", r.URL.Path)
		writeErr(w, internalError("The response could not be encoded."))
		return
	}
	if red.Leaks(body) {
		s.cfg.Log.Error(session.ErrLeak, "response withheld", "path", r.URL.Path)
		writeErr(w, internalError("The response still held a secret value after redaction, so it is withheld."))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", cacheNoStore)
	h.Set("Content-Security-Policy", apiv1.CSP)
	_, _ = w.Write(body)
}

// encodeJSON encodes v like apiv1.WriteJSON does: HTML characters escaped, one trailing newline.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding response: %w", err)
	}
	return buf.Bytes(), nil
}

var timeType = reflect.TypeFor[time.Time]()

// redactStrings replaces every string reachable from v (struct fields,
// pointers, slices) with its redacted form. Map keys and raw JSON are left
// alone: the list and run responses carry neither untrusted keys nor raw JSON.
func redactStrings(v reflect.Value, red *session.Redactor) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			redactStrings(v.Elem(), red)
		}
	case reflect.Struct:
		if v.Type() == timeType {
			return
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				redactStrings(v.Field(i), red)
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		for i := range v.Len() {
			redactStrings(v.Index(i), red)
		}
	case reflect.String:
		if v.CanSet() {
			out, _ := red.String(v.String())
			v.SetString(out)
		}
	default:
	}
}

// round rounds v to n decimals.
func round(v float64, n int) float64 {
	p := math.Pow10(n)
	return math.Round(v*p) / p
}

func round2(v float64) float64 { return round(v, 2) }

// timePtr returns a pointer to t in UTC, nil for the zero time.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// orEmpty returns list, or an empty list for nil, so JSON has [] and never null.
func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}
