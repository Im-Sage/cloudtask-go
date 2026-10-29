package requestbody

type UpdateTaskRequest  struct {
	Title  string `json:"title" binding:"required,max=200"`
    Status string `json:"status" binding:"required,oneof=pending in_progress completed"`
}