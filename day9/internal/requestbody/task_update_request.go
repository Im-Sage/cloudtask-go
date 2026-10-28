package requestbody

type UpdateTaskRequest  struct {
	Title  string `json:"title"`
    Status string `json:"status"`
}