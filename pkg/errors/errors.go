package errors

import "errors"

var (
	ErrExternalServiceUnavailable = errors.New("external service unavailable")
	ErrNotFound                   = errors.New("not found")
	ErrInvalidArgument            = errors.New("invalid argument")
	ErrKnowledgeHubUnavailable    = errors.New("knowledge hub unavailable")
)
