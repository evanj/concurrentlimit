package concurrentlimit

import "testing"

func TestNoLimit(t *testing.T) {
	limiter := NoLimit()

	endFuncs := []func(){}
	for i := 0; i < 10000; i++ {
		end, err := limiter.Start()
		if err != nil {
			t.Fatal("NoLimit should never return an error")
		}
		if end == nil {
			t.Fatal("end must not be nil")
		}

		endFuncs = append(endFuncs, end)
	}

	// calling all the end functions should work
	for _, end := range endFuncs {
		end()
	}
}
