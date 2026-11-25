package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *TaskHandler) healthzSys(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}
