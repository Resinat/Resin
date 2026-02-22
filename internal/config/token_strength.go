package config

import zxcvbn "github.com/ccojocar/zxcvbn-go"

const weakTokenScoreThreshold = 3

// IsWeakToken returns whether token strength is considered weak.
// Empty token is handled by auth mode (disabled), so this function treats it as not weak.
func IsWeakToken(token string) bool {
	if token == "" {
		return false
	}
	result := zxcvbn.PasswordStrength(token, nil)
	return result.Score < weakTokenScoreThreshold
}
