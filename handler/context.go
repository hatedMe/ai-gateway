package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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

// GetRequestID safely retrieves the request ID from context
func GetRequestID(c *gin.Context) string {
	if rid, ok := c.Get("requestID"); ok {
		if s, ok := rid.(string); ok {
			return s
		}
	}
	newRID := uuid.New().String()
	c.Set("requestID", newRID)
	return newRID
}