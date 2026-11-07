class Solution {
public:
    // 因为没有使用排序，所以时间复杂度可以达到O(n)
    int longestConsecutive(vector<int>& nums) {
        unordered_set<long long> values;
        values.reserve(nums.size());  // 为提前预计插入的数字预留容量

        for (int num : nums){
            values.insert(num);  // 去重，并可以快速定位到元素
        }
        int longest = 0;

        for(long long start:values){
            // values.count(start - 1)查看 集合里是否存在比start小1的数字，存在返回1，不存在返回0
            if (values.count(start - 1)){  
                continue; // 不是序列起点直接跳过
            }

            long long current = start;
            int length = 1;

            while(values.count(current + 1)) {  // 当不存在时，跳出循环
                ++current;
                ++length;
            }
            longest = max(longest, length);
        }
        return longest;
    }
};