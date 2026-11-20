class Solution {
public:
    int lengthOfLongestSubstring(string s) {
        
        int maxlength = 0;
        unordered_set<char> window; // 核心是滑动窗口
        int left = 0; 
        
        for(int right = 0; right < s.size(); right++){
            // 移除左侧字符，直到窗口中不再包含s[right]
            while(window.count(s[right]) > 0){  // s[right] 在窗口里时返回1进入循环
                window.erase(s[left]);
                left++;
            }
            window.insert(s[right]);

            maxlength = max(right - left + 1, maxlength);
        }
        return maxlength;
    }
};