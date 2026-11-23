//go:build race

package check_test

// slowdown is how much slower the race detector makes a run, for time budgets measured under it.
const slowdown = 10
