class Solution {
public:
    vector<vector<int>> merge(vector<vector<int>>& intervals) {
        // 合并区间的关键：先按左端点排序，再从左到右扫描
        sort(intervals.begin(), intervals.end()); // 按左端点排序

        vector<vector<int>> result;

        for (const auto& interval : intervals) {
            if(result.empty() || interval[0] > result.back()[1]){
                // 没有重叠，开始一个新区间,即result尾部数组的第二个元素小于当前数组的第一个元素，即不重叠
                result.push_back(interval);
            }else{
                result.back()[1] = max(interval[1], result.back()[0]);
            }
        }
        return result;
    }
};