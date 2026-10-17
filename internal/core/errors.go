package core

import "fmt"

// SyncError carries a machine-readable code the UI reacts to (LOCKED, CONFLICT, AUTH, ...).
type SyncError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

func (e *SyncError) Error() string { return e.Message }

func Errf(code, format string, args ...any) *SyncError {
	return &SyncError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the SyncError code of err, or "" for other errors.
func CodeOf(err error) string {
	if se, ok := err.(*SyncError); ok {
		return se.Code
	}
	return ""
}
