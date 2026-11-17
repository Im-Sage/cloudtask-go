class Solution {
public:
    vector<vector<int>> threeSum(vector<int>& nums) {
        vector<vector<int>> result; // 用来存储结果
        // 先将数组排序
        sort(nums.begin(), nums.end());
        // 遍历排序后的数组
        for (int i = 0; i < nums.size(); i++) {
            // 先固定第一个数
            int first = nums[i];
            // 剪枝优化 如果nums[i] > 0,
            // 此时为升序数组，说明后续都大于0，直接结束循环了
            if (nums[i] > 0) {
                return result;
            }
            // 去重三元组中第一个数，避免重复
            if (i > 0 && nums[i] == nums[i - 1])
                continue;
            // 使用双指针寻找剩余两个数
            int left = i + 1;
            int right = nums.size() - 1;
            while (left < right) {
                if (nums[i] + nums[left] + nums[right] == 0) {
                    result.push_back({nums[i], nums[left], nums[right]});
                    // 易错点：继续去重
                    while (left < right && nums[left] == nums[left + 1])
                        left++;
                    while (left < right && nums[right] == nums[right - 1])
                        right--;

                    left++;
                    right--; // 找到一组解之后应该继续寻找
                } else if (nums[i] + nums[left] + nums[right] < 0) {
                    left++;
                } else {
                    right--;
                }
            }
        }
        return result;
    }
};