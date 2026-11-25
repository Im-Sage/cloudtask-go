package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *HealthHandler) healthzSys(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}
