/**
 * Definition for a binary tree node.
 * struct TreeNode {
 *     int val;
 *     TreeNode *left;
 *     TreeNode *right;
 *     TreeNode() : val(0), left(nullptr), right(nullptr) {}
 *     TreeNode(int x) : val(x), left(nullptr), right(nullptr) {}
 *     TreeNode(int x, TreeNode *left, TreeNode *right) : val(x), left(left),
 * right(right) {}
 * };
 */
class Solution {
public:
    // ==============2026.11.07=================
    vector<vector<int>> levelOrder(TreeNode* root) {
        vector<vector<int>> result;
        // 层序遍历一定会用到队列
        if (root == nullptr)
        {
            return result;
        } 
        queue<TreeNode*> q;
        // 根节点先入队
        q.push(root); 
        while (!q.empty()) {
            vector<int> levelResult;
            int size = q.size(); // 获取每层的节点数量
            for (int i = 0; i < size; i++) {
                // 队头元素出队
                TreeNode* node = q.front();  // 获取队头元素
                q.pop(); // 再出队
                levelResult.push_back(node->val); 
                if (node->left != nullptr) {
                    q.push(node->left);
                }
                if (node->right != nullptr) {
                    q.push(node->right);
                }
            }
            result.push_back(levelResult);
        }

        return result;
    }