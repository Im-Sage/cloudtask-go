class Solution {
public:
    vector<int> topKFrequent(vector<int>& nums, int k) {
        sort(nums.begin(), nums.end());
        unordered_map<long long, int> count;
        vector<int> res;
        for (int num : nums) {
            count[num]++;
        }
        // 桶初始化  1,1,1,2,2,3,4,4   -> buckets[3] = [1], buckets[2] = [2,4],
        // buckets[1] = [3]
        vector<vector<int>> buckets(nums.size() + 1);
        for (const auto& entry : count) {
            int num = entry.first;
            int frequency = entry.second;
            buckets[frequency].push_back(num);
        }

        /**
        buckets[8] = []
        buckets[7] = []
        buckets[6] = []
        buckets[5] = []
        buckets[4] = []
        buckets[3] = [1]       // 1 出现了 3 次
        buckets[2] = [2,4]     // 2 和 4 各出现了 2 次
        buckets[1] = [3]       // 3 出现了 1 次
        */
        for (int frequency = nums.size(); frequency >= 1; frequency--) {
            for (
                int num : buckets
                    [frequency]) { // 当buckets[frequency]没有元素，这个for循环将不会执行
                res.push_back(num);
                if (res.size() == k) {
                    return res;
                }
            }
        }

        return res;
    }
};