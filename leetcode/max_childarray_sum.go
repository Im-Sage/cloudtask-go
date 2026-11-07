func maxSubArray(nums []int) int {
	current := nums[0] // 以当前位置结尾的最大和
	best := nums[0]    // 已找到的最大和

	for i := 1; i < len(nums); i++ {
		if nums[i] < current+nums[i] {
			current = current + nums[i]
		} else {
			current = nums[i]
		}
		if best < current {
			best = current
		}
	}
	return best
}