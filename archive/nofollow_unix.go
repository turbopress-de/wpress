//go:build unix

package archive

import "syscall"

const oNoFollow = syscall.O_NOFOLLOW
