package response

import (
	"errors"
	"net/http"

	appErr "git.pinquest.cn/ai-customer/pkg/errors"
	"github.com/gin-gonic/gin"
)

func WriteError(c *gin.Context, err error) {
	status := HTTPStatus(err)
	if status >= http.StatusInternalServerError {
		c.JSON(status, gin.H{"error": http.StatusText(http.StatusInternalServerError)})
		return
	}
	c.JSON(status, Error(status, err.Error()))
}

func HTTPStatus(err error) int {
	switch {
	case errors.Is(err, appErr.ErrExternalServiceUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, appErr.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, appErr.ErrInvalidArgument):
		return http.StatusBadRequest
	case errors.Is(err, appErr.ErrKnowledgeHubUnavailable):
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}
