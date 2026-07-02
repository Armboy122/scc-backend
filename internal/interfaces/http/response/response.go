package response

import (
	"encoding/json"
	"net/http"
)

// Envelope is the standard API response wrapper.
type Envelope struct {
	Data  interface{}  `json:"data"`
	Error *ErrorDetail `json:"error"`
	Meta  *Meta        `json:"meta,omitempty"`
}

// ErrorDetail holds structured error information.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Meta holds pagination metadata.
type Meta struct {
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
	Total int64 `json:"total"`
}

// JSON writes a successful JSON response.
func JSON(w http.ResponseWriter, status int, data interface{}) {
	write(w, status, Envelope{Data: data})
}

// JSONWithMeta writes a successful JSON response with pagination metadata.
func JSONWithMeta(w http.ResponseWriter, status int, data interface{}, page, limit int, total int64) {
	write(w, status, Envelope{
		Data: data,
		Meta: &Meta{Page: page, Limit: limit, Total: total},
	})
}

// Error writes an error JSON response.
func Error(w http.ResponseWriter, status int, code, message string) {
	write(w, status, Envelope{
		Error: &ErrorDetail{Code: code, Message: message},
	})
}

// NoContent writes a 204 response.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func write(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}
