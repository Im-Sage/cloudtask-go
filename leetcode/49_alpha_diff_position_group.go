package main

import (
	"fmt"
	"slices"
)

func groupAnagrams(strs []string) [][]string {
	mp := make(map[string][]string)

	for _, str := range strs {
		b := []byte(str)
		slices.Sort(b)
		key := string(b)
		mp[key] = append(mp[key], str) // 向map中添加元素，其中value是一个切片，append函数会将str添加到对应的切片中
	}

	var ans [][]string

	for _, values := range mp {
		ans = append(ans, values)
	}
	return ans
}

func main() {
	strs := []string{"eat", "tea", "tan", "ate", "nat", "bat"}
	result := groupAnagrams(strs)
	fmt.Println(result)
}

// class Solution {
// public:
//     vector<vector<string>> groupAnagrams(vector<string>& strs) {
//         // strs = ["eat","tea","tan","ate","nat","bat"]
//         unordered_map<string,vector<string>> mp;
//         for(auto str:strs) {
//             string key = str;
//             sort(key.begin(), key.end());
//             mp[key].push_back(str);
//         }

//         vector<vector<string>> ans;
//         for(auto it=mp.begin(); it!=mp.end(); it++) {
//             ans.push_back(it->second);
//         }
//         return ans;
//     }
// };
