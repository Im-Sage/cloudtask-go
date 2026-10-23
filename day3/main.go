package main

import (
	"fmt"
)

func main() {
	TaskManager := NewTaskManager()
	task1, error1 := TaskManager.Add("Chen")
	if error1 != nil {
		fmt.Println("title is empty:", error1)
		return
	}
	task2, error2_ := TaskManager.Add("Sai")
	if error2_ != nil {
		fmt.Println("title is empty:", error2_)
		return
	}
	fmt.Println(task1, task2)

	var tasks []Task
	tasks = append(tasks, task1, task2)

	TaskManager.SaveTasks("./data.json", tasks)

	tasks, err := TaskManager.LoadTasks("./data.json")
	if err != nil {
		fmt.Println("Error loading tasks:", err)
		return
	}

	for _, task := range tasks {
		fmt.Println("Loaded Task:", task)
	}
}
