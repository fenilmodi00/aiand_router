package main

import "syscall"

// openNoFollow is a no-op on Windows, which has no O_NOFOLLOW; the
// regular-file check after opening still guards against symlinked paths.
const openNoFollow = 0

var _ = syscall.O_NONBLOCK
