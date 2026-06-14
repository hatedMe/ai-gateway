package handler

import (
	"github.com/gin-gonic/gin"
)

// AuthKey is the context key for the authenticated token
const AuthKey = "authToken"

// GetAuthFromContext retrieves the authenticated token from the Gin context
func GetAuthFromContext(c *gin.Context) string {
	if auth, exists := c.Get(AuthKey); exists {
		if authStr, ok := auth.(string); ok {
			return authStr
		}
	}
	return ""
}