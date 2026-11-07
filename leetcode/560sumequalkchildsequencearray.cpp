class Solution {

/**
 * 算法思想：前缀和 + 哈希表

    设 prefix[i] 表示前 i 个元素之和，那么区间 [j, i-1] 的和为：
    prefix[i] - prefix[j]
    要让区间和等于 k，就需要：
    prefix[j] = prefix[i] - k
    因此，遍历数组时，只需要查询：当前前缀和减去 k，在之前出现过多少次。 每出现一次，就对应一个符合条件的子数组。
    哈希表保存“前缀和 → 出现次数”。
 */

public:
    int subarraySum(vector<int>& nums, int k) {
        unordered_map<long long, int> count;
        count[0] = 1; // 前缀和为0出现过一次
        long long sum = 0;
        int answer = 0;

        for (int num : nums) {
            sum = sum + num;
            // 查询之前有多少个前缀和等于sum-k
            auto it = count.find(sum - k);
            if (it != count.end()) { // 找到这个前缀和的键
                // 首先先记录当前前缀和
                answer = answer + count[sum - k];
            }
            count[sum]++;
        }
        return answer;
    }
};