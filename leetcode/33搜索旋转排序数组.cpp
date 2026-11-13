class Solution {
public:
    int search(vector<int>& nums, int target) {
        // nums = [4,5,6,7,0,1,2]  其中7->0是下降的，其他都是递增，这个位置就是断点
        // 一段连续区间只要不包含这个断点，就一定是有序的。
        // 随便选一个位置作为mid，考察[left,mid]和[mid,right]
        // 挺绕，多推演
        int left = 0;   int right = nums.size() - 1;
        while(left <= right){
            int mid = (left + right) / 2;
            if(nums[mid] == target){
                return mid;
            }
            if(nums[left] <= nums[mid]){ // 左半段有序
                if(target>=nums[left] && target < nums[mid]){ // 说明在左半段
                    right = mid - 1;
                }else{ //在右半段
                    left = mid + 1;
                }
            }else{
                if(nums[mid] < target && target <= nums[right]){
                    // 在右半段
                    left = mid + 1;
                }else{
                    right = mid - 1;
                }
            }
        }
        return -1;
    }
};