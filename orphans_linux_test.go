package goboot

import "testing"

func TestZombieChildOfPID1(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{"Name:\tchild\nState:\tZ (zombie)\nPPid:\t1\n", true},
		{"Name:\tchild\nState:\tS (sleeping)\nPPid:\t1\n", false},
		{"Name:\tchild\nState:\tZ (zombie)\nPPid:\t2\n", false},
		{"garbage", false},
	} {
		if got := zombieChildOfPID1([]byte(tc.status)); got != tc.want {
			t.Fatalf("status %q: got %v, want %v", tc.status, got, tc.want)
		}
	}
}
