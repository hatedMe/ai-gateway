package handler

import (
	"net/http"
	"strings"

	"traecli-api-proxy/config"

	"github.com/gin-gonic/gin"
)


func getAuthFromHeader(c *gin.Context, isClaude bool) string {
	appkey := c.GetHeader("Authorization")
	if appkey == "" {
		appkey = c.GetHeader("X-Api-Key")
	}
	appkey = strings.TrimPrefix(appkey, "Bearer ")
	if appkey == "" {
		if isClaude {
			c.JSON(http.StatusUnauthorized, gin.H{"type": "error", "error": map[string]string{"type": "authentication_error", "message": "Authorization or X-Api-Key header is required"}})
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization or X-Api-Key header is required"})
		}
		return ""
	}

	auth := config.GetRealToken(appkey)
	if auth == "" {
		if isClaude {
			c.JSON(http.StatusUnauthorized, gin.H{"type": "error", "error": map[string]string{"type": "authentication_error", "message": "invalid appkey"}})
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid appkey"})
		}
		return ""
	}
	return auth
}

