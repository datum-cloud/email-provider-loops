package loops

import (
	"errors"
	"fmt"
	"net/http"
)

// Error represents an error returned by the Loops API.
type Error struct {
	StatusCode int
	Body       string
}

func (e *Error) Error() string {
	return fmt.Sprintf("api request failed with status %d: %s", e.StatusCode, e.Body)
}

// IsErrorStatus checks if the error is a Loops API error with the given status code.
func isErrorStatus(err error, status int) bool {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == status
	}
	return false
}

// IsBadRequest checks if the error represents a 400 Bad Request response.
func IsBadRequest(err error) bool {
	return isErrorStatus(err, http.StatusBadRequest)
}

// IsNotFound checks if the error represents a 404 Not Found response.
func IsNotFound(err error) bool {
	return isErrorStatus(err, http.StatusNotFound)
}

// IsConflict checks if the error represents a 409 Conflict response.
func IsConflict(err error) bool {
	return isErrorStatus(err, http.StatusConflict)
}
