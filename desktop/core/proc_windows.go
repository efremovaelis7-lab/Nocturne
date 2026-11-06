package core

// terminateNocturne is not needed on Windows: there is no older build, and a
// dead process releases its lock automatically.
func terminateNocturne(pid int) {}
