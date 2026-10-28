package requestbody

type CreateTaskRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}
