func isValid(s string) bool {
	// 核心思想 遇到左括号入栈，右括号则判断是否匹配，匹配则出栈。不匹配则返回false
	stack := []rune{}
	for _, c := range s {
		if c == '(' || c == '{' || c == '[' {
			stack = append(stack, c)
		} else if c == ')' || c == '}' || c == ']' {
			// 若此时栈为空则匹配失败，因为这个右括号将无法和别的左括号匹配
			if len(stack) == 0 {
				return false
			}
			// 不为空，首先获取栈顶元素,再出栈
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1] // 栈顶元素出栈
			// 判断栈顶元素是否匹配
			if c == ')' && top != '(' ||
				c == '}' && top != '{' ||
				c == ']' && top != '[' {
				return false
			}
		}
	}
	if len(stack) == 0 {
		return true
	} else {
		return false
	}
}