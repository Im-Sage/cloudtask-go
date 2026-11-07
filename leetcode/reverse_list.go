/**
 * Definition for singly-linked list.
 * type ListNode struct {
 *     Val int
 *     Next *ListNode
 * }
 */
func reverseList(head *ListNode) *ListNode {
	var prev *ListNode
	curr := head

	for curr != nil {
		// 保存当前节点
		next := curr.Next
		curr.Next = prev
		prev = curr
		curr = next
	}
	return prev
}