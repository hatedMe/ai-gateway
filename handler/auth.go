package handler

import (
	"net/http"
	"strings"

	"traecli-api-proxy/config"

	"github.com/gin-gonic/gin"
)

func abortWithAuthError(c *gin.Context, isClaude bool, msg string) {
	if isClaude {
		c.JSON(http.StatusUnauthorized, gin.H{"type": "error", "error": map[string]string{"type": "authentication_error", "message": msg}})
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"error": msg})
	}
	c.Abort()
}

func AuthMiddleware(isClaude bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		appkey := c.GetHeader("Authorization")
		if appkey == "" {
			appkey = c.GetHeader("X-Api-Key")
		}

		if len(appkey) >= 7 && strings.EqualFold(appkey[:7], "Bearer ") {
			appkey = appkey[7:]
		}

		if appkey == "" {
			abortWithAuthError(c, isClaude, "Authorization or X-Api-Key header is required")
			return
		}

		auth := config.GetRealToken(appkey)
		if auth == "" {
			abortWithAuthError(c, isClaude, "invalid appkey")
			return
		}

		// Set the auth token in context for later use
		c.Set(AuthKey, auth)
		c.Next()
	}
}
