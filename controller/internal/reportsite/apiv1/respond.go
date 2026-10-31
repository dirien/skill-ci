package apiv1

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// CSP is the Content-Security-Policy of every API response: JSON is never a document.
const CSP = "default-src 'none'; frame-ancestors 'none'"

// WriteJSON writes v as a JSON response with the given status and
// Cache-Control. It encodes into a buffer first, so an encoding error becomes
// a 500 instead of a half-written body.
func WriteJSON(w http.ResponseWriter, status int, cacheControl string, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(v); err != nil {
		WriteError(w, http.StatusInternalServerError, CodeInternal, "The response could not be encoded.")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", cacheControl)
	h.Set("Content-Security-Policy", CSP)
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// WriteError writes an Error body with Cache-Control no-store.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	body, _ := json.Marshal(Error{Error: ErrorBody{Status: status, Code: code, Message: message}}) // plain strings always encode
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", CSP)
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
