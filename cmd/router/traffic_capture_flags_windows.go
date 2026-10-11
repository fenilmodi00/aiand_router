//go:build windows

package main

// openCaptureFileFlags adds the O_NOFOLLOW (refuse symlinked capture paths) and
// O_NONBLOCK guards. Windows has no O_NOFOLLOW; the follow-up Stat + IsRegular
// check in newTrafficCaptureFromEnvironment is what keeps a symlinked or
// non-regular path from being written there.
const openCaptureFileFlags = 0
