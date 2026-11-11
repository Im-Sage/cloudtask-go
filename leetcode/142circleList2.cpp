/**
 * Definition for singly-linked list.
 * struct ListNode {
 *     int val;
 *     ListNode *next;
 *     ListNode(int x) : val(x), next(NULL) {}
 * };
 */

 // 为什么第二次相遇一定在入口？, 其实这个是个数学问题
class Solution {
public:
    ListNode *detectCycle(ListNode *head) {
        // 核心方法是快慢指针法(Floyd判圈算法)
        // slow 每次走一步  fast每次走两步
        // 若fast到达链表末尾则说明没有环，若快慢指针相遇则说明有环,但相遇节点不一定是环的入口(手动加粗)
        // 这题难点就是找到环的入口节点
        ListNode * slow = head;
        ListNode * fast = head;

        while(fast != NULL && fast -> next != NULL){
            slow = slow -> next;
            fast = fast -> next -> next;
            if (slow == fast){  // 说明存在环
                ListNode* p = head;
                // 一个指针从head开始，slow指针从p开始,两个指针每次各走一步,再次相遇的节点即为入环节点
                while(p != slow){
                    p = p-> next;
                    slow = slow -> next;
                }
                return p;
            }           
        }
        return NULL;
    }
};