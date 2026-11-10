class Solution {
public:
    int maxArea(vector<int>& height) {
        // 核心思想是利用双指针，每次移动较短的一边
        int left = 0;   int right = height.size() - 1;
        int max = 0;    int current = 0;
        while(left < right){
            current = min(height[left], height[right]) * (right - left);
            if(max < current){
                max = current;
            }
            if(height[left] < height[right]){
                left++;
            }else{
                right--;
            }
        }
        return max;
    }
};