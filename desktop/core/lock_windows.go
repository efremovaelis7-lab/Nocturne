package core

import (
	"syscall"
)

type fileLock struct{ h syscall.Handle }

// acquireLock opens the lock file without write sharing, so a second
// NOCTURNE process cannot open it while the first one runs. Windows
// releases the handle automatically if the process dies.
func acquireLock(path string) (*fileLock, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errno, ok := err.(syscall.Errno); ok && errno == 32 { // ERROR_SHARING_VIOLATION
			return nil, errLocked
		}
		return nil, err
	}
	return &fileLock{h: h}, nil
}

func (l *fileLock) release() {
	if l == nil || l.h == 0 {
		return
	}
	syscall.CloseHandle(l.h)
	l.h = 0
}
