//go:build !unix

package system

// diskUsage is unavailable off unix; SelfDeploy runs on Linux, this only keeps
// development builds compiling on other systems.
func diskUsage(string) (total, used uint64, ok bool) { return 0, 0, false }
