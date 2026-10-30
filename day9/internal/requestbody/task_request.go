package requestbody

type CreateTaskRequest struct {
	Title       string `json:"title" binding:"required,max=200"`
	Description string `json:"description" binding:"required"`
}

type ChangeStatusRequest struct {
	Status string `json:"status"`
}
